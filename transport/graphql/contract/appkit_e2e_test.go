package contract

// appkit_e2e_test.go —— 声明式装配的端到端取用证据（一进程、真配置、真端口）。
//
// 前面几层判据各自成立，不等于串起来成立：这段跑的是完整链路
//
//	configs/*.yaml（契约文本）→ appkit 加载 + StrictContract 运行期契约门
//	  → 计划（server.graphql 段命中 AppProvider）→ Build → Start → 线上取用
//
// 判据落在「按契约文本写下的地址上真的能取到 after-build 登记的路由」，
// 中间任何一环（段名、映射、装配时机、监听）断掉都判红。

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/kalandramo/bald/pkg/appkit"
	graphql "github.com/kalandramo/bald/transport/graphql"
)

// isolateFlags 把 pflag.CommandLine 换成空的进程内 FlagSet——go test 的
// -test.* flag 会落进全局 FlagSet，Serve 的 --config/--plan 容错扫描与之竞争。
func isolateFlags(t *testing.T) {
	t.Helper()
	old := pflag.CommandLine
	pflag.CommandLine = pflag.NewFlagSet("graphql-contract-e2e", pflag.ContinueOnError)
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
		"    name: graphql-contract-e2e\n"+
		"    version: v0.0.0\n"+
		"server:\n"+
		"    graphql:\n"+
		"        addr: %s\n", addr))

	var wired atomic.Bool
	opts := []appkit.Option{
		appkit.WithRole("graphql-e2e"),
		appkit.ConfigFile(cfgPath),
		// Gate3：未知契约键启动即报——这条腿同时在判「server.graphql 是契约内段」。
		appkit.StrictContract(),
		appkit.Providers(AppProvider(
			WithAfterBuild(func(_ context.Context, s *graphql.Server) error {
				wired.Store(true)
				s.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, "pong:e2e")
				})
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
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("appkit.Serve returned %v, want nil or context.Canceled", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("appkit.Serve 未在取消后返回")
		}
	})

	body := ""
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/health")
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatalf("read body: %v", readErr)
		}
		body = string(raw)
		break
	}
	if body == "" {
		select {
		case serveErr := <-errCh:
			t.Fatalf("appkit.Serve 提前返回 (%v)，契约地址 %s 没取到路由: %v", serveErr, addr, lastErr)
		default:
		}
		t.Fatalf("契约地址 %s 未在真进程里提供服务: %v (wired=%v)", addr, lastErr, wired.Load())
	}
	if !wired.Load() {
		t.Fatal("after-build 接线没被执行（provider 没进计划？）")
	}
	if body != "pong:e2e" {
		t.Fatalf("response = %q, want pong:e2e", body)
	}
}

// TestAppProviderEndToEnd_UnknownContractKeyFailsServe 反证：契约门真的在跑——
// 段名写错（server.graphl）时启动必须失败，而不是静默少起一台服务器。
func TestAppProviderEndToEnd_UnknownContractKeyFailsServe(t *testing.T) {
	isolateFlags(t)
	cfgPath := writeContractFile(t, ""+
		"app:\n"+
		"    name: graphql-contract-e2e\n"+
		"    version: v0.0.0\n"+
		"server:\n"+
		"    graphl:\n"+
		"        addr: 127.0.0.1:19799\n")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := appkit.Serve(ctx,
		appkit.WithRole("graphql-e2e"),
		appkit.ConfigFile(cfgPath),
		appkit.StrictContract(),
		appkit.Providers(AppProvider()),
	)
	if err == nil {
		t.Fatal("Serve 成功，未知契约键没被契约门挡住（StrictContract 腿失效）")
	}
	if !strings.Contains(err.Error(), "graphl") {
		t.Fatalf("err = %v, want attribution to the typo'd key graphl", err)
	}
}
