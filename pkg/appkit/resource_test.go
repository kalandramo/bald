package appkit

import (
	"context"
	"testing"
)

// resourceMap 拆包 Resource 的两返回值为 map 视图（测试辅助：段缺集合或形状
// 不符即 Fatal）。原 Ais()/Databases() 等集合访问器的调用点经此迁移。
func resourceMap(t *testing.T, a *App, segment string) map[string]any {
	t.Helper()
	v, ok := a.Resource(segment)
	if !ok {
		t.Fatalf("Resource(%q) missing", segment)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("Resource(%q) = %T, want map[string]any", segment, v)
	}
	return m
}

// TestResource_SegmentPath 契约：map 产物按 "<顶层段>.<子段>" 展开、非 map
// 产物落 "<顶层段>"（storeProduct.setResources 的统一约定）。
func TestResource_SegmentPath(t *testing.T) {
	a := NewApp(effectOpt("noop", func(context.Context) error { return nil }))
	a.setResources("database", map[string]any{"sql": "SQL_CLIENT"})
	a.setResources("registry", "REGISTRAR")

	if v, ok := a.Resource("database.sql"); !ok || v != "SQL_CLIENT" {
		t.Fatalf("Resource(database.sql) = (%v,%v), want (SQL_CLIENT,true)", v, ok)
	}
	if v, ok := a.Resource("registry"); !ok || v != "REGISTRAR" {
		t.Fatalf("Resource(registry) = (%v,%v), want (REGISTRAR,true)", v, ok)
	}
	if _, ok := a.Resource("cache.redis"); ok {
		t.Fatal("Resource(cache.redis) should be missing")
	}
	if _, ok := a.Resource(""); ok {
		t.Fatal("empty segment should be missing")
	}
}

// TestResource_SnapshotIsCopy 副本语义（D1 §4.6 既有取舍，原 Ais() 返回副本）：
// 顶层段读取返回副本快照——改写快照不影响内部资源面，内部后续写入也不影响
// 已取出的快照。
func TestResource_SnapshotIsCopy(t *testing.T) {
	a := NewApp(effectOpt("noop", func(context.Context) error { return nil }))
	a.setResources("ai", map[string]any{"openai": "V1"})

	snap := resourceMap(t, a, "ai")
	if len(snap) != 1 || snap["openai"] != "V1" {
		t.Fatalf("snapshot = %v, want map with openai=V1", snap)
	}

	// 快照改写不影响内部。
	snap["openai"] = "MUTATED"
	delete(snap, "openai")
	if v, _ := a.Resource("ai.openai"); v != "V1" {
		t.Fatalf("internal mutated through snapshot: got %v, want V1", v)
	}

	// 内部后续写入不影响已取出的快照。
	a.setResources("ai", map[string]any{"openai": "V2"})
	if v, _ := a.Resource("ai.openai"); v != "V2" {
		t.Fatalf("internal update invisible: got %v, want V2", v)
	}
	if len(snap) != 0 {
		t.Fatalf("stale snapshot changed: %v", snap)
	}
}

// TestResource_NilAndEmpty 产物 nil 跳过、空 map 展开为零键（顶层读取缺失）。
func TestResource_NilAndEmpty(t *testing.T) {
	a := NewApp(effectOpt("noop", func(context.Context) error { return nil }))
	a.setResources("cache", map[string]any(nil))
	if _, ok := a.Resource("cache"); ok {
		t.Fatal("empty product map should not create the top key")
	}
	a.setResources("tracer", nil)
	if _, ok := a.Resource("tracer"); ok {
		t.Fatal("nil product should not be stored")
	}
}
