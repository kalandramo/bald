package contract

// appkit_e2e_test.go —— 声明式装配的端到端取用证据（一进程、真配置、真端口）。
//
// 前面几层判据各自成立，不等于串起来成立：这段跑的是完整链路
//
//	configs/*.yaml（契约文本）→ appkit 加载 + StrictContract 运行期契约门
//	  → 计划（server.webrtc 段命中 AppProvider）→ Build → Start → 信令往返
//
// 判据落在「按契约文本写下的地址上真的能走一次 signalHandler 往返，且 after-build
// 接线的中间件与消息处理器都在产物上」，中间任何一环（段名、映射、装配时机、监听）
// 断掉都判红。

// 入口必须是 appkit.Serve——assemble（Provider 计划与 server 段构建）只挂在
// Serve 上；直接 NewApp+Run 会跳过整个段驱动装配，provider 不进计划、服务器不
// 构造，且一声不响。同 transport/tcp/contract 的 e2e 起手式。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/kalandramo/bald/pkg/appkit"
	webrtc "github.com/kalandramo/bald/transport/webrtc"
)

// isolateFlags 把 pflag.CommandLine 换成空的进程内 FlagSet——go test 的
// -test.* flag 会落进全局 FlagSet，Serve 的 --config/--plan 容错扫描与之竞争。
func isolateFlags(t *testing.T) {
	t.Helper()
	old := pflag.CommandLine
	pflag.CommandLine = pflag.NewFlagSet("webrtc-contract-e2e", pflag.ContinueOnError)
	t.Cleanup(func() { pflag.CommandLine = old })
}

// writeContractFile 落一份契约 yaml 并返回其路径。
func writeContractFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bootstrap.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestAppProviderEndToEnd_ConfigFileToWire(t *testing.T) {
	isolateFlags(t)
	addr := loopbackAddr(t)
	cfgPath := writeContractFile(t, fmt.Sprintf(""+
		"app:\n"+
		"    name: webrtc-contract-e2e\n"+
		"    version: v0.0.0\n"+
		"server:\n"+
		"    webrtc:\n"+
		"        addr: %s\n"+
		"        path: /e2e-signal\n"+
		"        codec: json\n", addr))

	var handlerWired, mwWired bool
	opts := []appkit.Option{
		appkit.WithRole("webrtc-e2e"),
		appkit.ConfigFile(cfgPath),
		// Gate3：未知契约键启动即报——这条腿同时在判「server.webrtc 是契约内段」，
		// 且 codec: json 能过注册表门（TestMain 已注册 json codec）。
		appkit.StrictContract(),
		appkit.Providers(AppProvider(
			WithAfterBuild(func(_ context.Context, s *webrtc.Server) error {
				s.Use(func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						mwWired = true
						w.Header().Set("X-E2E-Mw", "on")
						next.ServeHTTP(w, r)
					})
				})
				s.RegisterMessageHandler(7, func(_ webrtc.SessionID, payload webrtc.MessagePayload) error {
					return nil
				}, nil)
				handlerWired = s.GetMessageHandler(7) != nil
				return nil
			}),
		)),
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- appkit.Serve(ctx, opts...) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("appkit.Serve returned %v, want nil or context.Canceled", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("appkit.Serve 未在取消后返回")
		}
	})

	// 契约地址上的信令往返：400 + invalid offer = signalHandler 的 offer 门被执行；
	// X-E2E-Mw 头 = after-build 的 Use 进了服务链（缺陷②在完整链路上的表现）。
	url := "http://" + addr + "/e2e-signal"
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "invalid offer") {
			t.Fatalf("POST %s = %d %q, want 400 invalid offer", url, resp.StatusCode, string(body))
		}
		if got := resp.Header.Get("X-E2E-Mw"); got != "on" {
			t.Fatalf("X-E2E-Mw = %q, want on（装配期接线的中间件没进链？）", got)
		}
		if !mwWired {
			t.Fatal("middleware closure never executed")
		}
		if !handlerWired {
			t.Fatal("after-build 注册的消息处理器不在产物上（接线的不是进服务的那台？）")
		}
		return
	}
	t.Fatalf("契约地址 %s 没在真进程里服务信令: %v", addr, lastErr)
}

// TestAppProviderEndToEnd_UnknownContractKeyFailsServe 反证：契约门真的在跑——
// 段名写错（server.webrt）时启动必须失败，而不是静默少起一台服务器。
func TestAppProviderEndToEnd_UnknownContractKeyFailsServe(t *testing.T) {
	isolateFlags(t)
	cfgPath := writeContractFile(t, ""+
		"app:\n"+
		"    name: webrtc-contract-e2e\n"+
		"    version: v0.0.0\n"+
		"server:\n"+
		"    webrt:\n"+
		"        addr: 127.0.0.1:19701\n")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := appkit.Serve(ctx,
		appkit.WithRole("webrtc-e2e"),
		appkit.ConfigFile(cfgPath),
		appkit.StrictContract(),
		appkit.Providers(AppProvider()),
	)
	if err == nil {
		t.Fatal("Serve 成功，未知契约键没被契约门挡住（StrictContract 腿失效）")
	}
	if !strings.Contains(err.Error(), "webrt") {
		t.Fatalf("err = %v, want attribution to the typo'd key webrt", err)
	}
}
