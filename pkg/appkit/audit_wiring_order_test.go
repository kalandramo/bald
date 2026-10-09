package appkit

import (
	"context"
	"net/http"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"

	"github.com/kalandramo/bald/bootstrap"
	"github.com/kalandramo/bald/pkg/audit"
)

// 审计后端装配时序（2026-09-28）：AuditProvider 必须在**数据/缓存客户端
// 就绪之后**被调用，且经 res 参数拿到已填充的资源容器。
//
// 为什么必须有它：store 审计后端要 *gorm.DB、stream 后端要 redis 客户端。
// 原实现把 buildAudit 排在 buildDatabases **之前**——后端构造时资源必为
// nil，业务被迫在应用层写惰性包装（lazyStoreAuditor：构造期只捕获取值
// 函数，请求期首次 Record 才解析 DB）绕开。这与 T11/T12 修的「装配时序
// 倒置」同族：消费方早于依赖就绪。
//
// 本测试把该不变量机械固化——若有人把 buildAudit 挪回 buildDatabases 之前，
// 这里会红（provider 拿到的 res 里取不到 database 客户端）。
//
// RED 场景：把 bootstrap.go 阶段 B 的 buildAudit 调用移到 buildDatabases 之前
// 即失败（res.Database("sql") 返回 not-ok）。
func TestFromBootstrap_AuditProviderSeesDatabases(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	// 声明 database.sql 段 + audit 段（store 后端）——二者都存在时，
	// 审计装配必须在数据库装配之后。
	cfg.Database = &bootstrapv1.Database{
		Sql: &bootstrapv1.Database_SQL{Driver: "sqlite", Source: ":memory:"},
	}
	cfg.Audit = &bootstrapv1.Audit{Backends: []string{"probe"}}

	const wantDB = "probe-db"
	dr := bootstrap.NewDatabaseRegistry()
	dr.MustRegister("sql", func(context.Context, *bootstrapv1.Database) (any, func(), error) {
		return wantDB, nil, nil
	})

	// probe provider 记录「构造时 res 里能否取到 database 客户端」。
	var (
		sawDB    bool
		calledAt time.Time
	)
	ar := NewAuditRegistry()
	ar.MustRegister("probe", func(_ context.Context, _ *bootstrapv1.Audit, res *AppKit) (audit.Auditor, func(context.Context) error, error) {
		calledAt = time.Now()
		if res == nil {
			return nil, nil, errProbe("res is nil")
		}
		if v, ok := res.Database("sql"); ok && v == wantDB {
			sawDB = true
		}
		return audit.NewLoggerAuditor(), nil, nil
	})

	a, err := FromBootstrap(cfg,
		WithConfigNamespace(testConfigNamespace), WithHTTP(new(http.ServeMux)),
		WithDatabaseRegistry(dr),
		WithAuditRegistry(ar),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}

	runBriefly(t, a, 50*time.Millisecond)

	if !sawDB {
		t.Fatalf("audit provider 构造时未取到 database 客户端（calledAt=%v）——"+
			"buildAudit 必须排在 buildDatabases 之后", calledAt)
	}
}

type errProbe string

func (e errProbe) Error() string { return string(e) }
