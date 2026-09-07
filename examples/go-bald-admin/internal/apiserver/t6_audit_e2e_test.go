package apiserver

// t6_audit_e2e_test.go 审计增强+查询 REST e2e（T6 验收）：真实 gin 引擎 +
// httptest + 真实 SQLite 内存库（StoreAuditor 落库 → AuditStore 查询，§0 禁 fake）。
//
// 验收点（移植计划 T6）：
//  1. 登录写路径触发 category=login 审计（成功 allow / 失败 deny + 原因，
//     IP/UA 独立列落库）
//  2. 业务写路径触发 category=operation 审计（gin AuditMiddleware，IP/UA）
//  3. 查询接口：多条件过滤 + 分页（page_size/page_token）+ 非法 token 400
//  4. 授权：audit 查询 admin 专属，viewer 403
//
// 数据隔离：各用例用唯一 username/过滤条件，规避全局 SQLite 与全局 auditor
// 的跨用例累计（SetAuditor 为进程级全局，与生产 audit.backends 热切换同源）。

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	gingonic "github.com/gin-gonic/gin"

	auditv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/audit/v1"
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
	securityaudit "github.com/kalandramo/bald/examples/go-bald-admin/internal/security/audit"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authz"
	ginmw "github.com/kalandramo/bald/pkg/middleware/gin"
)

// startAuditREST 起真实 gin 引擎 + 真实 SQLite：审计后端挂 StoreAuditor
// （落 AuditRecord 表，与 main.go audit.backends=store 同构），gin 审计中间件
// 挂全局（与 main.go 对称）。返回 base URL。
func startAuditREST(t *testing.T) string {
	t.Helper()
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	// T6：审计后端 = StoreAuditor（真实落库），登录/业务审计共用该入口。
	audit.SetAuditor(securityaudit.NewStore(bootstrappkg.DB))
	t.Cleanup(func() { audit.SetAuditor(audit.NopAuditor()) })

	e := gingonic.New()
	// 与 main.go 对称：审计中间件在路由注册前全局挂载（wrap 型旁路）。
	e.Use(ginmw.AuditMiddleware(
		ginmw.AuditWithObjectResolver(authz.DefaultHTTPObject),
		ginmw.AuditWithActionResolver(authz.DefaultHTTPAction),
	))
	RegisterRoutes(e, authbiz.New(bootstrappkg.Signer), secretbiz.New(nil), tenantbiz.New(),
		userbiz.New(), menubiz.New(), permissionbiz.New(), dictbiz.New(nil), filebiz.New(nil, ""), auditlogbiz.New())
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv.URL
}

// postJSON 发 JSON 请求（带可选 UA 与 token），返回状态码与原始响应体。
func postJSON(t *testing.T, base, method, path, ua, tok, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// listAudit 调审计查询接口并解析（调用方已持 admin token）。
func listAudit(t *testing.T, base, tok, query string) *auditv1.ListAuditRecordsResponse {
	t.Helper()
	code, raw := postJSON(t, base, http.MethodGet, "/v1/audit"+query, "t6-e2e-agent", tok, "")
	if code != http.StatusOK {
		t.Fatalf("list audit status=%d body=%s", code, raw)
	}
	out := new(auditv1.ListAuditRecordsResponse)
	decodePB(raw, out) // protojson（Timestamp/字段名映射，标准 json 不兼容）
	return out
}

// TestAuditREST_LoginAuditFields 登录成功/失败均落 category=login 审计，
// IP/UA 独立列（T6 验收 1）。
func TestAuditREST_LoginAuditFields(t *testing.T) {
	base := startAuditREST(t)
	tok := tenantToken(t, "admin", "u-admin", "admin", "t-default")

	// 1. 失败登录（唯一用户名隔离数据；deny + 原因）。
	if code, _ := postJSON(t, base, http.MethodPost, "/v1/login", "t6-fail-agent/1.0", "",
		`{"username":"t6-deny-user","password":"wrong"}`); code != http.StatusUnauthorized {
		t.Fatalf("bad login status=%d", code)
	}
	// 2. 成功登录（seed 管理员）。
	if code, _ := postJSON(t, base, http.MethodPost, "/v1/login", "t6-ok-agent/1.0", "",
		`{"username":"admin","password":"admin123"}`); code != http.StatusOK {
		t.Fatalf("good login failed")
	}

	// 3. 查询 deny 条目：IP/UA/原因落独立列。
	list := listAudit(t, base, tok, "?category=login&subject=t6-deny-user")
	if list.GetTotal() != 1 {
		t.Fatalf("deny login total=%d, want 1", list.GetTotal())
	}
	deny := list.GetItems()[0]
	if deny.GetResult() != "deny" || deny.GetAction() != "login" || deny.GetObject() != "auth" {
		t.Fatalf("deny record: %+v", deny)
	}
	if deny.GetError() != "invalid credentials" {
		t.Fatalf("deny error=%q", deny.GetError())
	}
	if deny.GetIpAddress() == "" || deny.GetUserAgent() != "t6-fail-agent/1.0" {
		t.Fatalf("deny ip/ua: %q / %q", deny.GetIpAddress(), deny.GetUserAgent())
	}

	// 4. 查询 allow 条目（subject=admin 累计可能多条，断言存在 allow 且字段完整）。
	list = listAudit(t, base, tok, "?category=login&subject=admin&result=allow")
	if list.GetTotal() < 1 {
		t.Fatalf("allow login total=%d", list.GetTotal())
	}
	allow := list.GetItems()[0]
	if allow.GetUserAgent() != "t6-ok-agent/1.0" || allow.GetError() != "" {
		t.Fatalf("allow record: %+v", allow)
	}
}

// TestAuditREST_OperationAudit 业务写路径触发 category=operation 审计
// （gin AuditMiddleware，object/action P9 归一化，T6 验收 2）。
func TestAuditREST_OperationAudit(t *testing.T) {
	base := startAuditREST(t)
	tok := tenantToken(t, "admin", "u-admin", "admin", "t-default")

	// 业务写：创建租户（已知 body 形态，T2 范式）。
	body := `{"id":"t-audit-op","name":"audit-op","remark":"t6"}`
	if code, raw := postJSON(t, base, http.MethodPost, "/v1/tenant", "t6-op-agent/1.0", tok, body); code != http.StatusCreated {
		t.Fatalf("create tenant status=%d body=%s", code, raw)
	}

	list := listAudit(t, base, tok, "?category=operation&object=tenant&action=post")
	if list.GetTotal() < 1 {
		t.Fatalf("operation audit total=%d", list.GetTotal())
	}
	rec := list.GetItems()[0]
	if rec.GetResult() != "allow" || rec.GetSubject() != "u-admin" {
		t.Fatalf("operation record: %+v", rec)
	}
	if rec.GetUserAgent() != "t6-op-agent/1.0" || rec.GetIpAddress() == "" {
		t.Fatalf("operation ip/ua: %q / %q", rec.GetIpAddress(), rec.GetUserAgent())
	}
}

// TestAuditREST_PaginationAndInvalidToken 分页翻页 + 非法 page_token 400
// （T6 验收 3；唯一 subject 隔离数据保证精确 total）。
func TestAuditREST_PaginationAndInvalidToken(t *testing.T) {
	base := startAuditREST(t)
	tok := tenantToken(t, "admin", "u-admin", "admin", "t-default")

	// 造 5 条 deny 登录审计（唯一用户名 t6-page-user）。
	for i := 0; i < 5; i++ {
		if code, _ := postJSON(t, base, http.MethodPost, "/v1/login", "t6-page-agent/1.0", "",
			`{"username":"t6-page-user","password":"wrong"}`); code != http.StatusUnauthorized {
			t.Fatalf("login #%d status=%d", i, code)
		}
	}

	// 页 1：page_size=2，total=5，next_page_token 非空。
	list := listAudit(t, base, tok, "?category=login&subject=t6-page-user&page_size=2")
	if list.GetTotal() != 5 || list.GetNextPageToken() == "" || len(list.GetItems()) != 2 {
		t.Fatalf("page1: total=%d next=%q items=%d", list.GetTotal(), list.GetNextPageToken(), len(list.GetItems()))
	}
	// 页 2：带 token 翻页。
	list = listAudit(t, base, tok, "?category=login&subject=t6-page-user&page_size=2&page_token="+list.GetNextPageToken())
	if len(list.GetItems()) != 2 || list.GetNextPageToken() == "" {
		t.Fatalf("page2: items=%d next=%q", len(list.GetItems()), list.GetNextPageToken())
	}
	// 页 3（末页）：只剩 1 条，next_page_token 为空。
	list = listAudit(t, base, tok, "?category=login&subject=t6-page-user&page_size=2&page_token="+list.GetNextPageToken())
	if len(list.GetItems()) != 1 || list.GetNextPageToken() != "" {
		t.Fatalf("page3: items=%d next=%q", len(list.GetItems()), list.GetNextPageToken())
	}

	// 非法 page_token → 400。
	if code, _ := postJSON(t, base, http.MethodGet, "/v1/audit?page_token=not-a-number", "", tok, ""); code != http.StatusBadRequest {
		t.Fatalf("invalid page_token status=%d", code)
	}
}

// TestAuditREST_GetAuditRecord 单条详情：列表取 ID → Get 断言一致 + 404 分支。
func TestAuditREST_GetAuditRecord(t *testing.T) {
	base := startAuditREST(t)
	tok := tenantToken(t, "admin", "u-admin", "admin", "t-default")

	if code, _ := postJSON(t, base, http.MethodPost, "/v1/login", "t6-get-agent/1.0", "",
		`{"username":"t6-get-user","password":"wrong"}`); code != http.StatusUnauthorized {
		t.Fatalf("bad login status=%d", code)
	}
	list := listAudit(t, base, tok, "?category=login&subject=t6-get-user")
	if list.GetTotal() != 1 {
		t.Fatalf("total=%d", list.GetTotal())
	}
	id := list.GetItems()[0].GetId()
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		t.Fatalf("id %q not numeric", id)
	}

	code, raw := postJSON(t, base, http.MethodGet, "/v1/audit/"+id, "", tok, "")
	if code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", code, raw)
	}
	out := new(auditv1.GetAuditRecordResponse)
	decodePB(raw, out)
	if out.GetRecord().GetId() != id || out.GetRecord().GetUserAgent() != "t6-get-agent/1.0" {
		t.Fatalf("get record: %+v", out.GetRecord())
	}
	// 不存在 → 404。
	if code, _ = postJSON(t, base, http.MethodGet, "/v1/audit/999999", "", tok, ""); code != http.StatusNotFound {
		t.Fatalf("missing record status=%d", code)
	}
}

// TestAuditREST_ViewerForbidden audit 查询 admin 专属：viewer 403（T6 验收 4）。
func TestAuditREST_ViewerForbidden(t *testing.T) {
	base := startAuditREST(t)
	viewer := tenantToken(t, "alice", "u-alice", "viewer", "t-default")
	if code, _ := postJSON(t, base, http.MethodGet, "/v1/audit", "", viewer, ""); code != http.StatusForbidden {
		t.Fatalf("viewer list audit want 403, got %d", code)
	}
}
