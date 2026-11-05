package contract

// mapping_offline_test.go —— server.mcp 映射行的线上判据
// （真监听、真 MCP 协议客户端往返；「offline」指不需要任何外部后端）。
//
// 为什么要这一层：contract_test.go 只证明「Option 被追加、端口被绑上」，
// 证明不了「契约值落进了协议语义」。mcp 的可观测面恰好很硬：
// initialize 响应里的 serverInfo 就是 name/version 两行的落点，
// tools/call 的正文就是 after-build 注册工具的落点，
// endpoint 的 host:port 就是 address 行的落点。
//
// 覆盖的行：
//   - address → 契约地址上完成 initialize + tools/call 往返（形态强制 HTTP 的线上半）；
//   - name / version → initialize 的 ServerInfo 即契约文本；
//   - name / version 缺省 → 实现默认 "MCP Server"/"1.0.0" 不被空串顶掉；
//   - 业务 WithMiddleware → 中间件真的包在 streamable handler 外。
//
// 口径记录：契约 mcp 段与底层都没有 tls 字段（buildOpts 头注释），所以没有
// https 行；STDIO 覆盖腿在 contract_test（endpoint 恒空即落点判据）。

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	mcpgoclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpgomcp "github.com/mark3labs/mcp-go/mcp"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	mcp "github.com/kalandramo/bald/transport/mcp"
)

const testWait time.Duration = 5 * time.Second

const echoToolJSON = `{
  "name": "echo",
  "description": "Echoes the input string",
  "inputSchema": {
    "type": "object",
    "properties": {
      "message": {"type": "string", "description": "The message to echo"}
    },
    "required": ["message"]
  }
}`

// runMCP 走 Provider 构造并 Start。mcp 的 HTTP 形态 Start 同步绑端口后立即返回
// （绑不上端口即刻报错），故无需就绪轮询；清理走 Stop。
func runMCP(t *testing.T, opts []Option, cfg *bootstrapv1.Server) (*mcp.Server, string) {
	t.Helper()
	srv, cleanup, err := Provider(opts...)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil")
	}
	msrv, ok := srv.(*mcp.Server)
	if !ok {
		t.Fatalf("srv = %T, want *mcp.Server", srv)
	}
	if err := msrv.Start(context.Background()); err != nil {
		t.Fatalf("mcp Start: %v", err)
	}
	t.Cleanup(func() {
		if err := msrv.Stop(context.Background()); err != nil {
			t.Errorf("mcp Stop: %v", err)
		}
	})
	return msrv, msrv.Endpoint()
}

// initialize 对契约端点做真实 MCP initialize 握手（streamable HTTP，/mcp 路径）。
func initialize(t *testing.T, ep string) (*mcpgoclient.Client, *mcpgomcp.InitializeResult) {
	t.Helper()
	httpTransport, err := mcptransport.NewStreamableHTTP(ep + "/mcp")
	if err != nil {
		t.Fatalf("NewStreamableHTTP: %v", err)
	}
	c := mcpgoclient.NewClient(httpTransport)
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("client Start: %v", err)
	}
	res, err := c.Initialize(ctx, mcpgomcp.InitializeRequest{
		Params: mcpgomcp.InitializeParams{
			ProtocolVersion: mcpgomcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcpgomcp.Implementation{Name: "contract-test", Version: "1.0.0"},
		},
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return c, res
}

func callEcho(t *testing.T, c *mcpgoclient.Client, msg string) string {
	t.Helper()
	result, err := c.CallTool(context.Background(), mcpgomcp.CallToolRequest{
		Params: mcpgomcp.CallToolParams{Name: "echo", Arguments: map[string]any{"message": msg}},
	})
	if err != nil {
		t.Fatalf("tools/call echo: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("len(Content) = %d, want 1", len(result.Content))
	}
	text, ok := result.Content[0].(mcpgomcp.TextContent)
	if !ok {
		t.Fatalf("content = %T, want TextContent", result.Content[0])
	}
	return text.Text
}

func echoOption() Option {
	return WithAfterBuild(func(_ context.Context, s *mcp.Server) error {
		return s.RegisterHandlerWithJsonString(echoToolJSON,
			func(_ context.Context, request mcpgomcp.CallToolRequest) (*mcpgomcp.CallToolResult, error) {
				got, err := request.RequireString("message")
				if err != nil {
					return mcpgomcp.NewToolResultError(err.Error()), nil
				}
				return mcpgomcp.NewToolResultText(got), nil
			})
	})
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

func loopbackAddr(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("127.0.0.1:%d", freePort(t))
}

// TestMappingOffline_AddressAndEchoToolOnWire address 行 + after-build 工具行：
// 契约地址上完成 initialize 与 tools/call，回的就是注册时写下的处理逻辑。
func TestMappingOffline_AddressAndEchoToolOnWire(t *testing.T) {
	addr := loopbackAddr(t)
	_, ep := runMCP(t, []Option{echoOption()}, &bootstrapv1.Server{
		Mcp: &bootstrapv1.Server_Mcp{Address: addr},
	})
	if ep != "http://"+addr {
		t.Fatalf("Endpoint() = %q, want http://%s", ep, addr)
	}

	c, _ := initialize(t, ep)
	if got := callEcho(t, c, "pong:mcp"); got != "pong:mcp" {
		t.Fatalf("echo = %q, want pong:mcp", got)
	}
}

// TestMappingOffline_NameVersionOnWire name/version 行的协议落点：
// initialize 响应的 ServerInfo 就是契约文本（客户端展示的就是这两个字段）。
func TestMappingOffline_NameVersionOnWire(t *testing.T) {
	_, ep := runMCP(t, nil, &bootstrapv1.Server{
		Mcp: &bootstrapv1.Server_Mcp{
			Address: loopbackAddr(t),
			Name:    "bald-contract-mcp",
			Version: "v7.8.9",
		},
	})

	_, res := initialize(t, ep)
	if res.ServerInfo.Name != "bald-contract-mcp" || res.ServerInfo.Version != "v7.8.9" {
		t.Fatalf("ServerInfo = %q %q, want bald-contract-mcp v7.8.9", res.ServerInfo.Name, res.ServerInfo.Version)
	}
}

// TestMappingOffline_DefaultsSurviveEmptyFields name/version 留空不追加：
// ServerInfo 是实现默认，而不是被空串顶掉的空名字。
func TestMappingOffline_DefaultsSurviveEmptyFields(t *testing.T) {
	_, ep := runMCP(t, nil, &bootstrapv1.Server{
		Mcp: &bootstrapv1.Server_Mcp{Address: loopbackAddr(t)},
	})

	_, res := initialize(t, ep)
	if res.ServerInfo.Name != "MCP Server" || res.ServerInfo.Version != "1.0.0" {
		t.Fatalf("ServerInfo = %q %q, want defaults MCP Server / 1.0.0", res.ServerInfo.Name, res.ServerInfo.Version)
	}
}

// TestMappingOffline_MiddlewareFromBusinessOptionReachesWire 业务 WithMiddleware
// 经 WithServerOptions 注入后包在 streamable handler 外层：原始 HTTP 响应带头。
func TestMappingOffline_MiddlewareFromBusinessOptionReachesWire(t *testing.T) {
	addr := loopbackAddr(t)
	tagged := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Contract-Mw", "applied")
			next.ServeHTTP(w, r)
		})
	}
	_, ep := runMCP(t, []Option{
		WithServerOptions(mcp.WithMiddleware(tagged)),
	}, &bootstrapv1.Server{Mcp: &bootstrapv1.Server_Mcp{Address: addr}})

	// 不走 MCP 客户端（它会吞掉响应头）：裸 POST 也要穿过中间件。
	// 状态码不管（非法 JSON-RPC 会被 streamable 层拒绝），判别点是中间件头。
	req, err := http.NewRequest(http.MethodPost, ep+"/mcp", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	client := &http.Client{Timeout: testWait}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s/mcp: %v", ep, err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if got := resp.Header.Get("X-Contract-Mw"); got != "applied" {
		t.Fatalf("X-Contract-Mw = %q, want applied（业务中间件没进服务链？）", got)
	}
}
