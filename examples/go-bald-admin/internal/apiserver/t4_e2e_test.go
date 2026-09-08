package apiserver

// t4_e2e_test.go 字典管理 REST e2e（T4 验收）：真实 gin 引擎 + httptest +
// miniredis 真实 Redis（dict biz 注入 Cache-Aside）。
//
// 验收点（移植计划 T4）：
//  1. 类型/条目 CRUD（业务键防重 400、类型删除级联条目）
//  2. Cache-Aside 命中可观测：读后缓存键真实存在（miniredis）；
//     写穿透失效：Update/Delete 后键消失、再读重载新值
//  3. 授权：viewer 读 200 / 写 403（dict_type/dict_entry 策略数据化）
//  4. P8 隔离：t-other 用户看不到 t-default 的字典种子（缓存键亦含租户维度）
//  5. Redis 停机降级直连 loader 由 contrib/cache-redis 单测覆盖（miniredis Close）

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"google.golang.org/protobuf/encoding/protojson"

	gingonic "github.com/gin-gonic/gin"

	rediscache "github.com/kalandramo/bald-cache-redis"
	dictv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/dict/v1"
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
)

// startDictREST 起真实 gin 引擎 + miniredis 真实 Redis（dict biz 注入缓存，
// 其余 biz 沿用 startTenantREST 形态）。返回 base URL 与缓存实例（键存在性断言）。
func startDictREST(t *testing.T) (string, *rediscache.Cache) {
	t.Helper()
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	cache, err := rediscache.New(mr.Addr())
	if err != nil {
		t.Fatalf("rediscache.New: %v", err)
	}
	e := gingonic.New()
	RegisterRoutes(e, authbiz.New(bootstrappkg.Signer), secretbiz.New(nil), tenantbiz.New(),
		userbiz.New(), menubiz.New(), permissionbiz.New(), dictbiz.New(cache), filebiz.New(nil, ""), auditlogbiz.New())
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv.URL, cache
}

// callDictType / callDictEntry 通用 REST 调用（响应体 protojson 解码）。
func callDictType(t *testing.T, base, tok, method, path string, body any) (int, *dictv1.ListDictTypesResponse) {
	t.Helper()
	code, raw := callRaw(t, base, tok, method, path, body)
	out := new(dictv1.ListDictTypesResponse)
	decodePB(raw, out)
	return code, out
}

func callDictEntry(t *testing.T, base, tok, method, path string, body any) (int, *dictv1.ListDictEntriesResponse) {
	t.Helper()
	code, raw := callRaw(t, base, tok, method, path, body)
	out := new(dictv1.ListDictEntriesResponse)
	decodePB(raw, out)
	return code, out
}

// TestDictREST_TypeLifecycle 类型全生命周期 + 级联删除（admin）。
func TestDictREST_TypeLifecycle(t *testing.T) {
	base, _ := startDictREST(t)
	tok := tenantToken(t, "admin", "u-admin", "admin", "t-default")

	// 1. 种子类型列表：3 组，SortOrder 升序 gender/status/yes_no。
	code, list := callDictType(t, base, tok, http.MethodGet, "/v1/dict_type", nil)
	if code != http.StatusOK {
		t.Fatalf("list types status=%d", code)
	}
	if list.GetTotal() != 3 {
		t.Fatalf("expect 3 seed types, got %d", list.GetTotal())
	}
	for i, want := range []string{"gender", "status", "yes_no"} {
		if list.GetItems()[i].GetId() != want {
			t.Fatalf("types[%d]=%s, want %s (sort_order asc)", i, list.GetItems()[i].GetId(), want)
		}
	}

	// 2. 创建 → 重复创建 409（业务键租户内唯一；store.ErrConflict → Conflict，
	//    此前 handler 一刀切 400，错误映射统一后回归 REST 惯例语义）。
	if code, _ = callDictType(t, base, tok, http.MethodPost, "/v1/dict_type",
		map[string]any{"id": "priority", "type_name": "优先级", "sort_order": 4}); code != http.StatusCreated {
		t.Fatalf("create type status=%d", code)
	}
	if code, _ = callDictType(t, base, tok, http.MethodPost, "/v1/dict_type",
		map[string]any{"id": "priority"}); code != http.StatusConflict {
		t.Fatalf("duplicate type must 409, got %d", code)
	}

	// 3. 更新：改名称 + 显式停用（enabled_set 位语义）。
	if code, _ = callDictType(t, base, tok, http.MethodPut, "/v1/dict_type/priority",
		map[string]any{"type_name": "优先级(改)", "enabled": false, "enabled_set": true}); code != http.StatusOK {
		t.Fatalf("update type status=%d", code)
	}

	// 4. 级联删除：建 2 条条目后删类型，条目一并消失。
	if code, _ = callDictEntry(t, base, tok, http.MethodPost, "/v1/dict_entry",
		map[string]any{"type_code": "priority", "value": "high", "label": "高"}); code != http.StatusCreated {
		t.Fatalf("create entry status=%d", code)
	}
	if code, _ = callDictEntry(t, base, tok, http.MethodPost, "/v1/dict_entry",
		map[string]any{"type_code": "priority", "value": "low", "label": "低"}); code != http.StatusCreated {
		t.Fatalf("create entry#2 status=%d", code)
	}
	if code, _ = callDictType(t, base, tok, http.MethodDelete, "/v1/dict_type/priority", nil); code != http.StatusOK {
		t.Fatalf("delete type status=%d", code)
	}
	if code, _ := callDictEntry(t, base, tok, http.MethodGet, "/v1/dict_entry?type_code=priority", nil); code != http.StatusNotFound {
		t.Fatalf("entries of deleted type must 404 (type gone), got %d", code)
	}
	if code, _ := callDictEntry(t, base, tok, http.MethodGet, "/v1/dict_entry/priority:high", nil); code != http.StatusNotFound {
		t.Fatalf("entry must cascade-deleted, got %d", code)
	}
	if code, _ := callDictType(t, base, tok, http.MethodDelete, "/v1/dict_type/priority", nil); code != http.StatusNotFound {
		t.Fatalf("double delete type must 404, got %d", code)
	}
}

// TestDictREST_EntryLifecycleAndCache 条目 CRUD + Cache-Aside 命中/写穿透失效
// （T4 核心验收：缓存键真实存在于 miniredis，失效可观测）。
func TestDictREST_EntryLifecycleAndCache(t *testing.T) {
	base, cache := startDictREST(t)
	tok := tenantToken(t, "admin", "u-admin", "admin", "t-default")
	ctx := context.Background()
	ck := rediscache.Key("dict:entries", "t-default", "gender") // 租户维度键

	// 1. 创建条目（numeric 演示，sort_order=4 落尾）→ 重复 409（type_code:value 业务键唯一，
	//    错误映射统一后 store.ErrConflict → 409）。
	num := 9
	if code, _ := callDictEntry(t, base, tok, http.MethodPost, "/v1/dict_entry",
		map[string]any{"type_code": "gender", "value": "custom", "label": "自定义", "numeric": num, "sort_order": 4}); code != http.StatusCreated {
		t.Fatalf("create entry status=%d", code)
	}
	if code, _ := callDictEntry(t, base, tok, http.MethodPost, "/v1/dict_entry",
		map[string]any{"type_code": "gender", "value": "custom", "label": "dup"}); code != http.StatusConflict {
		t.Fatalf("duplicate entry must 409, got %d", code)
	}

	// 2. 首次读：未命中经 loader 回填 → 4 条（3 种子 + 1 新建）且排序正确。
	code, entries := callDictEntry(t, base, tok, http.MethodGet, "/v1/dict_entry?type_code=gender", nil)
	if code != http.StatusOK || entries.GetTotal() != 4 {
		t.Fatalf("list entries status=%d total=%d, want 200/4", code, entries.GetTotal())
	}
	for i, want := range []string{"gender:male", "gender:female", "gender:unknown", "gender:custom"} {
		if entries.GetItems()[i].GetId() != want {
			t.Fatalf("entries[%d]=%s, want %s", i, entries.GetItems()[i].GetId(), want)
		}
	}
	// 缓存键真实存在（Cache-Aside 命中可观测——写穿透/失效断言的基线）。
	if _, err := cache.Client().Get(ctx, ck).Result(); err != nil {
		t.Fatalf("cache key %s must be backfilled, got err=%v", ck, err)
	}

	// 3. 写穿透失效：更新条目 → 键消失 → 再读重载新值并回填。
	if code, _ = callDictEntry(t, base, tok, http.MethodPut, "/v1/dict_entry/gender:custom",
		map[string]any{"label": "自定义(改)"}); code != http.StatusOK {
		t.Fatalf("update entry status=%d", code)
	}
	if _, err := cache.Client().Get(ctx, ck).Result(); err == nil {
		t.Fatalf("cache key must be invalidated after update (write-through)")
	}
	code, entries = callDictEntry(t, base, tok, http.MethodGet, "/v1/dict_entry?type_code=gender", nil)
	if code != http.StatusOK || entries.GetTotal() != 4 {
		t.Fatalf("re-list entries status=%d total=%d", code, entries.GetTotal())
	}
	if last := entries.GetItems()[3]; last.GetLabel() != "自定义(改)" {
		t.Fatalf("updated label = %q, want reloaded from store", last.GetLabel())
	}

	// 4. 删除条目 → 失效 → 列表少一条。
	if code, _ = callDictEntry(t, base, tok, http.MethodDelete, "/v1/dict_entry/gender:custom", nil); code != http.StatusOK {
		t.Fatalf("delete entry status=%d", code)
	}
	if code, _ = callDictEntry(t, base, tok, http.MethodGet, "/v1/dict_entry?type_code=gender", nil); code != http.StatusOK {
		t.Fatalf("list after delete status=%d", code)
	}
	if _, entries = callDictEntry(t, base, tok, http.MethodGet, "/v1/dict_entry?type_code=gender", nil); entries.GetTotal() != 3 {
		t.Fatalf("entries total after delete = %d, want 3", entries.GetTotal())
	}
	if code, _ := callDictEntry(t, base, tok, http.MethodDelete, "/v1/dict_entry/gender:custom", nil); code != http.StatusNotFound {
		t.Fatalf("double delete entry must 404, got %d", code)
	}
}

// TestDictAuthz_DictPoliciesAndTenantIsolation 授权 + 租户隔离（P9/P8 同源验证）：
//   - viewer 读 dict_type/dict_entry 200（p 行 viewer 只读）；
//   - viewer 写 403（写策略行仅授予 admin）；
//   - t-other 用户看不到 t-default 的字典种子（P8 隔离；缓存键含租户维度）。
func TestDictAuthz_DictPoliciesAndTenantIsolation(t *testing.T) {
	base, _ := startDictREST(t)
	viewer := tenantToken(t, "alice", "u-alice", "viewer", "t-default")
	other := tenantToken(t, "bob", "u-bob", "viewer", "t-other")

	// viewer 只读放行。
	if code, _ := callDictType(t, base, viewer, http.MethodGet, "/v1/dict_type", nil); code != http.StatusOK {
		t.Fatalf("viewer list types status=%d, want 200", code)
	}
	if code, _ := callDictEntry(t, base, viewer, http.MethodGet, "/v1/dict_entry?type_code=gender", nil); code != http.StatusOK {
		t.Fatalf("viewer list entries status=%d, want 200", code)
	}
	// viewer 写被拒（数据化 p 行：viewer 无 dict_type:post / dict_entry:write）。
	if code, _ := callDictType(t, base, viewer, http.MethodPost, "/v1/dict_type",
		map[string]any{"id": "x"}); code != http.StatusForbidden {
		t.Fatalf("viewer create type status=%d, want 403", code)
	}
	if code, _ := callDictEntry(t, base, viewer, http.MethodDelete, "/v1/dict_entry/gender:male", nil); code != http.StatusForbidden {
		t.Fatalf("viewer delete entry status=%d, want 403", code)
	}
	// t-other 租户：P8 隔离——字典种子全在 t-default，其他租户不可见。
	code, list := callDictType(t, base, other, http.MethodGet, "/v1/dict_type", nil)
	if code != http.StatusOK || list.GetTotal() != 0 {
		t.Fatalf("cross-tenant list status=%d total=%d, want 200/0 (P8 isolation)", code, list.GetTotal())
	}
}

// t4 时间戳字段解码冒烟：protojson Timestamp 侧通道（created_at RFC3339 字符串）
// 不炸 DiscardUnknown 解码即视为通过（字段值断言在 handler 层无意义，格式兼容即可）。
func TestDictREST_TimestampFieldsDecode(t *testing.T) {
	base, _ := startDictREST(t)
	tok := tenantToken(t, "admin", "u-admin", "admin", "t-default")
	_, raw := callRaw(t, base, tok, http.MethodGet, "/v1/dict_type", nil)
	if len(raw) == 0 {
		t.Fatalf("empty response")
	}
	out := new(dictv1.ListDictTypesResponse)
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, out); err != nil {
		t.Fatalf("protojson decode: %v", err)
	}
	if out.GetItems()[0].GetCreatedAt() == nil {
		t.Fatalf("created_at must be present (seed rows persisted)")
	}
}
