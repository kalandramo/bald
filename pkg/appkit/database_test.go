package appkit

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
)

// 约定装配：阶段 B 构建 → Database 取用 → 停机 cleanup。

func bconfNewBootstrapWithDatabase() *bootstrapv1.BootstrapConfig {
	cfg := bconf.NewBootstrap()
	cfg.Database = &bootstrapv1.Database{
		Sql: &bootstrapv1.Database_SQL{Driver: "sqlite", Source: "stub-dsn"},
	}
	return cfg
}

func TestAssemble_ContractDatabaseLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	type stubClient struct{ name string }
	cleaned := false
	prov := stubDomainProvider("database", "sql",
		func(c *bootstrapv1.BootstrapConfig) bool { return c.GetDatabase().GetSql() != nil },
		&stubClient{name: "sql"}, func() { cleaned = true })

	cfg := bconfNewBootstrapWithDatabase()
	dynamicAddr(cfg)

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)), prov)
	// 构造期（阶段 A）尚未装载契约：Database 为空
	if _, ok := a.lookup("database.sql"); ok {
		t.Fatal("Database should be empty before Run (phase B not reached)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	cli, ok := a.lookup("database.sql")
	if !ok {
		t.Fatal("database client not built from contract")
	}
	if c, ok := cli.(*stubClient); !ok || c.name != "sql" {
		t.Fatalf("client type mismatch: %T", cli)
	}
	if all := resourceMap(t, a, "database"); len(all) != 1 {
		t.Fatalf("Resource(\"database\") snapshot = %v, want 1 entry", all)
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

// 契约 database 段存在但无 database Provider → 阶段 B fail-fast。
func TestAssemble_DatabaseSectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconfNewBootstrapWithDatabase()
	dynamicAddr(cfg)

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)))
	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no consumer") || !strings.Contains(err.Error(), "database") {
		t.Fatalf("expected no-consumer error for database, got %v", err)
	}
}

// database 段缺失（纯后台进程）→ no-op，不要求 Provider。
func TestAssemble_NoDatabaseSection(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := a.lookup("database.sql"); ok {
		t.Fatal("no database should be built without database section")
	}
}
