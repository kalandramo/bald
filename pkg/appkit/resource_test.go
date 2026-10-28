package appkit

import (
	"context"
	"testing"
)

// resourceMap 拆包 lookup 的两返回值为 map 视图（测试辅助：段缺集合或形状
// 不符即 Fatal）。原 Ais()/Databases() 等集合访问器的调用点经此迁移。
func resourceMap(t *testing.T, a *App, segment string) map[string]any {
	t.Helper()
	v, ok := a.lookup(segment)
	if !ok {
		t.Fatalf("lookup(%q) missing", segment)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("lookup(%q) = %T, want map[string]any", segment, v)
	}
	return m
}

// TestResource_SegmentPath 契约：map 产物按 "<顶层段>.<子段>" 展开、非 map
// 产物落 "<顶层段>"（storeProduct.setResources 的统一约定）。
func TestResource_SegmentPath(t *testing.T) {
	a := NewApp(effectOpt("noop", func(context.Context) error { return nil }))
	a.setResources("database", map[string]any{"sql": "SQL_CLIENT"})
	a.setResources("registry", "REGISTRAR")

	if v, ok := a.lookup("database.sql"); !ok || v != "SQL_CLIENT" {
		t.Fatalf("lookup(database.sql) = (%v,%v), want (SQL_CLIENT,true)", v, ok)
	}
	if v, ok := a.lookup("registry"); !ok || v != "REGISTRAR" {
		t.Fatalf("lookup(registry) = (%v,%v), want (REGISTRAR,true)", v, ok)
	}
	if _, ok := a.lookup("cache.redis"); ok {
		t.Fatal("lookup(cache.redis) should be missing")
	}
	if _, ok := a.lookup(""); ok {
		t.Fatal("empty segment should be missing")
	}
}

// TestResource_SnapshotIsCopy 副本语义（既有取舍）：
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
	if v, _ := a.lookup("ai.openai"); v != "V1" {
		t.Fatalf("internal mutated through snapshot: got %v, want V1", v)
	}

	// 内部后续写入不影响已取出的快照。
	a.setResources("ai", map[string]any{"openai": "V2"})
	if v, _ := a.lookup("ai.openai"); v != "V2" {
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
	if _, ok := a.lookup("cache"); ok {
		t.Fatal("empty product map should not create the top key")
	}
	a.setResources("tracer", nil)
	if _, ok := a.lookup("tracer"); ok {
		t.Fatal("nil product should not be stored")
	}
}
