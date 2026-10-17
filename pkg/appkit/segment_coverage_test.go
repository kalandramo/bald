// L3 段覆盖检查的单测（批 2 W2b：告警升级为启动期 error，裁决 2；有意
// 行为变更——原 5 个告警断言迁移为报错语义，逐处见各测试注释）。
//
// 覆盖：含 script 段的合法配置 → 启动期 error 且列出段名；白名单豁免
// （WithUnimplementedSegments）；全部出现的段都被消费 → 无错误；端到端走
// 配置文件装载路径（script 由 config 层贡献）；零能力声明一对钉子：预填
// 默认 server 段不报错（框架自填非用户意图，批 1 预填豁免沿用），用户
// 覆盖 server 子字段则报错；server 非内置子段无 Provider 接管 → 报错
// （原 bootstrap.validateServerSections 语义并入 L3）。
package appkit

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bootstrap"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
	log "github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/transport"
)

// runExpectErr 驱动 Run 并断言返回错误包含全部 want 子串（L3 报错发生在
// BeforeStart 装配链末尾——契约已装载定型，经 Run 失败路径返回）。
func runExpectErr(t *testing.T, a *AppKit, want ...string) {
	t.Helper()
	err := a.Run(context.Background())
	if err == nil {
		t.Fatal("expected startup error, got nil")
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Fatalf("error %q does not contain %q", err.Error(), w)
		}
	}
}

// 含 script 段的合法配置：启动期报错、列出的就是 script（原告警测试
// TestAssemble_WarnsUnconsumedScript 的报错语义迁移；原「启动不受影响」
// 断言反转为「启动被拦截」）。
func TestAssemble_ErrorsUnconsumedScript(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Script = &bootstrapv1.Script{Engine: bootstrapv1.Script_LUA}

	a := assembleForTest(t, cfg,
		ConfigNamespace(testConfigNamespace), WithHTTP(new(http.ServeMux)),
		StandardServerProvider())
	runExpectErr(t, a, "no consumer", "script", "WithUnimplementedSegments")
}

// 白名单豁免：WithUnimplementedSegments("script") 显式承认「该段暂不生效」，
// 启动放行（告警级无豁免出口，报错级新增）。
func TestAssemble_WhitelistExemptsScript(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Script = &bootstrapv1.Script{Engine: bootstrapv1.Script_LUA}

	a := assembleForTest(t, cfg,
		ConfigNamespace(testConfigNamespace), WithHTTP(new(http.ServeMux)),
		WithUnimplementedSegments("script"),
		StandardServerProvider())
	runBriefly(t, a, 30*time.Millisecond) // Run 无错 = 白名单豁免生效
}

// 全部出现的段都被消费：无报错（原 TestAssemble_NoWarnWhenAllConsumed 迁移：
// 告警消失断言 → Run 无错）。
func TestAssemble_NoErrorWhenAllConsumed(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Database = &bootstrapv1.Database{
		Sql: &bootstrapv1.Database_SQL{Driver: "sqlite", Source: ":memory:"},
	}
	dr := bootstrap.NewDatabaseRegistry()
	dr.MustRegister("sql", func(context.Context, *bootstrapv1.Database) (any, func(), error) {
		return "db", nil, nil
	})

	a := assembleForTest(t, cfg, ConfigNamespace(testConfigNamespace),
		dbTestProvider(dr), StandardServerProvider())
	runBriefly(t, a, 50*time.Millisecond)
}

// 端到端（配置文件装载路径）：script 段由 config 层贡献（非代码内联），
// 装载完成后 L3 如实报出（原 TestAssemble_WarnsScriptFromConfigLayer 迁移）。
func TestAssemble_ErrorsScriptFromConfigLayer(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	reg := bootstrap.NewConfigRegistry()
	reg.MustRegister("fake", func(context.Context, *bootstrapv1.BootstrapConfig) (*baldconfig.Layer, func(), error) {
		return &baldconfig.Layer{
			Name:   "fake",
			Reader: &fakeReader{data: []byte("script:\n  options:\n    enabled: true\n")},
		}, nil, nil
	})

	a := assembleForTest(t, cfg,
		ConfigNamespace(testConfigNamespace), WithHTTP(new(http.ServeMux)),
		configTestProvider(reg), StandardServerProvider())
	runExpectErr(t, a, "no consumer", "script")
}

// 零能力声明 + 未改动的预填默认 server 段（bconf.NewBootstrap 的 http :8080 /
// grpc :9090）→ 不报错——预填是框架自己的行为，对其报错属自相矛盾的噪音
// （批 1 预填豁免沿用；原 TestAssemble_ZeroCapabilityDefaultServerNoWarn 迁移）。
func TestAssemble_ZeroCapabilityDefaultServerNoError(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()

	a := assembleForTest(t, cfg, ConfigNamespace(testConfigNamespace))
	runBriefly(t, a, 30*time.Millisecond)
}

// 零能力声明 + 用户覆盖任一 server 子字段 → 启动期报错——该段配置确实不
// 产生任何服务器，报错可行动（原 TestAssemble_ZeroCapabilityOverriddenServerWarns
// 迁移为报错语义）。
func TestAssemble_ZeroCapabilityOverriddenServerError(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	cfg.GetServer().GetHttp().Addr = "127.0.0.1:18080" // 用户覆盖：偏离预填默认

	a := assembleForTest(t, cfg, ConfigNamespace(testConfigNamespace))
	runExpectErr(t, a, "no consumer", "server")
}

// server 非内置子段（asynq）配置了但无 Provider 接管 → 启动期报错
// （原 bootstrap.validateServerSections 的「配了没实现」fail-fast 语义并入
// L3，文案保留可行动出路）。
func TestAssemble_ServerSubSegmentWithoutProvider(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Server.Asynq = &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"}

	a := assembleForTest(t, cfg, ConfigNamespace(testConfigNamespace),
		WithHTTP(new(http.ServeMux)), StandardServerProvider())
	runExpectErr(t, a, "server.asynq is configured but has no implementation")
}

// 白名单按子段路径豁免 server.asynq。
func TestAssemble_WhitelistExemptsServerSubSegment(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Server.Asynq = &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"}

	a := assembleForTest(t, cfg,
		ConfigNamespace(testConfigNamespace), WithHTTP(new(http.ServeMux)),
		WithUnimplementedSegments("server.asynq"),
		StandardServerProvider())
	runBriefly(t, a, 30*time.Millisecond)
}

// server 非内置子段有 Provider 接管（co-provider，server.asynq 路径）→
// 合法构建，与内置 http 面并存（原 WithServerRegistry 用户注册表语义的
// Provider 等价形态，详见 server_registry_test.go）。
func TestAssemble_ServerSubSegmentWithCoProvider(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Server.Asynq = &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"}

	var buildCalled, started atomic.Bool
	asynq := segProvider{segs: []string{"server.asynq"}, build: func(context.Context, BuildScope) (any, Cleanup, error) {
		buildCalled.Store(true)
		return []transport.Server{&probeServer{name: "asynq", started: &started, mark: func(string) {}}}, nil, nil
	}}

	a := assembleForTest(t, cfg,
		ConfigNamespace(testConfigNamespace), WithHTTP(new(http.ServeMux)),
		StandardServerProvider(), asynq)
	runBriefly(t, a, 50*time.Millisecond)

	if !buildCalled.Load() {
		t.Fatal("asynq co-provider Build not invoked (segment server.asynq present)")
	}
	if !started.Load() {
		t.Fatal("asynq co-provider server not started")
	}
	if len(a.servers) != 2 {
		t.Fatalf("servers = %d, want 2 (http builtin + asynq co-provider)", len(a.servers))
	}
	if a.Err() != nil {
		t.Fatalf("a.Err() = %v, want nil", a.Err())
	}
}
