package appkit

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	"github.com/kalandramo/bald/log"
)

// FromBootstrap 契约装配：阶段 B 构建 → Database 取用 → 停机 cleanup。
// Registry 行为测试（注册/段枚举/回滚）已随六域 Registry 迁 bootstrap
// （bootstrap/database_test.go，2026-09-15）。

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
	dr := baldbootstrap.NewDatabaseRegistry()
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
