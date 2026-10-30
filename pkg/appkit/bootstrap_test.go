package appkit

// 约定装配层（assemble，经 Serve 触达）的单测（2026-09-06）。
//
// 覆盖：元数据契约驱动、能力声明 fail-fast、服务器构造、日志两阶段生命周期
// （阶段 A 默认 → 阶段 B 契约重建 → 停机恢复）、config Provider 层装配与
// cleanup 停机释放、热更新重建 Logger。

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
	log "github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/log/bslog"
	gateway "github.com/kalandramo/bald/transport/gateway"
)

// fakeReader 内存配置源桩（整文档源，key 恒为空串）。
type fakeReader struct{ data []byte }

func (f *fakeReader) Load(context.Context, string) ([]byte, error) { return f.data, nil }

// stubLogger 供测试桩返回的轻量 logger（slog 默认输出，不受契约段影响）。
func stubLogger() log.Logger {
	return bslog.New(bslog.NewOptions())
}

// 日志源两级分发：logger 段 Provider 接管（原 WithLogRegistry 通道的等价
// 形态）> 默认纯函数路径。Provider 在阶段 A（Config=nil）与阶段 B（完整
// 契约）各有一个调用点。
func TestResolveLoggerSource(t *testing.T) {
	var contractHits int
	loggerProv := segProvider{segs: []string{"logger"}, build: func(_ context.Context, s BuildScope) (any, Cleanup, error) {
		if s.Config == nil {
			return stubLogger(), nil, nil // 阶段 A：回退可用 Logger
		}
		contractHits++
		return stubLogger(), nil, nil
	}}
	spec := &Decl{Providers: []Provider{loggerProv}}
	plan, err := newProviderPlan(context.Background(), spec, &bootstrapv1.BootstrapConfig{}, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	src := resolveLoggerSource(spec, plan)

	// 阶段 A（cfg=nil）：Provider 被调用且拿到 nil 契约，产出可用 Logger。
	lg, _, err := src.bootstrapLogger(context.Background())
	if err != nil || lg == nil {
		t.Fatalf("phase A: (%v, %v)", lg, err)
	}
	if contractHits != 0 {
		t.Fatalf("phase A must not pass contract, hits = %d", contractHits)
	}

	// 阶段 B：按契约重建，Provider 拿到含 logger 段的完整契约。
	if _, _, err := src.contractLogger(context.Background(), &bootstrapv1.BootstrapConfig{
		Logger: &bootstrapv1.Logger{Backends: []*bootstrapv1.Logger_Backend{{Type: "fake"}}},
	}); err != nil {
		t.Fatalf("phase B: %v", err)
	}
	if contractHits != 1 {
		t.Fatalf("contract hits = %d, want 1", contractHits)
	}

	// ② 无 logger Provider → 默认纯函数路径：阶段 A 回退可用。
	defSrc := resolveLoggerSource(&Decl{}, plan)
	lg2, _, err := defSrc.bootstrapLogger(context.Background())
	if err != nil || lg2 == nil {
		t.Fatalf("default path phase A: (%v, %v)", lg2, err)
	}
}

// 内置 slog Provider 分派（B7：原默认路径整体迁入 StandardLoggerProvider）：
// 阶段 A 回退；type=slog 直构；其余 type（含远端后端）fail-fast 教学报错
// （错误给出 logger Provider 用法），绝不静默降级。
func TestStandardLoggerProvider(t *testing.T) {
	prov := StandardLoggerProvider()
	build := func(t *testing.T, cfg *bootstrapv1.BootstrapConfig) (log.Logger, func(), error) {
		t.Helper()
		product, cleanup, err := prov.Build(context.Background(), ApplyScope{Config: cfg, Decl: &Decl{}}, Node{})
		if err != nil {
			return nil, nil, err
		}
		lg, ok := product.(log.Logger)
		if !ok {
			t.Fatalf("provider product %T, want log.Logger", product)
		}
		var fc func()
		if cleanup != nil {
			fc = func() { _ = cleanup(context.Background()) }
		}
		return lg, fc, nil
	}
	fac := func(_ context.Context, l *bootstrapv1.Logger) (log.Logger, func(), error) {
		return build(t, &bootstrapv1.BootstrapConfig{Logger: l})
	}

	// 阶段 A（l==nil）：回退默认 bslog。
	if lg, _, err := fac(context.Background(), nil); err != nil || lg == nil {
		t.Fatalf("phase A fallback: (%v, %v)", lg, err)
	}

	// type=loki 项：默认路径不查表，fail-fast 教学报错（不静默替换为 bslog）。
	_, _, err := fac(context.Background(), &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{{
			Type: "loki",
			Loki: &bootstrapv1.Logger_Loki{Endpoint: "http://127.0.0.1:1/loki/api/v1/push"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "logger provider") {
		t.Fatalf("type=loki should fail-fast with usage, got: %v", err)
	}

	// 未实现 type：同样 fail-fast 教学。
	if _, _, err := fac(context.Background(), &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{{Type: "zap"}},
	}); err == nil || !strings.Contains(err.Error(), "logger provider") {
		t.Fatalf("type=zap should fail-fast with usage, got: %v", err)
	}

	// backends 为空：fail-fast（契约至少声明一项）。
	if _, _, err := fac(context.Background(), &bootstrapv1.Logger{}); err == nil {
		t.Fatal("empty backends should fail-fast")
	}

	// backends 含非 slog 子项：fail-fast 并定位到具体项。
	_, _, err = fac(context.Background(), &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{
			{Type: "slog", Slog: &bootstrapv1.Logger_Slog{Level: "info", Format: "console", OutputPath: "stdout"}},
			{Type: "loki", Loki: &bootstrapv1.Logger_Loki{Endpoint: "http://127.0.0.1:1/loki/api/v1/push"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "backends[1]") {
		t.Fatalf("backends with non-slog item should fail-fast with index, got: %v", err)
	}

	// backends 全 slog 项：直构合并 MultiLogger。
	ml, mcleanup, err := fac(context.Background(), &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{
			{Type: "slog", Slog: &bootstrapv1.Logger_Slog{Level: "info", Format: "console", OutputPath: "stdout"}},
		},
	})
	if err != nil || ml == nil {
		t.Fatalf("all-slog backends should build MultiLogger, got (%v, %v)", ml, err)
	}
	if mcleanup != nil {
		mcleanup()
	}
	// Enabled 语义：slog(info) 启用 Info——MultiLogger 任一子启用即启用。
	if !ml.Enabled(log.LevelInfo) {
		t.Fatal("MultiLogger should enable Info via slog child")
	}
}

// TestStandardLoggerProvider_FilterKeys 全局脱敏在内置路径生效：type=slog 特例
// 与 backends 直构路径出口包 FilterLogger（filter_keys 与 deco 可叠加）。
func TestStandardLoggerProvider_FilterKeys(t *testing.T) {
	prov := StandardLoggerProvider()
	build := func(cfg *bootstrapv1.BootstrapConfig) (log.Logger, func(), error) {
		product, cleanup, err := prov.Build(context.Background(), ApplyScope{Config: cfg, Decl: &Decl{}}, Node{})
		if err != nil {
			return nil, nil, err
		}
		lg, ok := product.(log.Logger)
		if !ok {
			t.Fatalf("provider product %T, want log.Logger", product)
		}
		var fc func()
		if cleanup != nil {
			fc = func() { _ = cleanup(context.Background()) }
		}
		return lg, fc, nil
	}
	fac := func(_ context.Context, l *bootstrapv1.Logger) (log.Logger, func(), error) {
		return build(&bootstrapv1.BootstrapConfig{Logger: l})
	}

	// 单 slog 项直构路径。
	path := filepath.Join(t.TempDir(), "filter-slog.log")
	l, cleanup, err := fac(context.Background(), &bootstrapv1.Logger{
		FilterKeys: []string{"password"},
		Backends: []*bootstrapv1.Logger_Backend{
			{Type: "slog", Slog: &bootstrapv1.Logger_Slog{Level: "info", Format: "json", OutputPath: path}},
		},
	})
	if err != nil || l == nil {
		t.Fatalf("slog item with filter_keys: (%v, %v)", l, err)
	}
	l.Info(context.Background(), "login", "password", "secret", "user", "alice")
	if cleanup != nil {
		cleanup()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), `"password":"***"`) || !strings.Contains(string(data), `"user":"alice"`) {
		t.Fatalf("单 slog 项路径脱敏应生效: %s", data)
	}

	// backends 直构路径（slog 项 + filter_keys）。
	path2 := filepath.Join(t.TempDir(), "filter-backends.log")
	ml, mcleanup, err := fac(context.Background(), &bootstrapv1.Logger{
		FilterKeys: []string{"password"},
		Backends: []*bootstrapv1.Logger_Backend{
			{Type: "slog", Slog: &bootstrapv1.Logger_Slog{Level: "info", Format: "json", OutputPath: path2}},
		},
	})
	if err != nil || ml == nil {
		t.Fatalf("backends with filter_keys: (%v, %v)", ml, err)
	}
	ml.Info(context.Background(), "login", "password", "secret")
	mcleanup()
	data2, err := os.ReadFile(path2)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data2), `"password":"***"`) {
		t.Fatalf("backends 路径脱敏应生效: %s", data2)
	}
}

// TestStandardLoggerProvider_SlogDecorators type=slog 保留装饰器语义
// （装饰经 Decl 内部字段消费——B7 取消导出后归 provider 自持的接缝）。
func TestStandardLoggerProvider_SlogDecorators(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deco.log")
	prov := StandardLoggerProvider()
	build := func(cfg *bootstrapv1.BootstrapConfig) (log.Logger, func(), error) {
		product, cleanup, err := prov.Build(context.Background(), ApplyScope{
			Config: cfg,
			Decl:   &Decl{logDecorators: []bslog.Option{bslog.WithAttrs(slog.String("deco", "yes"))}},
		}, Node{})
		if err != nil {
			return nil, nil, err
		}
		lg, ok := product.(log.Logger)
		if !ok {
			t.Fatalf("provider product %T, want log.Logger", product)
		}
		var fc func()
		if cleanup != nil {
			fc = func() { _ = cleanup(context.Background()) }
		}
		return lg, fc, nil
	}

	l, cleanup, err := build(&bootstrapv1.BootstrapConfig{Logger: &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{
			{Type: "slog", Slog: &bootstrapv1.Logger_Slog{Level: "debug", Format: "json", OutputPath: path}},
		},
	}})
	if err != nil || l == nil {
		t.Fatalf("slog item with deco: (%v, %v)", l, err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	l.Info(context.Background(), "msg")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "\"deco\":\"yes\"") {
		t.Fatalf("decorator should apply on builtin slog path: (%s, %v)", data, err)
	}
}

// countingLogSrc 计数日志源（loggerSource 测试桩）：记录每次调用的契约
// 入参（nil = 阶段 A）。
type countingLogSrc struct {
	mu   sync.Mutex
	args []*bootstrapv1.Logger
	dec  []bslog.Option
}

func (c *countingLogSrc) build(_ context.Context, l *bootstrapv1.Logger) (log.Logger, func(), error) {
	c.mu.Lock()
	c.args = append(c.args, l)
	c.mu.Unlock()
	// 用默认 Options（stdout+info）而非 LogOptions(l)：避免契约段值（如输出到文件）影响测试输出。
	return bslog.New(bslog.NewOptions(), c.dec...), nil, nil
}

func (c *countingLogSrc) bootstrapLogger(ctx context.Context) (log.Logger, func(), error) {
	return c.build(ctx, nil)
}

func (c *countingLogSrc) contractLogger(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (log.Logger, func(), error) {
	return c.build(ctx, cfg.GetLogger())
}

func (c *countingLogSrc) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.args)
}

// funcLogSrc 以工厂函数构造 loggerSource（测试桩；B7 删除 defaultLoggerFactory
// 后工厂类型不再存在，桩直接内联 func 字段）。
type funcLogSrc struct {
	fn func(ctx context.Context, l *bootstrapv1.Logger) (lg log.Logger, cleanup func(), err error)
}

func (f funcLogSrc) bootstrapLogger(ctx context.Context) (log.Logger, func(), error) {
	return f.fn(ctx, nil)
}

func (f funcLogSrc) contractLogger(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (log.Logger, func(), error) {
	return f.fn(ctx, cfg.GetLogger())
}

// runBriefly 启动 app 并在延迟后取消 ctx，等待 Run 返回（完整生命周期：BeforeStart→运行→停机回放）。
func runBriefly(t *testing.T, a *App, delay time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(delay)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
	}
}

// dynamicAddr 把契约 server 地址改为动态端口，避免测试端口冲突。
func dynamicAddr(cfg *bootstrapv1.BootstrapConfig) {
	if h := cfg.GetServer().GetHttp(); h != nil {
		h.Addr = ":0"
	}
	if g := cfg.GetServer().GetGrpc(); g != nil {
		g.Addr = ":0"
	}
}

// assembleForTest 是原 FromBootstrap(cfg, opts...) 在测试里的等价物（契约显式
// 注入）：契约由测试代码构造并直接传入内部 assemble，构造期语义与旧入口逐点
// 一致——含把契约段置 nil（server_registry/nil-段 fail-fast 用例）、构造期读
// 契约段（gateway driver 判定）这类 WithConfigLayers 表达不了的形态。只放
// 本 _test.go：公开装配入口只有 Serve（契约由其内部装载产出），不存在第二
// 公开装配路径。
func assembleForTest(t *testing.T, cfg *bootstrapv1.BootstrapConfig, opts ...Option) *App {
	t.Helper()
	a, err := assemble(newDecl(opts...), cfg, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return a
}

// assembleForTestErr 是 assembleForTest 的错误路径版本：不 Fatal，返回装配
// 错误供 fail-fast 断言（原 `FromBootstrap(...)` 的 err 检查用法）。
func assembleForTestErr(cfg *bootstrapv1.BootstrapConfig, opts ...Option) (*App, error) {
	return assemble(newDecl(opts...), cfg, nil)
}

// 元数据（id/name/version/env/stop_timeout）全部取自契约 app 段。
func TestAssemble_MetadataFromContract(t *testing.T) {
	cfg := bconf.NewBootstrap()
	cfg.App.Name = "meta-app"
	cfg.App.Version = "v9.9.9"
	cfg.App.Env = "test-env"
	cfg.App.Id = "instance-1"
	cfg.App.StopTimeout = nil // 回退默认 30s

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)))
	if a.id != "instance-1" || a.name != "meta-app" || a.version != "v9.9.9" {
		t.Fatalf("metadata not driven by contract: id=%s name=%s version=%s", a.id, a.name, a.version)
	}
	if a.cfg.env != "test-env" {
		t.Fatalf("env = %q, want test-env", a.cfg.env)
	}
	if a.stopTimeout != 30*time.Second {
		t.Fatalf("stopTimeout = %v, want default 30s", a.stopTimeout)
	}
}

// nil 契约 fail-fast。
func TestAssemble_NilConfig(t *testing.T) {
	if _, err := assembleForTestErr(nil); err == nil {
		t.Fatal("expected error for nil config")
	}
}

// 能力声明了但契约段缺失 → 构造期 fail-fast（不等到 Run）。
func TestAssemble_FailFastMissingSegments(t *testing.T) {
	cfg := bconf.NewBootstrap()
	cfg.Server.Http = nil
	if _, err := assembleForTestErr(cfg, WithHTTP(new(http.ServeMux))); err == nil {
		t.Fatal("expected error: WithHTTP declared but server.http is nil")
	}

	cfg2 := bconf.NewBootstrap()
	cfg2.Server.Grpc = nil
	if _, err := assembleForTestErr(cfg2, WithGRPC(func(*grpc.Server) {})); err == nil {
		t.Fatal("expected error: WithGRPC declared but server.grpc is nil")
	}
}

// 服务器构造数量随能力声明走：双声明=2，单声明=1，零声明=0。
//
// 构造延后到 Run 期（beforeStart 链后），故此处必须驱动 Run 才能观察
// servers——构造期断言的是「尚未构造」（servers==0），运行后才是声明数量。
func TestAssemble_ServersConstructed(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	a := assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		WithGRPC(func(*grpc.Server) {}),
		StandardServerProvider(),
	)
	// 构造期未构建（语义：延后到 Run）。
	if len(a.servers) != 0 {
		t.Fatalf("servers at construction = %d, want 0 (deferred to Run)", len(a.servers))
	}
	runBriefly(t, a, 50*time.Millisecond)
	if len(a.servers) != 2 {
		t.Fatalf("servers = %d, want 2", len(a.servers))
	}

	cfg2 := bconf.NewBootstrap()
	dynamicAddr(cfg2)
	a2 := assembleForTest(t, cfg2, WithHTTP(new(http.ServeMux)), StandardServerProvider())
	runBriefly(t, a2, 50*time.Millisecond)
	if len(a2.servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(a2.servers))
	}

	a3 := assembleForTest(t, bconf.NewBootstrap(), StandardServerProvider())
	// 零能力声明：运行前后都无 server（合法：纯后台进程）。
	runBriefly(t, a3, 30*time.Millisecond)
	if len(a3.servers) != 0 {
		t.Fatalf("servers = %d, want 0", len(a3.servers))
	}
}

// gateway 能力声明的 driver 语义：网关是 server.http 段的一种模式
// （契约 server.http.driver 选择），不是独立服务器。
func TestAssemble_GatewayDriver(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	gwRegister := func(context.Context, *grpc.ClientConn) (http.Handler, error) {
		return new(http.ServeMux), nil
	}

	// ① gateway 能力声明但 server.http 段缺失 → 构造期 fail-fast。
	cfgNoHttp := bconf.NewBootstrap()
	cfgNoHttp.Server.Http = nil
	if _, err := assembleForTestErr(cfgNoHttp, WithGatewayRegister(gwRegister)); err == nil {
		t.Fatal("expected error: WithGatewayRegister declared but server.http is nil")
	}

	// ② 单声明 + driver 留空 → 默认转码面（GatewayServer 承载 server.http 段）。
	//    构造延后到 Run，故驱动 Run 后观察 server 类型。
	cfg2 := bconf.NewBootstrap()
	dynamicAddr(cfg2)
	a := assembleForTest(t, cfg2, WithGatewayRegister(gwRegister), StandardServerProvider())
	runBriefly(t, a, 50*time.Millisecond)
	if len(a.servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(a.servers))
	}
	if _, ok := a.servers[0].(*gateway.GatewayServer); !ok {
		t.Fatalf("server type = %T, want *gateway.GatewayServer", a.servers[0])
	}

	// ③ 双能力声明 + driver 留空 → fail-fast：两个能力竞争同一端口，
	//    必须由配置显式表态（显式 > 隐式）。
	_, err := assembleForTestErr(bconf.NewBootstrap(),
		WithHTTP(new(http.ServeMux)), WithGatewayRegister(gwRegister))
	if err == nil || !strings.Contains(err.Error(), "driver") {
		t.Fatalf("expected driver fail-fast, got %v", err)
	}

	// ④ 双能力声明 + driver=grpc-gateway → 转码面生效（handler 被显式替代）。
	cfg4 := bconf.NewBootstrap()
	dynamicAddr(cfg4)
	cfg4.GetServer().GetHttp().Driver = baldbootstrap.DriverGrpcGateway
	a4 := assembleForTest(t, cfg4, WithHTTP(new(http.ServeMux)), WithGatewayRegister(gwRegister), StandardServerProvider())
	runBriefly(t, a4, 50*time.Millisecond)
	if len(a4.servers) == 0 {
		t.Fatal("servers = 0, want >=1 after Run")
	}
	if _, ok := a4.servers[0].(*gateway.GatewayServer); !ok {
		t.Fatalf("server type = %T, want *gateway.GatewayServer", a4.servers[0])
	}

	// ⑤ gateway 能力声明 + driver=其他值 → fail-fast（转码能力无消费面）。
	cfg5 := bconf.NewBootstrap()
	cfg5.GetServer().GetHttp().Driver = "gin"
	if _, err := assembleForTestErr(cfg5, WithGatewayRegister(gwRegister)); err == nil {
		t.Fatal("expected error: gateway register declared but driver not grpc-gateway")
	}
}

// 日志两阶段生命周期（logger 段 Provider 接管形态）：阶段 A（构造期，nil
// 契约）→ 阶段 B（BeforeStart，契约段）→ 停机恢复原全局 Logger。
func TestAssemble_LoggerLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	// logger Provider：记录每次调用收到的契约（nil = 阶段 A）。
	var mu sync.Mutex
	var got []*bootstrapv1.BootstrapConfig
	loggerProv := segProvider{segs: []string{"logger"}, build: func(_ context.Context, s BuildScope) (any, Cleanup, error) {
		mu.Lock()
		got = append(got, s.Config) // 构造与 BeforeStart 均主 goroutine，无需锁。
		mu.Unlock()
		return stubLogger(), nil, nil
	}}
	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)), loggerProv)

	// 阶段 A 已发生：全局句柄已是回退 bslog（非 old），Provider 拿到 nil 契约。
	if log.GetLogger() == old {
		t.Fatal("phase A should install fallback logger, global still old")
	}
	if len(got) != 1 || got[0] != nil {
		t.Fatalf("phase A calls = %d, want 1 with nil contract", len(got))
	}

	runBriefly(t, a, 30*time.Millisecond)

	// 阶段 B：BeforeStart 按契约重建，恰好一次，入参为契约终值。
	if len(got) != 2 {
		t.Fatalf("provider calls after run = %d, want 2 (phase A + phase B)", len(got))
	}
	if got[1] == nil || got[1].GetLogger().GetBackends()[0].GetSlog().GetLevel() != cfg.GetLogger().GetBackends()[0].GetSlog().GetLevel() {
		t.Fatal("phase B should receive contract logger segment")
	}

	// 停机后恢复原 Logger。
	if log.GetLogger() != old {
		t.Fatal("global logger not restored after stop")
	}
}

// config 段 Provider（构造期）：契约 Config 段经 Registry.Build 产出层参与
// 合并，cleanup 挂 Effect 停机释放；层内容（logger.level=debug）经装载写回
// 契约。（原 WithConfigRegistry 形态的 Provider 等价迁移。）
func TestAssemble_ConfigProvider(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	closed := make(chan struct{})
	reg := baldbootstrap.NewConfigRegistry()
	reg.MustRegister("fake", func(context.Context, *bootstrapv1.BootstrapConfig) (*baldconfig.Layer, func(), error) {
		l := &baldconfig.Layer{
			Name:   "fake",
			Reader: &fakeReader{data: []byte("logger:\n  backends:\n    - type: slog\n      slog:\n        level: debug\n        format: console\n        output_path: stdout\n")},
		}
		return l, func() { close(closed) }, nil
	})

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)), configTestProvider(reg))
	if len(a.cfg.layers) != 1 {
		t.Fatalf("layers = %d, want 1", len(a.cfg.layers))
	}

	runBriefly(t, a, 30*time.Millisecond)

	// fake 层内容经 Store 合并→BeforeStart 装载→写回契约。
	if got := cfg.GetLogger().GetBackends()[0].GetSlog().GetLevel(); got != "debug" {
		t.Fatalf("logger.backends[0].slog.level after load = %q, want debug (layer content not merged)", got)
	}
	select {
	case <-closed:
	default:
		t.Fatal("layer cleanup not invoked on stop")
	}
}

// 热更新：重装契约并按新 logger 段重建全局 Logger。
func TestHotReload_RebuildsLogger(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	cf := &countingLogSrc{}
	spec := &Decl{}
	// logSrc 是 resolveLoggerSource 的解析结果形态（非用户声明），不落入
	// Decl——测试以独立变量承载并按参数传递（与 assemble 内部一致）。
	logSrc := cf

	var cc atomic.Pointer[func()]
	lg, cleanup, err := logSrc.bootstrapLogger(context.Background())
	if err != nil {
		t.Fatalf("phase A: %v", err)
	}
	if cleanup == nil { // 镜像 assemble 的归一化（curCleanup 钩子恒非 nil）。
		cleanup = func() {}
	}
	log.SetLogger(lg)
	cc.Store(&cleanup)

	hotReload(NewHarness(), cfg, spec, logSrc, &cc, map[string]any{
		"logger": map[string]any{
			"backends": []any{
				map[string]any{"type": "slog", "slog": map[string]any{"level": "warn", "format": "json", "output_path": "stdout"}},
			},
		},
	})

	if got := cfg.GetLogger().GetBackends()[0].GetSlog().GetLevel(); got != "warn" {
		t.Fatalf("contract logger.backends[0].slog.level = %q, want warn", got)
	}
	if n := cf.calls(); n != 2 {
		t.Fatalf("factory calls = %d, want 2", n)
	}
	if cf.args[1].GetBackends()[0].GetSlog().GetLevel() != "warn" {
		t.Fatal("rebuild should receive updated logger segment")
	}
}

// 热更新换后端：旧后端 cleanup 必须被兑现（带缓冲后端如 loki 的尾批冲刷），
// 而非被 Store 覆盖丢弃。
func TestRebuildLogger_FlushesOldBackend(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	flushed := make(chan struct{}, 1)
	first := true
	logSrc := funcLogSrc{fn: func(context.Context, *bootstrapv1.Logger) (log.Logger, func(), error) {
		if first {
			first = false
			return stubLogger(), func() { flushed <- struct{}{} }, nil
		}
		return stubLogger(), nil, nil
	}}

	var cc atomic.Pointer[func()]
	// 阶段 A：初始后端 + cleanup 入表。
	lg, cleanup, err := logSrc.bootstrapLogger(context.Background())
	if err != nil {
		t.Fatalf("phase A: %v", err)
	}
	log.SetLogger(lg)
	cc.Store(&cleanup)

	// 阶段 B：换后端——旧 cleanup 应被同步调用。
	rebuildCfg := bconf.NewBootstrap()
	rebuildCfg.Logger = &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{{Type: "slog"}},
	}
	if err := rebuildLogger(rebuildCfg, logSrc, &cc); err != nil {
		t.Fatalf("rebuildLogger: %v", err)
	}
	select {
	case <-flushed:
	default:
		t.Fatal("old backend cleanup should be invoked on backend swap (buffered tail loss)")
	}
}

// 双服务器（HTTP+gRPC）动态端口 :0 完整 Run：Endpoint 必须解析出真实端口。
func TestAssemble_AllServersDynamicPort(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)), WithGRPC(func(*grpc.Server) {}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	eps := make([]string, 0, len(a.servers))
	for _, s := range a.servers {
		eps = append(eps, s.Endpoint())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return; endpoints = %v", eps)
	}
	for i, ep := range eps {
		if ep == "" || strings.HasSuffix(ep, ":0") {
			t.Fatalf("server[%d] endpoint not resolved: %q", i, ep)
		}
	}
}

// 坏配置热更新：降级记日志、契约保留旧值、不 panic。
func TestHotReload_BadConfigKeepsOld(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	spec := &Decl{}
	// logSrc 参数化（resolveLoggerSource 的解析结果形态，非用户声明）。
	logSrc := &countingLogSrc{}
	var cc atomic.Pointer[func()]
	lg, cleanup, err := logSrc.bootstrapLogger(context.Background())
	if err != nil {
		t.Fatalf("phase A: %v", err)
	}
	if cleanup == nil { // 镜像 assemble 的归一化（curCleanup 钩子恒非 nil）。
		cleanup = func() {}
	}
	log.SetLogger(lg)
	cc.Store(&cleanup)

	hotReload(NewHarness(), cfg, spec, logSrc, &cc, map[string]any{
		"logger": map[string]any{
			"backends": []any{
				map[string]any{"type": "slog", "slog": map[string]any{"level": "not-a-level"}},
			},
		},
	})

	// 契约保留旧 level（Unmarshal 失败即整体跳过）。
	if got := cfg.GetLogger().GetBackends()[0].GetSlog().GetLevel(); got != "info" {
		t.Fatalf("contract logger.backends[0].slog.level = %q, want info (old value kept)", got)
	}
}

// ---------------------------------------------------------------------------
// 业务透传：BeforeStart/BeforeStop 与机制声明（mech.Effect/mech.Reconcile/
// mech.Provides/mech.Requires/mech.OnKeyChange 等写入同一 Decl；本组测试经
// 未导出 *Opt 写入面驱动同一透传路径——in-package 测试不可 import mech）。
// ---------------------------------------------------------------------------

// 钩子顺序：BeforeStart 在 assemble 内部装载链之后执行（钩子里
// 契约已是配置终值）；停机时 BeforeStop 先于 Effect 回放、业务 Effect
// 逆序回放先于框架 Effect（业务资源先收）。
func TestAssemble_PassthroughOrder(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	// 配置文件提供 app.name 终值（钩子里读契约验证「装载后执行」）。
	dir := t.TempDir()
	yaml := filepath.Join(dir, "u1.yaml")
	if err := os.WriteFile(yaml, []byte("app:\n  name: u1-order\n  version: v9.9.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := bconf.NewBootstrap()
	cfg.GetServer().GetHttp().Addr = "127.0.0.1:0"

	var order []string
	var mu sync.Mutex
	record := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	var nameInHook atomic.Value
	a := assembleForTest(t, cfg,
		ConfigFile(yaml),
		WithHTTP(new(http.ServeMux)),
		BeforeStart(func(context.Context) error {
			// 契约终值验证：装载链（Unmarshal→Validate→Logger）已跑完。
			nameInHook.Store(cfg.GetApp().GetName())
			record("beforeStart")
			return nil
		}),
		effectOpt("biz:one", func(context.Context) error { record("effect:one"); return nil }),
		effectOpt("biz:two", func(context.Context) error { record("effect:two"); return nil }),
		BeforeStop(func(context.Context) error { record("beforeStop"); return nil }),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}

	if got := nameInHook.Load(); got != "u1-order" {
		t.Fatalf("beforeStart saw app.name = %v, want u1-order (hook must run after config load)", got)
	}
	mu.Lock()
	defer mu.Unlock()
	// 停机五阶段（App 既有语义）：阶段 0 Effect 逆序回放 → 阶段 1 BeforeStop。
	// 业务 Effect（biz:two/biz:one）注册在框架 Effect 之后 → 逆序回放先执行。
	want := []string{"beforeStart", "effect:two", "effect:one", "beforeStop"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// 协调器声明经透传注册：启动收敛期首次触发（Run 内 loadConfig 基线后）。
func TestAssemble_PassthroughReconcile(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	cfg.GetServer().GetHttp().Addr = "127.0.0.1:0"

	var triggered atomic.Bool
	a := assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		reconcileOpt("test.passthrough", func(context.Context, *ReconcileCtx) error {
			triggered.Store(true)
			return nil
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(400 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if !triggered.Load() {
		t.Fatal("reconciler not triggered during startup convergence")
	}
}

// Requires 声明的 S1 fail-fast 经透传生效：声明依赖但未 Provide → 启动期报错。
func TestAssemble_PassthroughCapabilityFailFast(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	cfg.GetServer().GetHttp().Addr = "127.0.0.1:0"

	a := assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		providesOpt("db"),
		requiresOpt("audit.store", "db", "cache"),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cache") {
			t.Fatalf("Run err = %v, want capability error mentioning cache", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	cancel()
}

// testDisposeComp 适配无启动逻辑的清理函数为 Component——mech.ComponentFunc
// 的包内测试替代（B5 下沉后 in-package 测试不可 import mech：导入环约束）。
type testDisposeComp struct {
	name    string
	dispose func(context.Context) error
}

func (c testDisposeComp) Name() string                { return c.name }
func (c testDisposeComp) Start(context.Context) error { return nil }
func (c testDisposeComp) Dispose(ctx context.Context) error {
	if c.dispose == nil {
		return nil
	}
	return c.dispose(ctx)
}

// componentsOpt 透传：C1 生命周期经约定装配声明（Start 于监听前、
// Dispose 于停机末段）。
func TestAssemble_PassthroughComponents(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	cfg.GetServer().GetHttp().Addr = "127.0.0.1:0"

	var started, disposed atomic.Bool
	a := assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		componentsOpt(testDisposeComp{"test.comp", func(context.Context) error {
			started.Store(true)
			return nil
		}}),
	)
	// Dispose 观测：包一层组件捕获。
	a = assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		componentsOpt(&disposeProbe{disposed: &disposed}, testDisposeComp{"test.comp2", func(context.Context) error {
			started.Store(true)
			return nil
		}}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if !started.Load() {
		t.Fatal("component not started")
	}
	if !disposed.Load() {
		t.Fatal("component not disposed")
	}
}

// disposeProbe 仅观测 Dispose 的最小组件。
type disposeProbe struct{ disposed *atomic.Bool }

func (p *disposeProbe) Name() string                { return "test.dispose-probe" }
func (p *disposeProbe) Start(context.Context) error { return nil }
func (p *disposeProbe) Dispose(context.Context) error {
	p.disposed.Store(true)
	return nil
}
