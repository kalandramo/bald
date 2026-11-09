package mcp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

func TestServer_Name(t *testing.T) {
	require.Equal(t, KindMCP, NewServer().Name())
}

// TestServer_UnsupportedTypeDefaultsToStdio 覆盖 init 的 default 分支：
// 未知传输类型回退 STDIO（logWarnf 路径）。
func TestServer_UnsupportedTypeDefaultsToStdio(t *testing.T) {
	srv := NewServer(WithMCPServeType(ServerType("BOGUS")))
	require.Equal(t, ServerTypeStdio, srv.serverType)
}

// TestServer_OptionAggregation 覆盖 mcpOpts/sseOpts 追加选项。
func TestServer_OptionAggregation(t *testing.T) {
	srv := NewServer(
		WithServerName("n"),
		WithServerVersion("v"),
		WithMCPServerOptions(server.WithToolCapabilities(false)),
		WithSSEOptions(server.WithBaseURL("http://example.com")),
	)
	require.Equal(t, "n", srv.serverName)
	require.Equal(t, "v", srv.serverVersion)
	require.Len(t, srv.mcpOpts, 1)
	require.Len(t, srv.sseOpts, 1)
}

// TestServer_StartPresetError 覆盖 Start 在已有错误时直接返回。
func TestServer_StartPresetError(t *testing.T) {
	srv := NewServer(WithMCPServeType(ServerTypeInProcess))
	sentinel := errors.New("preset")
	srv.setErr(sentinel)

	require.ErrorIs(t, srv.Start(context.Background()), sentinel)
}

// TestServer_StartTwiceAndStopTwice 覆盖重复 Start（告警后返回 nil）与
// 重复 Stop（告警后返回 nil），并验证 Done 在 Stop 后关闭。
func TestServer_StartTwiceAndStopTwice(t *testing.T) {
	srv := NewServer(WithMCPServeType(ServerTypeInProcess))
	ctx := context.Background()

	require.NoError(t, srv.Start(ctx))
	require.NoError(t, srv.Start(ctx))

	require.NoError(t, srv.Stop(ctx))
	select {
	case <-srv.Done():
	default:
		t.Fatal("Done must be closed after Stop")
	}

	require.NoError(t, srv.Stop(ctx))
}

// TestServer_StdioServeError 覆盖 stdio 传输读取失败：ServeStdio 返回错误并
// 记入 s.err，随后再次 Start 命中预设错误分支。
func TestServer_StdioServeError(t *testing.T) {
	closed, err := os.Open(os.DevNull)
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	orig := os.Stdin
	os.Stdin = closed
	t.Cleanup(func() { os.Stdin = orig })

	srv := NewServer(WithMCPServeType(ServerTypeStdio))
	require.NoError(t, srv.Start(context.Background()))

	select {
	case <-srv.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("stdio server did not finish after stdin was closed")
	}

	require.Error(t, srv.Start(context.Background()))
}

// TestServer_HTTPServeError 覆盖 http.Server.Serve 非正常退出（listener 被外部
// 关闭）：错误记入 s.err，Stop 返回该错误并走 logError 分支。
func TestServer_HTTPServeError(t *testing.T) {
	srv := NewServer(
		WithMCPServeType(ServerTypeHTTP),
		WithMCPServeAddress("127.0.0.1:0"),
	)
	require.NoError(t, srv.Start(context.Background()))

	srv.mu.RLock()
	ln := srv.ln
	srv.mu.RUnlock()
	require.NoError(t, ln.Close())

	require.Eventually(t, func() bool {
		srv.mu.RLock()
		defer srv.mu.RUnlock()
		return srv.err != nil
	}, 2*time.Second, 10*time.Millisecond, "serve error must be recorded")

	require.Error(t, srv.Stop(context.Background()))
}

// TestServer_StopShutdownError 覆盖 Shutdown 返回错误（ctx 已取消且存在在途
// 请求），Stop 把错误返回给调用方。
func TestServer_StopShutdownError(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := NewServer(
		WithMCPServeType(ServerTypeHTTP),
		WithMCPServeAddress("127.0.0.1:0"),
		WithMiddleware(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
				next.ServeHTTP(w, r)
			})
		}),
	)
	require.NoError(t, srv.Start(context.Background()))

	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		req, err := http.NewRequest(http.MethodPost, srv.Endpoint()+"/mcp", strings.NewReader("{}"))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	defer func() { <-reqDone }()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached the middleware")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	defer close(release)

	err := srv.Stop(canceled)
	require.Error(t, err)
	require.Contains(t, err.Error(), "shutdown")
}

// TestServer_StopClosesListenOnly 覆盖无 http.Server、只有 listener 的停机路径。
func TestServer_StopClosesListenOnly(t *testing.T) {
	srv := NewServer(
		WithMCPServeType(ServerTypeHTTP),
		WithMCPServeAddress("127.0.0.1:0"),
	)
	require.NoError(t, srv.Start(context.Background()))

	srv.mu.Lock()
	srv.httpSrv = nil
	srv.mu.Unlock()

	require.NoError(t, srv.Stop(context.Background()))
}

func TestServer_RegisterNilServer(t *testing.T) {
	srv := &Server{}
	handler := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}

	require.Error(t, srv.RegisterHandler(mcp.NewTool("t"), handler))
	require.Error(t, srv.RegisterHandlerWithJsonString(echoToolJSON, handler))
	require.Error(t, srv.RegisterHandlerWithJsonSchema("t", "d", `{"type":"object","properties":{}}`, handler))
	require.Error(t, srv.RegisterResource(mcp.Resource{URI: "test://r", Name: "r"},
		func(context.Context, mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return nil, nil
		}))
}

// TestServer_RegisterInvalidToolJSON 覆盖 JSON 字符串工具注册的解析失败分支。
func TestServer_RegisterInvalidToolJSON(t *testing.T) {
	srv := NewServer()
	err := srv.RegisterHandlerWithJsonString(`{not-json`,
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return nil, nil
		})
	require.Error(t, err)
}

// TestServer_RegisterSchemaAndResource 覆盖原始 JSON Schema 工具注册与资源注册成功路径。
func TestServer_RegisterSchemaAndResource(t *testing.T) {
	srv := NewServer()

	require.NoError(t, srv.RegisterHandlerWithJsonSchema("schema-tool", "desc",
		`{"type":"object","properties":{"x":{"type":"string"}}}`,
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("ok"), nil
		}))

	resource := mcp.Resource{URI: "test://r", Name: "r"}
	require.NoError(t, srv.RegisterResource(resource,
		func(_ context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{
				mcp.TextResourceContents{URI: request.Params.URI, MIMEType: "text/plain", Text: "body"},
			}, nil
		}))
}

// TestEndpointFromListener 覆盖 host 解析失败回退 localhost，以及 listener
// 非 *net.TCPAddr 时从地址字符串取端口的回退。
func TestEndpointFromListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	got := endpointFromListener(ln, "bad::addr")
	require.Equal(t, "localhost", got.Hostname())
	require.NotEmpty(t, got.Port())

	wrapped := &nonTCPListener{Listener: ln}
	got = endpointFromListener(wrapped, "example.com:1234")
	require.Equal(t, "http://example.com:9999", got.String())
}

type nonTCPListener struct {
	net.Listener
}

func (nonTCPListener) Addr() net.Addr { return stringAddr("1.2.3.4:9999") }

type stringAddr string

func (stringAddr) Network() string  { return "tcp" }
func (a stringAddr) String() string { return string(a) }

// TestServer_SetErr 覆盖 setErr 的 nil 短路与多次错误 Join。
func TestServer_SetErr(t *testing.T) {
	srv := NewServer(WithMCPServeType(ServerTypeInProcess))
	srv.setErr(nil)

	first := errors.New("first")
	second := errors.New("second")
	srv.setErr(first)
	srv.setErr(second)

	err := srv.Start(context.Background())
	require.ErrorIs(t, err, first)
	require.ErrorIs(t, err, second)
}

func TestValidateToolInputSchema(t *testing.T) {
	cases := []struct {
		name   string
		schema mcp.ToolInputSchema
		ok     bool
	}{
		{"empty type", mcp.ToolInputSchema{Properties: map[string]any{}}, false},
		{"non-object type", mcp.ToolInputSchema{Type: "string", Properties: map[string]any{}}, false},
		{"nil properties", mcp.ToolInputSchema{Type: "object"}, false},
		{"valid", mcp.ToolInputSchema{Type: "object", Properties: map[string]any{}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateToolInputSchema(tc.schema)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
		})
	}
}

func TestLoadToolFromJsonFile(t *testing.T) {
	dir := t.TempDir()

	valid := filepath.Join(dir, "tool.json")
	require.NoError(t, os.WriteFile(valid, []byte(echoToolJSON), 0o600))
	tool, err := LoadToolFromJsonFile(valid)
	require.NoError(t, err)
	require.Equal(t, "echo", tool.Name)

	_, err = LoadToolFromJsonFile(filepath.Join(dir, "missing.json"))
	require.Error(t, err)

	badJSON := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(badJSON, []byte(`{bad`), 0o600))
	_, err = LoadToolFromJsonFile(badJSON)
	require.Error(t, err)

	badSchema := filepath.Join(dir, "schema.json")
	require.NoError(t, os.WriteFile(badSchema,
		[]byte(`{"name":"x","inputSchema":{"type":"string","properties":{}}}`), 0o600))
	_, err = LoadToolFromJsonFile(badSchema)
	require.Error(t, err)
}

func TestLoadToolFromJsonString(t *testing.T) {
	_, err := LoadToolFromJsonString(`{bad`)
	require.Error(t, err)

	_, err = LoadToolFromJsonString(`{"name":"x","inputSchema":{"type":"object"}}`)
	require.Error(t, err)

	tool, err := LoadToolFromJsonString(echoToolJSON)
	require.NoError(t, err)
	require.Equal(t, "echo", tool.Name)
}
