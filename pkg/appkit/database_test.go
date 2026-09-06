package appkit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
)

// --- DatabaseRegistry：显式注册与 fail-fast ---

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

// --- FromBootstrap 契约装配：阶段 B 构建 → Database 取用 → 停机 cleanup ---

func bconfNewBootstrapWithDatabase() *bootstrapv1.BootstrapConfig {
	cfg := bconf.NewBootstrap()
	cfg.Database = &bootstrapv1.Database{
		Sql: &bootstrapv1.Database_SQL{Driver: "sqlite", Source: "stub-dsn"},
	}
	return cfg
}

func TestFromBootstrap_ContractDatabaseLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	type stubClient struct{ name string }
	cleaned := false
	dr := NewDatabaseRegistry()
	dr.MustRegister("sql", func(context.Context, *bootstrapv1.Database) (any, func(), error) {
		return &stubClient{name: "sql"}, func() { cleaned = true }, nil
	})

	cfg := bconfNewBootstrapWithDatabase()
	dynamicAddr(cfg)

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)), WithDatabaseRegistry(dr))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	// 构造期（阶段 A）尚未装载契约：Database 为空
	if _, ok := a.Database("sql"); ok {
		t.Fatal("Database should be empty before Run (phase B not reached)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	cli, ok := a.Database("sql")
	if !ok {
		t.Fatal("database client not built from contract")
	}
	if c, ok := cli.(*stubClient); !ok || c.name != "sql" {
		t.Fatalf("client type mismatch: %T", cli)
	}
	if all := a.Databases(); len(all) != 1 {
		t.Fatalf("Databases() = %v, want 1 entry", all)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
	}
	if !cleaned {
		t.Fatal("database cleanup should run on shutdown")
	}
}

// 契约 database 段存在但未接 DatabaseRegistry → 阶段 B fail-fast。
func TestFromBootstrap_DatabaseSectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconfNewBootstrapWithDatabase()
	dynamicAddr(cfg)

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)))
	if err != nil {
		t.Fatalf("FromBootstrap (构造期不校验合并后配置): %v", err)
	}
	err = a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "DatabaseRegistry") {
		t.Fatalf("expected DatabaseRegistry missing error, got %v", err)
	}
}

// database 段缺失（纯后台进程）→ no-op，不要求接注册表。
func TestFromBootstrap_NoDatabaseSection(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := a.Database("sql"); ok {
		t.Fatal("no database should be built without database section")
	}
}
