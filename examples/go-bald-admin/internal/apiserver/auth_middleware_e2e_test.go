package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gingonic "github.com/gin-gonic/gin"

	authbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auth"
	secretbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/secret"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// setup 初始化桥接并构造带完整路由的 gin engine。
func setup(t *testing.T) *gingonic.Engine {
	t.Helper()
	gingonic.SetMode(gingonic.TestMode)
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	e := gingonic.New()
	RegisterRoutes(e, authbiz.New(bootstrappkg.Signer), secretbiz.New(nil))
	return e
}

func login(t *testing.T, e *gingonic.Engine, username, password string) string {
	t.Helper()
	body, _ := json.Marshal(authbiz.Credential{Username: username, Password: password})
	req := httptest.NewRequest(http.MethodPost, "/v1/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login %s: got %d, body=%s", username, w.Code, w.Body.String())
	}
	var pair authbiz.TokenPair
	if err := json.Unmarshal(w.Body.Bytes(), &pair); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	return pair.AccessToken
}

func do(e *gingonic.Engine, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

// TestAuthnRequired_NoToken 未携带 token 访问受保护资源应 401。
func TestAuthnRequired_NoToken(t *testing.T) {
	e := setup(t)
	w := do(e, http.MethodGet, "/v1/secret/s-db-pwd", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d (%s)", w.Code, w.Body.String())
	}
}

// TestAuthz_Admin_FullAccess admin 可读可删。
func TestAuthz_Admin_FullAccess(t *testing.T) {
	e := setup(t)
	tok := login(t, e, "admin", "admin123")

	if w := do(e, http.MethodGet, "/v1/secret/s-db-pwd", tok); w.Code != http.StatusOK {
		t.Fatalf("admin GET secret: want 200 got %d (%s)", w.Code, w.Body.String())
	}
	if w := do(e, http.MethodDelete, "/v1/secret/s-db-pwd", tok); w.Code != http.StatusOK {
		t.Fatalf("admin DELETE secret: want 200 got %d (%s)", w.Code, w.Body.String())
	}
}

// TestAuthz_Viewer_ForbiddenDelete viewer 可读但删除应 403。
// 注意：admin 用例会删除 s-db-pwd，为避免用例间 seed 数据污染，viewer 只读未被删除的 s-api-key。
func TestAuthz_Viewer_ForbiddenDelete(t *testing.T) {
	e := setup(t)
	tok := login(t, e, "alice", "alice123")

	if w := do(e, http.MethodGet, "/v1/secret/s-api-key", tok); w.Code != http.StatusOK {
		t.Fatalf("viewer GET secret: want 200 got %d (%s)", w.Code, w.Body.String())
	}
	if w := do(e, http.MethodDelete, "/v1/secret/s-api-key", tok); w.Code != http.StatusForbidden {
		t.Fatalf("viewer DELETE secret: want 403 got %d (%s)", w.Code, w.Body.String())
	}
}

// TestDeleteSecret_RealDelete admin 删除后该 secret 真正从 store 消失（M6 CR 修复：
// DELETE 不再返回占位 {"deleted":id}，而是经 biz 落 store，并清空缓存条目）。
// 为避免与共享 seed 用例相互污染（顺序无关），本用例自建一条专属 secret（t-default 租户）后删除。
func TestDeleteSecret_RealDelete(t *testing.T) {
	e := setup(t)
	tok := login(t, e, "admin", "admin123")

	// 自建专属资源（admin 属 t-default，落同租户，受隔离约束可命中）。
	own := &authmodel.Secret{ID: "s-del-temp", Name: "待删资源", Content: "x", TenantID: "t-default"}
	if err := bootstrappkg.SecretStore.Create(context.Background(), own); err != nil {
		t.Fatalf("seed temp secret: %v", err)
	}

	if w := do(e, http.MethodDelete, "/v1/secret/s-del-temp", tok); w.Code != http.StatusOK {
		t.Fatalf("admin DELETE secret: want 200 got %d (%s)", w.Code, w.Body.String())
	}
	// 删除后再次 GET 应 404（资源已真实消失，而非占位响应）。
	if w := do(e, http.MethodGet, "/v1/secret/s-del-temp", tok); w.Code != http.StatusNotFound {
		t.Fatalf("admin GET after delete: want 404 got %d (%s)", w.Code, w.Body.String())
	}
	// 删除后再次 DELETE 同 ID 应 404（幂等 NotFound，不报错）。
	if w := do(e, http.MethodDelete, "/v1/secret/s-del-temp", tok); w.Code != http.StatusNotFound {
		t.Fatalf("admin DELETE again: want 404 got %d (%s)", w.Code, w.Body.String())
	}
}

// TestBadCredential 错误密码应 401。
func TestBadCredential(t *testing.T) {
	e := setup(t)
	body, _ := json.Marshal(authbiz.Credential{Username: "admin", Password: "wrong"})
	req := httptest.NewRequest(http.MethodPost, "/v1/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// TestWhoAmI 登录后可解析当前用户。
func TestWhoAmI(t *testing.T) {
	e := setup(t)
	tok := login(t, e, "admin", "admin123")
	w := do(e, http.MethodGet, "/v1/auth/whoami", tok)
	if w.Code != http.StatusOK {
		t.Fatalf("whoami: want 200 got %d (%s)", w.Code, w.Body.String())
	}
	var info authbiz.UserInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}
	if info.UserID != "u-admin" {
		t.Fatalf("want u-admin, got %s", info.UserID)
	}
}
