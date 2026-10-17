// audit_registry_test.go —— audit 段装配契约（批 2 W2b 迁移：原
// AuditRegistry 的查表语义已吸收进用户侧 Provider + pkg/audit.Compose，
// 组装顺序/回滚/聚合错误由 pkg/audit 的 Compose 单测承载；本文件保留
// std 内置 audit Provider 的契约与引擎全局注入的编排断言）。
package appkit

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bootstrap"
	log "github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
)

// taggedAuditor 带名字标记的桩后端。
type taggedAuditor struct {
	audit.Auditor
	tag string
}

// auditCfg 构造带 backends 列表的 audit 契约段。
func auditCfg(backends ...string) *bootstrapv1.Audit {
	return &bootstrapv1.Audit{Backends: backends}
}

// TestStdAuditProvider_Build std 内置 audit Provider 契约：log 内置零注册
// 即得 LoggerAuditor；backends 空/第三方项 fail-fast（教学文案直指 Provider
// 覆盖出口——原 AuditRegistry.Build 的「not registered」文案随注册表一并
// 退役，有意行为变更）。
func TestStdAuditProvider_Build(t *testing.T) {
	p := stdAuditProvider{}
	scope := BuildScope{Config: &bootstrapv1.BootstrapConfig{}}

	// 段缺省：不装配。
	if a, _, err := p.Build(context.Background(), scope); a != nil || err != nil {
		t.Fatalf("nil section: a=%v err=%v", a, err)
	}

	// 空列表：fail-fast。
	scope.Config.Audit = &bootstrapv1.Audit{}
	if _, _, err := p.Build(context.Background(), scope); err == nil {
		t.Fatal("empty backends should fail")
	}

	// [log]：内置 LoggerAuditor。
	scope.Config.Audit = auditCfg("log")
	a, cleanup, err := p.Build(context.Background(), scope)
	if err != nil || a == nil || cleanup != nil {
		t.Fatalf("log builtin: a=%v cleanup-set=%v err=%v", a, cleanup != nil, err)
	}
	if _, ok := a.(audit.LoggerAuditor); !ok {
		t.Fatalf("log builtin should return LoggerAuditor, got %T", a)
	}

	// [store]：std 不含第三方后端，fail-fast 并指向 Provider 覆盖出口。
	scope.Config.Audit = auditCfg("store")
	_, _, err = p.Build(context.Background(), scope)
	if err == nil || !strings.Contains(err.Error(), "declare an audit Provider") {
		t.Fatalf("store should fail with provider guidance, got %v", err)
	}

	// 重复项：经 audit.Compose 重复检测 fail-fast。
	scope.Config.Audit = auditCfg("log", "log")
	if _, _, err = p.Build(context.Background(), scope); err == nil {
		t.Fatal("duplicate backend should fail")
	}
}

// TestAssemble_AuditGlobalInjection 引擎编排断言：audit 段 Provider 的产物
// 全局注入（SetAuditor）、prev 保留、停机 Effect 恢复装配前全局并兑现后端
// cleanup（appkit:audit-restore 逆序回放最后执行）。
func TestAssemble_AuditGlobalInjection(t *testing.T) {
	old := audit.GetAuditor()
	t.Cleanup(func() { audit.SetAuditor(old) })

	var cleaned int
	prov := auditTestProvider(func(_ context.Context, _ *bootstrapv1.Audit, _ *AppKit) (audit.Auditor, func(context.Context) error, error) {
		return taggedAuditor{Auditor: audit.NopAuditor(), tag: "contract"},
			func(context.Context) error { cleaned++; return nil }, nil
	})

	cfg := bconf.NewBootstrap() // 预填 server 段豁免（无能力 ⇒ 不绑端口，L3 不报错）
	cfg.Audit = auditCfg("log")

	a := assembleForTest(t, cfg, WithConfigNamespace(testConfigNamespace), prov)

	// 注入发生在阶段 B（Run 期）——驱动 Run 后、停机前断言全局句柄。
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	if _, isContract := audit.GetAuditor().(taggedAuditor); !isContract {
		t.Fatalf("global auditor should be contract backend during Run, got %T", audit.GetAuditor())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	if cleaned != 1 {
		t.Fatalf("audit cleanup called %d times, want 1 (stop replay)", cleaned)
	}
	if audit.GetAuditor() != old {
		t.Fatalf("global auditor not restored after stop, got %T", audit.GetAuditor())
	}
}

// TestAssemble_AuditProviderSeesDatabases 迁移自 audit_wiring_order_test.go
// 的核心断言（该文件整体并入此处）：audit Provider 构造时经 scope.App 取到
// 已就绪的 database 客户端——audit 段 canonical 序必须排在 database 之后。
func TestAssemble_AuditProviderSeesDatabases(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Database = &bootstrapv1.Database{
		Sql: &bootstrapv1.Database_SQL{Driver: "sqlite", Source: ":memory:"},
	}
	cfg.Audit = auditCfg("probe")

	const wantDB = "probe-db"
	dr := bootstrap.NewDatabaseRegistry()
	dr.MustRegister("sql", func(context.Context, *bootstrapv1.Database) (any, func(), error) {
		return wantDB, nil, nil
	})

	var sawDB bool
	prov := auditTestProvider(func(_ context.Context, _ *bootstrapv1.Audit, res *AppKit) (audit.Auditor, func(context.Context) error, error) {
		if res == nil {
			return nil, nil, errProbe("res is nil")
		}
		if v, ok := res.Database("sql"); ok && v == wantDB {
			sawDB = true
		}
		return audit.NewLoggerAuditor(), nil, nil
	})

	a := assembleForTest(t, cfg, WithConfigNamespace(testConfigNamespace), WithHTTP(new(http.ServeMux)),
		StandardServerProvider(), dbTestProvider(dr), prov)
	runBriefly(t, a, 50*time.Millisecond)

	if !sawDB {
		t.Fatal("audit provider 构造时未取到 database 客户端——audit 段 canonical 序必须在 database 之后")
	}
}

type errProbe string

func (e errProbe) Error() string { return string(e) }
