package cobramcp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

// okHandler 是认证通过后的下游 handler，返回 200。
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TestStaticTokenMiddleware_RejectsMissingToken 验证无 token → 401。
func TestStaticTokenMiddleware_RejectsMissingToken(t *testing.T) {
	h := staticTokenMiddleware("secret")(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestStaticTokenMiddleware_RejectsWrongToken 验证错误 token → 401。
func TestStaticTokenMiddleware_RejectsWrongToken(t *testing.T) {
	h := staticTokenMiddleware("secret")(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestStaticTokenMiddleware_AllowsCorrectToken 验证正确 token → 200。
func TestStaticTokenMiddleware_AllowsCorrectToken(t *testing.T) {
	h := staticTokenMiddleware("secret")(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

// TestStaticTokenMiddleware_PublicPathBypassesAuth 验证免认证路径无 token 也能访问
// （OAuth well-known 发现端点必须公开）。
func TestStaticTokenMiddleware_PublicPathBypassesAuth(t *testing.T) {
	const wk = "/.well-known/oauth-protected-resource"
	h := staticTokenMiddleware("secret", wk)(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, wk, nil))

	require.Equal(t, http.StatusOK, rec.Code)
}

// TestBuildAuthMiddleware_NilWhenUnconfigured 验证两者都未配置时返回 nil
// （不启用认证，保持既有行为）。
func TestBuildAuthMiddleware_NilWhenUnconfigured(t *testing.T) {
	require.Nil(t, buildAuthMiddleware(nil, ""))
}

// TestBuildAuthMiddleware_UserMiddlewareOuter 验证 AuthMiddleware 在外层先执行。
func TestBuildAuthMiddleware_UserMiddlewareOuter(t *testing.T) {
	var order []string
	userMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "user")
			next.ServeHTTP(w, r)
		})
	}
	h := buildAuthMiddleware(userMW, "secret")(okHandler())
	require.NotNil(t, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)

	require.Equal(t, []string{"user"}, order, "用户中间件应在外层先执行")
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestPublicAuthPaths_IncludesWellKnown 验证配置元数据后，免认证路径含 well-known。
func TestPublicAuthPaths_IncludesWellKnown(t *testing.T) {
	c := &Config{
		OAuthProtectedResource: &mcpserver.ProtectedResourceMetadataConfig{
			Resource: "https://mcp.example.com",
		},
	}
	paths := c.publicAuthPaths()
	require.Equal(t, []string{mcpserver.WellKnownProtectedResourcePath}, paths)
}

// TestPublicAuthPaths_EmptyWhenNoMetadata 验证未配置元数据时无免认证路径。
func TestPublicAuthPaths_EmptyWhenNoMetadata(t *testing.T) {
	c := &Config{}
	require.Empty(t, c.publicAuthPaths())
}

// TestRESTAuthMiddleware_Integration 集成验证：buildAuthMiddleware 包裹的 handler
// 在 REST 场景下正确拦截/放行，且 well-known 端点公开。
func TestRESTAuthMiddleware_Integration(t *testing.T) {
	const wk = mcpserver.WellKnownProtectedResourcePath
	c := &Config{
		AuthToken: "secret",
		OAuthProtectedResource: &mcpserver.ProtectedResourceMetadataConfig{
			Resource: "https://mcp.example.com",
		},
	}

	// 构造一个模拟 REST mux：一个工具路由 + well-known 端点。
	mux := http.NewServeMux()
	mux.HandleFunc("/tool", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle(wk, mcpserver.NewProtectedResourceMetadataHandler(*c.OAuthProtectedResource))

	var handler http.Handler = mux
	if mw := buildAuthMiddleware(c.AuthMiddleware, c.AuthToken, c.publicAuthPaths()...); mw != nil {
		handler = mw(handler)
	}

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// 1) 无 token 访问工具 → 401
	resp, err := http.Post(srv.URL+"/tool", "application/json", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// 2) 带 token 访问工具 → 200
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/tool", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// 3) 无 token 访问 well-known → 200（公开发现端点）
	resp, err = http.Get(srv.URL + wk)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
