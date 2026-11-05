package contract

// appkit_e2e_test.go —— 声明式装配的端到端取用证据（一进程、真配置、真端口）。
//
// 前面几层判据各自成立，不等于串起来成立：这段跑的是完整链路
//
//	configs/*.yaml（契约文本）→ appkit 加载 + StrictContract 运行期契约门
//	  → 计划（server.mcp 段命中 AppProvider）→ Build → Start → MCP 协议往返
//
// 判据落在「按契约文本写下的地址上，真 MCP 客户端能 initialize 并 tools/call，
// 且 initialize 里的 serverInfo 就是契约 name/version」，
// 中间任何一环（段名、映射、装配时机、监听）断掉都判红。

// 入口必须是 appkit.Serve——assemble（Provider 计划与 server 段构建）只挂在
// Serve 上；直接 NewApp+Run 会跳过整个段驱动装配，provider 不进计划、服务器不
// 构造，且一声不响。同 transport/tcp 与 transport/webrtc contract 的 e2e 起手式。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	mcpgoclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpgomcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/kalandramo/bald/pkg/appkit"
)

// initializeAttempt 一次 initialize 握手（不 Fatal——e2e 的启动是异步的，
// 轮询到通为止）。失败时返回的 client 已 Close。
func initializeAttempt(ep string) (*mcpgoclient.Client, *mcpgomcp.InitializeResult, error) {
	httpTransport, err := mcptransport.NewStreamableHTTP(ep + "/mcp")
	if err != nil {
		return nil, (*mcpgomcp.InitializeResult)(nil), err
	}
	c := mcpgoclient.NewClient(httpTransport)
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		return nil, (*mcpgomcp.InitializeResult)(nil), err
	}
	res, err := c.Initialize(ctx, mcpgomcp.InitializeRequest{
		Params: mcpgomcp.InitializeParams{
			ProtocolVersion: mcpgomcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcpgomcp.Implementation{Name: "e2e-test", Version: "1.0.0"},
		},
	})
	if err != nil {
		_ = c.Close()
		return nil, (*mcpgomcp.InitializeResult)(nil), err
	}
	return c, res, nil
}

// isolateFlags 把 pflag.CommandLine 换成空的进程内 FlagSet——go test 的
// -test.* flag 会落进全局 FlagSet，Serve 的 --config/--plan 容错扫描与之竞争。
func isolateFlags(t *testing.T) {
	t.Helper()
	old := pflag.CommandLine
	pflag.CommandLine = pflag.NewFlagSet("mcp-contract-e2e", pflag.ContinueOnError)
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
		"    name: mcp-contract-e2e\n"+
		"    version: v0.0.0\n"+
		"server:\n"+
		"    mcp:\n"+
		"        address: %s\n"+
		"        name: e2e-mcp\n"+
		"        version: v0.0.1\n", addr))

	opts := []appkit.Option{
		appkit.WithRole("mcp-e2e"),
		appkit.ConfigFile(cfgPath),
		// Gate3：未知契约键启动即报——这条腿同时在判「server.mcp 是契约内段」。
		appkit.StrictContract(),
		appkit.Providers(AppProvider(echoOption())),
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

	// 真进程里 Serve 的启动是异步的，initialize 轮询到通为止。
	ep := "http://" + addr
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		c, res, err := initializeAttempt(ep)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		defer c.Close() //nolint:errcheck // 测试收尾
		if res.ServerInfo.Name != "e2e-mcp" || res.ServerInfo.Version != "v0.0.1" {
			t.Fatalf("ServerInfo = %q %q, want e2e-mcp v0.0.1（契约 name/version 没落进协议？）",
				res.ServerInfo.Name, res.ServerInfo.Version)
		}
		if got := callEcho(t, c, "pong:e2e"); got != "pong:e2e" {
			t.Fatalf("echo = %q, want pong:e2e", got)
		}
		return
	}
	t.Fatalf("契约地址 %s 没在真进程里跑通 MCP 握手: %v", addr, lastErr)
}

// TestAppProviderEndToEnd_UnknownContractKeyFailsServe 反证：契约门真的在跑——
// 段名写错（server.mcp2）时启动必须失败，而不是静默少起一台服务器。
func TestAppProviderEndToEnd_UnknownContractKeyFailsServe(t *testing.T) {
	isolateFlags(t)
	cfgPath := writeContractFile(t, ""+
		"app:\n"+
		"    name: mcp-contract-e2e\n"+
		"    version: v0.0.0\n"+
		"server:\n"+
		"    mcp2:\n"+
		"        address: 127.0.0.1:19702\n")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := appkit.Serve(ctx,
		appkit.WithRole("mcp-e2e"),
		appkit.ConfigFile(cfgPath),
		appkit.StrictContract(),
		appkit.Providers(AppProvider()),
	)
	if err == nil {
		t.Fatal("Serve 成功，未知契约键没被契约门挡住（StrictContract 腿失效）")
	}
	if !strings.Contains(err.Error(), "mcp2") {
		t.Fatalf("err = %v, want attribution to the typo'd key mcp2", err)
	}
}
