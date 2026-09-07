package apiserver

// t3_e2e_test.go 菜单/权限管理 REST e2e（T3 验收）：真实 gin 引擎 + httptest，
// 完整中间件链（分组 Authn/Authz）+ biz/store/DB + 数据化 casbin 策略。
//
// 验收点（移植计划 T3）：
//  1. 菜单树 ListMenus（种子树结构 / Order 排序 / BUTTON 节点）+ CRUD + 级联删除子树
//  2. 权限点注册表 CRUD（权限码唯一，重复创建 400）
//  3. 角色策略（casbin p 行数据化）：List 全量、Create 主键防重、Delete
//  4. 策略数据化的授权行为：viewer 读 menu/permission 200、写 403（p 行仅 admin 写）
//  5. 无策略默认拒绝（fail-closed）由 security/casbin 包单测覆盖（空 csv 构造）

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	menuv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/menu/v1"
	permissionv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/permission/v1"
)

// callMenu / callPermission 通用 REST 调用：返回状态码（响应体由调用方按需解析）。
// 解码必须用 protojson：handler 的 writePB 按 proto3 JSON 规范输出（lowerCamel
// 字段名），encoding/json 的结构体 tag（snake_case）对不上多词字段（如 menuIds）。
func callMenu(t *testing.T, base, tok, method, path string, body any) (int, *menuv1.ListMenusResponse) {
	t.Helper()
	code, raw := callRaw(t, base, tok, method, path, body)
	out := new(menuv1.ListMenusResponse) // 树根数组 + total
	decodePB(raw, out)
	return code, out
}

func callPermission(t *testing.T, base, tok, method, path string, body any) (int, *permissionv1.ListPermissionsResponse) {
	t.Helper()
	code, raw := callRaw(t, base, tok, method, path, body)
	out := new(permissionv1.ListPermissionsResponse)
	decodePB(raw, out)
	return code, out
}

func callPermissionPolicy(t *testing.T, base, tok, method, path string, body any) (int, *permissionv1.ListRolePoliciesResponse) {
	t.Helper()
	code, raw := callRaw(t, base, tok, method, path, body)
	out := new(permissionv1.ListRolePoliciesResponse)
	decodePB(raw, out)
	return code, out
}

func decodePB(raw []byte, msg proto.Message) {
	if len(raw) == 0 {
		return
	}
	_ = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(raw, msg)
}

// callRaw 通用 REST 调用：返回状态码与原始响应体（protojson 与 encoding/json
// 双兼容——生成消息的 JSON tag 由 protojson 规范输出，encoding/json 可解码标量字段）。
func callRaw(t *testing.T, base, tok, method, path string, body any) (int, []byte) {
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
		t.Fatalf("rest call %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Logf("HTTP %s %s -> %d: %s", method, path, resp.StatusCode, raw)
	}
	return resp.StatusCode, raw
}

// TestMenuREST_TreeAndLifecycle 菜单树 + 生命周期（admin）。
func TestMenuREST_TreeAndLifecycle(t *testing.T) {
	base := startTenantREST(t)
	tok := tenantToken(t, "admin", "u-admin", "admin", "t-default")

	// 1. 树：2 个种子根（menu-dashboard / menu-system），order 排序（dashboard -1 在前）。
	code, tree := callMenu(t, base, tok, http.MethodGet, "/v1/menu", nil)
	if code != http.StatusOK {
		t.Fatalf("list tree status=%d", code)
	}
	if len(tree.GetItems()) != 2 {
		t.Fatalf("expect 2 roots, got %d", len(tree.GetItems()))
	}
	if tree.GetItems()[0].GetId() != "menu-dashboard" || tree.GetItems()[1].GetId() != "menu-system" {
		t.Fatalf("roots not sorted by order: %v, %v", tree.GetItems()[0].GetId(), tree.GetItems()[1].GetId())
	}
	var sys *menuv1.Menu
	for _, r := range tree.GetItems() {
		if r.GetId() == "menu-system" {
			sys = r
		}
	}
	if sys == nil || len(sys.GetChildren()) != 6 {
		t.Fatalf("menu-system should have 6 children, got %+v", sys)
	}
	// children 按 order 排序：tenant/user/menu/permission/secret/dict。
	wantOrder := []string{"menu-tenant", "menu-user", "menu-menu", "menu-permission", "menu-secret", "menu-dict"}
	for i, id := range wantOrder {
		if sys.GetChildren()[i].GetId() != id {
			t.Fatalf("child[%d]=%s, want %s", i, sys.GetChildren()[i].GetId(), id)
		}
	}

	// 2. 创建：catalog + 两级子节点，随后级联删除验证。
	code, _ = callMenu(t, base, tok, http.MethodPost, "/v1/menu",
		map[string]any{"id": "menu-log", "type": "TYPE_CATALOG", "name": "Log", "path": "/log", "title": "日志审计", "order": 3000})
	if code != http.StatusCreated {
		t.Fatalf("create root status=%d", code)
	}
	code, _ = callMenu(t, base, tok, http.MethodPost, "/v1/menu",
		map[string]any{"id": "menu-log-op", "parent_id": "menu-log", "type": "TYPE_MENU", "name": "OperationLog", "path": "/log/operation", "title": "操作日志", "order": 1})
	if code != http.StatusCreated {
		t.Fatalf("create child status=%d", code)
	}
	// 父不存在 → 400。
	if code, _ := callMenu(t, base, tok, http.MethodPost, "/v1/menu",
		map[string]any{"id": "menu-orphan", "parent_id": "menu-nope", "type": "TYPE_MENU"}); code != http.StatusBadRequest {
		t.Fatalf("create with missing parent must 400, got %d", code)
	}

	// 3. 更新：改标题 + 显式改 order 为 0（order_set 位语义）。
	code, _ = callMenu(t, base, tok, http.MethodPut, "/v1/menu/menu-log",
		map[string]any{"title": "日志审计(改)", "order": 0, "order_set": true})
	if code != http.StatusOK {
		t.Fatalf("update status=%d", code)
	}

	// 4. 级联删除：删 catalog 连子节点一起消失。
	code, _ = callMenu(t, base, tok, http.MethodDelete, "/v1/menu/menu-log", nil)
	if code != http.StatusOK {
		t.Fatalf("delete status=%d", code)
	}
	if code, _ := callMenu(t, base, tok, http.MethodGet, "/v1/menu/menu-log-op", nil); code != http.StatusNotFound {
		t.Fatalf("child must cascade-deleted, get status=%d", code)
	}
}

// TestPermissionREST_RegistryAndPolicies 权限点注册表 + 角色策略数据化管理（admin）。
func TestPermissionREST_RegistryAndPolicies(t *testing.T) {
	base := startTenantREST(t)
	tok := tenantToken(t, "admin", "u-admin", "admin", "t-default")

	// 1. 权限点列表：7 条种子（auth:get、secret:list/delete、tenant/user/menu/permission:list）。
	code, list := callPermission(t, base, tok, http.MethodGet, "/v1/permission", nil)
	if code != http.StatusOK {
		t.Fatalf("list permissions status=%d", code)
	}
	if list.GetTotal() < 7 {
		t.Fatalf("expect >=7 seed permissions, got %d", list.GetTotal())
	}
	// secret:list 挂 menu-secret 可见性（关联内联 CSV 的解析验证）。
	var found bool
	for _, p := range list.GetItems() {
		if p.GetId() == "secret:list" {
			found = true
			if len(p.GetMenuIds()) != 1 || p.GetMenuIds()[0] != "menu-secret" {
				t.Fatalf("secret:list menu_ids = %v", p.GetMenuIds())
			}
		}
	}
	if !found {
		t.Fatalf("seed permission secret:list missing")
	}

	// 2. 创建 → 重复创建冲突 400 → 删除。
	code, _ = callPermission(t, base, tok, http.MethodPost, "/v1/permission",
		map[string]any{"id": "dict:list", "name": "列出字典", "menu_ids": []string{"menu-system"}})
	if code != http.StatusCreated {
		t.Fatalf("create permission status=%d", code)
	}
	if code, _ = callPermission(t, base, tok, http.MethodPost, "/v1/permission",
		map[string]any{"id": "dict:list", "name": "dup"}); code != http.StatusBadRequest {
		t.Fatalf("duplicate permission must 400, got %d", code)
	}
	if code, _ = callPermission(t, base, tok, http.MethodDelete, "/v1/permission/dict:list", nil); code != http.StatusOK {
		t.Fatalf("delete permission status=%d", code)
	}

	// 3. 角色策略：种子行全量可见（含 role:object:action 业务键）。
	code, pol := callPermissionPolicy(t, base, tok, http.MethodGet, "/v1/permission/policy", nil)
	if code != http.StatusOK {
		t.Fatalf("list policies status=%d", code)
	}
	if pol.GetTotal() < 36 {
		t.Fatalf("expect >=36 seeded policy rows, got %d", pol.GetTotal())
	}
	var adminTenantGet bool
	for _, p := range pol.GetItems() {
		if p.GetId() == "admin:tenant:get" && p.GetRole() == "admin" && p.GetObject() == "tenant" && p.GetAction() == "get" {
			adminTenantGet = true
		}
	}
	if !adminTenantGet {
		t.Fatalf("seed policy admin:tenant:get missing")
	}

	// 4. 新增策略行（主键防重）+ 删除。
	code, _ = callPermissionPolicy(t, base, tok, http.MethodPost, "/v1/permission/policy",
		map[string]any{"role": "viewer", "object": "dict", "action": "list"})
	if code != http.StatusCreated {
		t.Fatalf("create policy status=%d", code)
	}
	if code, _ = callPermissionPolicy(t, base, tok, http.MethodPost, "/v1/permission/policy",
		map[string]any{"role": "viewer", "object": "dict", "action": "list"}); code != http.StatusBadRequest {
		t.Fatalf("duplicate policy must 400 (unique role:object:action), got %d", code)
	}
	if code, _ = callPermissionPolicy(t, base, tok, http.MethodDelete, "/v1/permission/policy/viewer:dict:list", nil); code != http.StatusOK {
		t.Fatalf("delete policy status=%d", code)
	}
	if code, _ = callPermissionPolicy(t, base, tok, http.MethodDelete, "/v1/permission/policy/viewer:dict:list", nil); code != http.StatusNotFound {
		t.Fatalf("double delete must 404, got %d", code)
	}
}

// TestT3Authz_DataDrivenPolicy 数据化策略的授权行为（P9 同源验证）：
//   - viewer 读 menu/permission 200（p 行 viewer 只读存在）；
//   - viewer 写 menu/permission 403（写策略行仅授予 admin）；
//   - admin 写 201（策略数据 → 授权放行全链路）。
func TestT3Authz_DataDrivenPolicy(t *testing.T) {
	base := startTenantREST(t)
	viewer := tenantToken(t, "alice", "u-alice", "viewer", "t-default")
	admin := tenantToken(t, "admin", "u-admin", "admin", "t-default")

	// viewer 只读放行。
	if code, _ := callMenu(t, base, viewer, http.MethodGet, "/v1/menu", nil); code != http.StatusOK {
		t.Fatalf("viewer list menu status=%d, want 200", code)
	}
	if code, _ := callPermissionPolicy(t, base, viewer, http.MethodGet, "/v1/permission/policy", nil); code != http.StatusOK {
		t.Fatalf("viewer list policy status=%d, want 200", code)
	}
	// viewer 写被拒（casbin 数据化 p 行：viewer 无 menu:write）。
	if code, _ := callMenu(t, base, viewer, http.MethodPost, "/v1/menu",
		map[string]any{"id": "menu-x", "type": "TYPE_MENU"}); code != http.StatusForbidden {
		t.Fatalf("viewer create menu status=%d, want 403", code)
	}
	if code, _ := callPermission(t, base, viewer, http.MethodDelete, "/v1/permission/auth:get", nil); code != http.StatusForbidden {
		t.Fatalf("viewer delete permission status=%d, want 403", code)
	}
	// admin 写放行（g 行 u-admin→admin 来自 User.Roles 数据化装载）。
	if code, _ := callMenu(t, base, admin, http.MethodPost, "/v1/menu",
		map[string]any{"id": "menu-x", "type": "TYPE_MENU", "name": "X", "path": "/x"}); code != http.StatusCreated {
		t.Fatalf("admin create menu status=%d, want 201", code)
	}
}
