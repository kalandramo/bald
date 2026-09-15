package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// --- DatabaseRegistry：显式注册与 fail-fast（自 pkg/appkit 迁入 2026-09-15）---

func TestDatabaseRegistry_FailFast(t *testing.T) {
	dr := NewDatabaseRegistry()
	dr.MustRegister("sql", func(context.Context, *bootstrapv1.Database) (any, func(), error) {
		return nil, nil, nil
	})

	// 重名
	err := dr.Register("sql", func(context.Context, *bootstrapv1.Database) (any, func(), error) {
		return nil, nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	// 空类型
	if err := dr.Register("", nil); err == nil {
		t.Fatal("expected empty type error")
	}
	// nil provider
	if err := dr.Register("mongodb", nil); err == nil {
		t.Fatal("expected nil provider error")
	}
	// Build：契约段整体为 nil → no-op（未配置任何数据库是合法状态）
	clients, cleanup, err := dr.Build(context.Background(), nil)
	if err != nil || clients != nil || cleanup != nil {
		t.Fatalf("nil section should be no-op, got (clients=%v, cleanup=%t, err=%v)", clients, cleanup != nil, err)
	}
	// Build：零段非 nil 契约 → no-op
	clients, cleanup, err = dr.Build(context.Background(), &bootstrapv1.Database{})
	if err != nil || clients == nil || len(clients) != 0 || cleanup != nil {
		t.Fatalf("empty section should be no-op, got (clients=%v, cleanup=%t, err=%v)", clients, cleanup != nil, err)
	}
	// Build：段存在但 provider 未注册（显式注册的核心收益：未 import 的引擎这里报错）
	_, _, err = dr.Build(context.Background(), &bootstrapv1.Database{
		Clickhouse: &bootstrapv1.Database_ClickHouse{Dsn: "clickhouse://x"},
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected not-registered error, got %v", err)
	}
}

// Build：段存在 → 查表构建，多段并存，全部返回。
func TestDatabaseRegistry_BuildMultiSection(t *testing.T) {
	dr := NewDatabaseRegistry()
	sqlClient, mongoClient := "sql-client", "mongo-client"
	cleaned := []string{}
	dr.MustRegister("sql", func(context.Context, *bootstrapv1.Database) (any, func(), error) {
		return sqlClient, func() { cleaned = append(cleaned, "sql") }, nil
	})
	dr.MustRegister("mongodb", func(context.Context, *bootstrapv1.Database) (any, func(), error) {
		return mongoClient, func() { cleaned = append(cleaned, "mongodb") }, nil
	})

	clients, cleanup, err := dr.Build(context.Background(), &bootstrapv1.Database{
		Sql:     &bootstrapv1.Database_SQL{Source: "dsn"},
		Mongodb: &bootstrapv1.Database_MongoDB{Uri: "mongodb://x"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if clients["sql"] != sqlClient || clients["mongodb"] != mongoClient {
		t.Fatalf("clients mismatch: %v", clients)
	}
	cleanup()
	// 聚合 cleanup 逆序回放：mongodb（后建）先于 sql
	if len(cleaned) != 2 || cleaned[0] != "mongodb" || cleaned[1] != "sql" {
		t.Fatalf("cleanup order = %v, want [mongodb sql]", cleaned)
	}
}

// Build：第二段构建失败 → 回滚第一段已建实例的 cleanup。
func TestDatabaseRegistry_BuildRollback(t *testing.T) {
	dr := NewDatabaseRegistry()
	rolled := false
	dr.MustRegister("sql", func(context.Context, *bootstrapv1.Database) (any, func(), error) {
		return "sql-client", func() { rolled = true }, nil
	})
	dr.MustRegister("mongodb", func(context.Context, *bootstrapv1.Database) (any, func(), error) {
		return nil, nil, errors.New("boom")
	})

	_, _, err := dr.Build(context.Background(), &bootstrapv1.Database{
		Sql:     &bootstrapv1.Database_SQL{Source: "dsn"},
		Mongodb: &bootstrapv1.Database_MongoDB{Uri: "mongodb://x"},
	})
	if err == nil || !strings.Contains(err.Error(), "build database mongodb") {
		t.Fatalf("expected mongodb build error, got %v", err)
	}
	if !rolled {
		t.Fatal("sql cleanup should be rolled back on mongodb failure")
	}
}
