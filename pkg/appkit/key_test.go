// key_test.go —— 类型化资源句柄的运行时行为。
package appkit

import "testing"

func TestKey_Resource(t *testing.T) {
	a := &App{resources: map[string]any{
		"database.sql": "SQL_CLIENT",
		"audit":        "AUDITOR",
	}}
	sql := NewKey[string]("database.sql")
	if sql.Name() != "database.sql" {
		t.Fatalf("Name() = %q, want database.sql", sql.Name())
	}
	if v, ok := sql.Resource(a); !ok || v != "SQL_CLIENT" {
		t.Fatalf("typed hit = (%v, %v), want (SQL_CLIENT, true)", v, ok)
	}
	if v, ok := NewKey[int]("database.sql").Resource(a); ok || v != 0 {
		t.Fatalf("type mismatch = (%v, %v), want (0, false)", v, ok)
	}
	if v, ok := NewKey[string]("cache.redis").Resource(a); ok || v != "" {
		t.Fatalf("missing segment = (%v, %v), want (zero, false)", v, ok)
	}
}

func TestKey_ResourceNilAppAndZeroKey(t *testing.T) {
	var a *App
	if v, ok := NewKey[string]("audit").Resource(a); ok || v != "" {
		t.Fatalf("nil App = (%v, %v), want (zero, false)", v, ok)
	}
	var zero Key[string]
	if v, ok := zero.Resource(&App{resources: map[string]any{"": "x"}}); ok || v != "" {
		t.Fatalf("zero Key = (%v, %v), want (zero, false)", v, ok)
	}
}

func TestKey_TopLevelSnapshot(t *testing.T) {
	a := &App{resources: map[string]any{
		"database.sql":      "SQL_CLIENT",
		"database.mongo":    "MONGO_CLIENT",
		"unrelated.segment": "X",
	}}
	all, ok := NewKey[map[string]any]("database").Resource(a)
	if !ok || len(all) != 2 || all["sql"] != "SQL_CLIENT" || all["mongo"] != "MONGO_CLIENT" {
		t.Fatalf("top-level snapshot = (%v, %v), want sql+mongo", all, ok)
	}
	// 快照是副本：改动不影响内部资源面。
	all["sql"] = "OVERWRITTEN"
	if v, _ := NewKey[string]("database.sql").Resource(a); v != "SQL_CLIENT" {
		t.Fatalf("snapshot aliased internal map: %v", v)
	}
}
