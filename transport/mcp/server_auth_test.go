package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

// authMiddleware 是一个最小认证中间件：校验 Authorization: Bearer <want>，
// 失败返回 401 并中断（不调用 next）。用于验证中间件链的拦截能力。
func authMiddleware(want string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+want {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// newSSEServerWithMiddleware 构造一个带中间件的 SSE 服务端并启动，返回 endpoint。
func newSSEServerWithMiddleware(t *testing.T, mw ...Middleware) string {
	t.Helper()
	opts := []ServerOption{
		WithMCPServeType(ServerTypeSSE),
		WithMCPServeAddress("127.0.0.1:0"),
	}
	if len(mw) > 0 {
		opts = append(opts, WithMiddleware(mw...))
	}
	srv := NewServer(opts...)
	require.NoError(t, srv.RegisterHandler(
		mcp.NewTool("ping", mcp.WithDescription("ping")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("pong"), nil
		},
	))
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, srv.Stop(context.Background())) })
	return srv.Endpoint()
}

// TestServer_MiddlewareRejectsUnauthorized 验证无 token 时中间件返回 401。
func TestServer_MiddlewareRejectsUnauthorized(t *testing.T) {
	endpoint := newSSEServerWithMiddleware(t, authMiddleware("secret"))

	resp, err := http.Get(endpoint + "/sse")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestServer_MiddlewareAllowsAuthorized 验证带正确 token 时放行（非 401）。
func TestServer_MiddlewareAllowsAuthorized(t *testing.T) {
	endpoint := newSSEServerWithMiddleware(t, authMiddleware("secret"))

	req, err := http.NewRequest(http.MethodGet, endpoint+"/sse", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer secret")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// SSE 长连接会挂起；只断言未被认证拦截（非 401）。
	require.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestServer_MiddlewareOrder 验证多个中间件按注册顺序从外到内执行。
func TestServer_MiddlewareOrder(t *testing.T) {
	var order []string
	rec := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	endpoint := newSSEServerWithMiddleware(t, rec("first"), rec("second"))

	req, err := http.NewRequest(http.MethodGet, endpoint+"/sse", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	require.Equal(t, []string{"first", "second"}, order,
		"中间件应按注册顺序从外到内执行")
}

// TestServer_MiddlewareNotAppliedToStdio 验证 stdio 形态不受中间件影响
// （无 HTTP 层，不 panic、不报错）。
func TestServer_MiddlewareNotAppliedToStdio(t *testing.T) {
	var called bool
	srv := NewServer(
		WithMCPServeType(ServerTypeStdio),
		WithMiddleware(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				next.ServeHTTP(w, r)
			})
		}),
	)
	// stdio 的 Start 会阻塞在 ServeStdio；用可取消的 ctx 启动再立即停止。
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Start(ctx)
	}()
	cancel()
	<-done

	require.False(t, called, "stdio 形态不应触发 HTTP 中间件")
}

// TestServer_ProtectedResourceMetadata 验证配置后 well-known 端点返回
// 正确的 RFC 9728 元数据 JSON。
func TestServer_ProtectedResourceMetadata(t *testing.T) {
	prm := mcpserver.ProtectedResourceMetadataConfig{
		Resource:             "https://mcp.example.com",
		AuthorizationServers: []string{"https://auth.example.com"},
		ScopesSupported:      []string{"mcp:read", "mcp:write"},
	}
	srv := NewServer(
		WithMCPServeType(ServerTypeSSE),
		WithMCPServeAddress("127.0.0.1:0"),
		WithSSEProtectedResourceMetadata(prm),
	)
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, srv.Stop(context.Background())) })

	resp, err := http.Get(srv.Endpoint() + mcpserver.WellKnownProtectedResourcePath)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got mcpserver.ProtectedResourceMetadataConfig
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Equal(t, "https://mcp.example.com", got.Resource)
	require.Equal(t, []string{"https://auth.example.com"}, got.AuthorizationServers)
}
