package e2e

// tenant_e2e_test.go 租户管理 REST e2e（T2 验收）：真实 gin 引擎 + httptest，
// 走完整中间件链（分组 Authn/Authz）与 biz/store/DB。
//
// 验证点：
//  1. admin 全 CRUD + 种子租户可见（platform/t-default/t-other）
//  2. viewer（无 tenant 权限点）→ 403（casbin P9 归一化策略）
//  3. platform 平台租户受保护（删除拒绝）

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gingonic "github.com/gin-gonic/gin"

	tenantv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/tenant/v1"
	"github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver"
	auditlogbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auditlog"
	authbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auth"
	dictbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/dict"
	filebiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/file"
	menubiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/menu"
	permissionbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/permission"
	secretbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/secret"
	tenantbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/tenant"
	userbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/user"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
	"github.com/kalandramo/bald/pkg/authn"
)

// startTenantREST 起真实 gin 引擎（httptest），返回可调用的 base URL。
func startTenantREST(t *testing.T) string {
	t.Helper()
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	e := gingonic.New()
	apiserver.RegisterRoutes(e, &apiserver.BizSet{
		Auth: authbiz.New(bootstrappkg.Signer), Secret: secretbiz.New(nil), Tenant: tenantbiz.New(),
		User: userbiz.New(), Menu: menubiz.New(), Permission: permissionbiz.New(),
		Dict: dictbiz.New(nil), File: filebiz.New(nil, ""), AuditLog: auditlogbiz.New(),
	})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv.URL
}

func tenantToken(t *testing.T, username, userID, role, tenantID string) string {
	t.Helper()
	claims := authn.AuthClaims{Issuer: "go-bald-admin", Subject: userID, TenantID: tenantID, Roles: []string{role}, Name: username}
	tok, err := bootstrappkg.Signer.IssueToken(claims, 2*time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return tok
}

// callTenant 执行租户 REST 调用，返回状态码与（成功时）解析的响应。
func callTenant(t *testing.T, base, tok, method, path string, body any) (int, *tenantv1.ListTenantsResponse) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("rest call: %v", err)
	}
	defer resp.Body.Close()
	out := new(tenantv1.ListTenantsResponse) // 复用 items/total 结构做列表断言
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, out
}

// TestTenantREST_AdminLifecycle 平台管理员租户全生命周期。
func TestTenantREST_AdminLifecycle(t *testing.T) {
	base := startTenantREST(t)
	tok := tenantToken(t, "admin", "u-admin", "admin", "t-default")

	// 1. 列表：三个种子租户可见（平台全量语义）。
	code, list := callTenant(t, base, tok, http.MethodGet, "/v1/tenant", nil)
	if code != http.StatusOK {
		t.Fatalf("list status=%d", code)
	}
	ids := map[string]bool{}
	for _, it := range list.GetItems() {
		ids[it.GetId()] = true
	}
	if !ids["platform"] || !ids["t-default"] || !ids["t-other"] {
		t.Fatalf("seed tenants missing, got %v", ids)
	}

	// 2. 创建 → 查询 → 更新（冻结）。
	code, _ = callTenant(t, base, tok, http.MethodPost, "/v1/tenant",
		map[string]any{"id": "t-acme", "name": "ACME Corp", "remark": "T2 e2e"})
	if code != http.StatusCreated {
		t.Fatalf("create status=%d", code)
	}
	code, _ = callTenant(t, base, tok, http.MethodGet, "/v1/tenant/t-acme", nil)
	if code != http.StatusOK {
		t.Fatalf("get status=%d", code)
	}
	code, _ = callTenant(t, base, tok, http.MethodPut, "/v1/tenant/t-acme",
		map[string]any{"id": "t-acme", "status": "FREEZE"})
	if code != http.StatusOK {
		t.Fatalf("update status=%d", code)
	}

	// 3. 删除（platform 受保护）。
	if code, _ := callTenant(t, base, tok, http.MethodDelete, "/v1/tenant/platform", nil); code == http.StatusOK {
		t.Fatalf("platform tenant must be protected")
	}
	if code, _ := callTenant(t, base, tok, http.MethodDelete, "/v1/tenant/t-acme", nil); code != http.StatusOK {
		t.Fatalf("delete status=%d", code)
	}
}

// TestTenantREST_ViewerForbidden viewer 无 tenant 权限点 → 403。
func TestTenantREST_ViewerForbidden(t *testing.T) {
	base := startTenantREST(t)
	tok := tenantToken(t, "alice", "u-alice", "viewer", "t-default")
	code, _ := callTenant(t, base, tok, http.MethodGet, "/v1/tenant", nil)
	if code != http.StatusForbidden {
		t.Fatalf("viewer list tenants want 403, got %d", code)
	}
}
