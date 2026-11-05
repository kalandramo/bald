package contract

// appkit_e2e_test.go —— 声明式装配的端到端取用证据（一进程、真配置、真生命周期）。
//
// 前面几层判据各自成立，不等于串起来成立：这段跑的是完整链路
//
//	configs/*.yaml（契约文本）→ appkit 加载 + StrictContract 运行期契约门
//	  → 计划（server.rocketmq 段命中 AppProvider）→ Build → Start
//	  → 就绪闸门（waitForEndpoints 读到描述性 rocketmq://addr）
//
// rocketmq 没有 HTTP 那样的「线上取用」——判据落在「契约 NameServer 在真进程里
// 被 Start 走全（Init 读表 + Connect）并反映到服务端点」，中间任何一环（段名、
// 映射、装配时机、就绪形状）断掉都判红。订阅者的真上线需真 NameServer，离线
// 边界与失败形状见 mapping_offline_test.go 文件头（§7.24 同口径）。

// 入口必须是 appkit.Serve——assemble（Provider 计划与 server 段构建）只挂在
// Serve 上；直接 NewApp+Run 会跳过整个段驱动装配，provider 不进计划、服务器不
// 构造，且一声不响。同 transport/{tcp,graphql,kafka}/contract 的 e2e 起手式。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/kalandramo/bald/pkg/appkit"
	rocketmq "github.com/kalandramo/bald/transport/rocketmq"
)

// isolateFlags 把 pflag.CommandLine 换成空的进程内 FlagSet——go test 的
// -test.* flag 会落进全局 FlagSet，Serve 的 --config/--plan 容错扫描与之竞争。
func isolateFlags(t *testing.T) {
	t.Helper()
	old := pflag.CommandLine
	pflag.CommandLine = pflag.NewFlagSet("rocketmq-contract-e2e", pflag.ContinueOnError)
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
		"    name: rocketmq-contract-e2e\n"+
		"    version: v0.0.0\n"+
		"server:\n"+
		"    rocketmq:\n"+
		"        name_server_addrs:\n"+
		"            - %s\n", addr))

	var wired atomic.Bool
	var captured atomic.Pointer[rocketmq.Server]
	opts := []appkit.Option{
		appkit.WithRole("rocketmq-e2e"),
		appkit.ConfigFile(cfgPath),
		// Gate3：未知契约键启动即报——这条腿同时在判「server.rocketmq 是契约内段」。
		appkit.StrictContract(),
		appkit.Providers(AppProvider(
			WithAfterBuild(func(_ context.Context, s *rocketmq.Server) error {
				// 无订阅者的接线形状：after-build 只做捕获。带订阅者的
				// Start 在离线必红（SDK 路由拉取），归 mapping_offline 判。
				captured.Store(s)
				wired.Store(true)
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

	// 就绪判据：Start（Init 读表 + Connect）完成后端点才变描述性。appkit 的
	// waitForEndpoints 读的正是这个面——它若卡住，Serve 会在超时后报错返回。
	want := "rocketmq://" + addr
	deadline := time.Now().Add(20 * time.Second)
	srv := captured.Load()
	for (srv == nil || srv.Endpoint() != want) && time.Now().Before(deadline) {
		select {
		case serveErr := <-errCh:
			t.Fatalf("appkit.Serve 提前返回 (%v)，契约地址 %s 的端点没起来 (wired=%v)", serveErr, addr, wired.Load())
		default:
		}
		time.Sleep(50 * time.Millisecond)
		srv = captured.Load()
	}
	if !wired.Load() {
		t.Fatal("after-build 接线没被执行（provider 没进计划？）")
	}
	if srv == nil {
		t.Fatalf("20s 内 after-build 都没拿到 server 实例（Build 没跑？）")
	}
	if ep := srv.Endpoint(); ep != want {
		t.Fatalf("Endpoint() = %q, want %s（Start 没走完或契约地址没落到服务端面）", ep, want)
	}
}

// TestAppProviderEndToEnd_UnknownContractKeyFailsServe 反证：契约门真的在跑——
// 段名写错（server.rmq）时启动必须失败，而不是静默少起一台服务器。
func TestAppProviderEndToEnd_UnknownContractKeyFailsServe(t *testing.T) {
	isolateFlags(t)
	cfgPath := writeContractFile(t, ""+
		"app:\n"+
		"    name: rocketmq-contract-e2e\n"+
		"    version: v0.0.0\n"+
		"server:\n"+
		"    rmq:\n"+
		"        name_server_addrs:\n"+
		"            - 127.0.0.1:9876\n")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := appkit.Serve(ctx,
		appkit.WithRole("rocketmq-e2e"),
		appkit.ConfigFile(cfgPath),
		appkit.StrictContract(),
		appkit.Providers(AppProvider()),
	)
	if err == nil {
		t.Fatal("Serve 成功，未知契约键没被契约门挡住（StrictContract 腿失效）")
	}
	if !strings.Contains(err.Error(), "rmq") {
		t.Fatalf("err = %v, want attribution to the typo'd key rmq", err)
	}
}
