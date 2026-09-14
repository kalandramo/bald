package appkit

// FromBootstrap 约定装配层的单测（2026-09-06）。
//
// 覆盖：元数据契约驱动、能力声明 fail-fast、服务器构造、日志两阶段生命周期
// （阶段 A 默认 → 阶段 B 契约重建 → 停机恢复）、ConfigRegistry 层装配与
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

// 日志工厂两级分发：契约注册表（WithLogRegistry）> 默认纯函数路径。
func TestResolveLoggerFactory(t *testing.T) {
	// ① 注册表：阶段 A（cfg=nil）回退默认 bslog（不触发表）；阶段 B 按 type 查表。
	hits := 0
	lr := baldbootstrap.NewLogRegistry()
	lr.MustRegister("fake", func(_ context.Context, b *bootstrapv1.Logger_Backend) (log.Logger, func(), error) {
		hits++
		if b.GetType() != "fake" {
			t.Errorf("provider got type %q, want fake", b.GetType())
		}
		return stubLogger(), nil, nil
	})
	regFac := resolveLoggerFactory(&bootstrapSpec{logRegistry: lr})

	lg, _, err := regFac(context.Background(), nil)
	if err != nil {
		t.Fatalf("phase A: %v", err)
	}
	if lg == nil {
		t.Fatal("phase A must produce a usable logger (fallback slog)")
	}
	if hits != 0 {
		t.Fatalf("phase A must not consult registry, hits = %d", hits)
	}

	if _, _, err := regFac(context.Background(), &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{{Type: "fake"}},
	}); err != nil {
		t.Fatalf("phase B: %v", err)
	}
	if hits != 1 {
		t.Fatalf("registry hits = %d, want 1", hits)
	}

	// ② 注册表未覆盖的契约 type → fail-fast（列出已注册项）。
	if _, _, err := regFac(context.Background(), &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{{Type: "nope"}},
	}); err == nil {
		t.Fatal("expected unknown logger type fail-fast")
	}

	// ③ 都不声明 → 默认纯函数路径：阶段 A 回退可用。
	defFac := resolveLoggerFactory(&bootstrapSpec{})
	lg2, _, err := defFac(context.Background(), nil)
	if err != nil || lg2 == nil {
		t.Fatalf("default path phase A: (%v, %v)", lg2, err)
	}
}

// 默认路径纯函数分派：阶段 A 回退；type=slog 直构；其余 type（含远端后端与
// 已删除的 nop）fail-fast 教学报错（错误给出 WithLogRegistry 用法），绝不静默降级。
func TestDefaultPath(t *testing.T) {
	fac := resolveLoggerFactory(&bootstrapSpec{})

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
	if err == nil || !strings.Contains(err.Error(), "WithLogRegistry") {
		t.Fatalf("type=loki should fail-fast with usage, got: %v", err)
	}

	// 未实现 type：同样 fail-fast 教学。
	if _, _, err := fac(context.Background(), &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{{Type: "zap"}},
	}); err == nil || !strings.Contains(err.Error(), "WithLogRegistry") {
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

// TestDefaultPath_FilterKeys 全局脱敏在默认路径生效：type=slog 特例
// 与 backends 直构路径出口包 FilterLogger（filter_keys 与 deco 可叠加）。
func TestDefaultPath_FilterKeys(t *testing.T) {
	fac := resolveLoggerFactory(&bootstrapSpec{})

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

// TestDefaultPath_SlogDecorators type=slog 在默认路径保留装饰器语义。
func TestDefaultPath_SlogDecorators(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deco.log")
	fac := resolveLoggerFactory(&bootstrapSpec{
		logDeco: []bslog.Option{bslog.WithAttrs(slog.String("deco", "yes"))},
	})

	l, cleanup, err := fac(context.Background(), &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{
			{Type: "slog", Slog: &bootstrapv1.Logger_Slog{Level: "debug", Format: "json", OutputPath: path}},
		},
	})
	if err != nil || l == nil {
		t.Fatalf("slog item with deco: (%v, %v)", l, err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	l.Info(context.Background(), "msg")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"deco":"yes"`) {
		t.Fatalf("decorator should apply on builtin slog path: (%s, %v)", data, err)
	}
}

// countingFactory 包一层计数工厂：记录每次调用的契约 logger 段入参。
type countingFactory struct {
	mu   sync.Mutex
	args []*bootstrapv1.Logger
	dec  []bslog.Option
}

func (c *countingFactory) factory(_ context.Context, l *bootstrapv1.Logger) (log.Logger, func(), error) {
	c.mu.Lock()
	c.args = append(c.args, l)
	c.mu.Unlock()
	// 用默认 Options（stdout+info）而非 LogOptions(l)：避免契约段值（如输出到文件）影响测试输出。
	return bslog.New(bslog.NewOptions(), c.dec...), nil, nil
}

func (c *countingFactory) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.args)
}

// runBriefly 启动 app 并在延迟后取消 ctx，等待 Run 返回（完整生命周期：BeforeStart→运行→停机回放）。
func runBriefly(t *testing.T, a *AppKit, delay time.Duration) {
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

// 元数据（id/name/version/env/stop_timeout）全部取自契约 app 段。
func TestFromBootstrap_MetadataFromContract(t *testing.T) {
	cfg := bconf.NewBootstrap()
	cfg.App.Name = "meta-app"
	cfg.App.Version = "v9.9.9"
	cfg.App.Env = "test-env"
	cfg.App.Id = "instance-1"
	cfg.App.StopTimeout = nil // 回退默认 30s

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
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
func TestFromBootstrap_NilConfig(t *testing.T) {
	if _, err := FromBootstrap(nil); err == nil {
		t.Fatal("expected error for nil config")
	}
}

// 能力声明了但契约段缺失 → 构造期 fail-fast（不等到 Run）。
func TestFromBootstrap_FailFastMissingSegments(t *testing.T) {
	cfg := bconf.NewBootstrap()
	cfg.Server.Http = nil
	if _, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux))); err == nil {
		t.Fatal("expected error: WithHTTP declared but server.http is nil")
	}

	cfg2 := bconf.NewBootstrap()
	cfg2.Server.Grpc = nil
	if _, err := FromBootstrap(cfg2, WithGRPC(func(*grpc.Server) {})); err == nil {
		t.Fatal("expected error: WithGRPC declared but server.grpc is nil")
	}
}

// 服务器构造数量随能力声明走：双声明=2，单声明=1，零声明=0。
func TestFromBootstrap_ServersConstructed(t *testing.T) {
	cfg := bconf.NewBootstrap()

	a, err := FromBootstrap(cfg,
		WithHTTP(new(http.ServeMux)),
		WithGRPC(func(*grpc.Server) {}),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	if len(a.servers) != 2 {
		t.Fatalf("servers = %d, want 2", len(a.servers))
	}

	a2, err := FromBootstrap(bconf.NewBootstrap(), WithHTTP(new(http.ServeMux)))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	if len(a2.servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(a2.servers))
	}

	a3, err := FromBootstrap(bconf.NewBootstrap())
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	if len(a3.servers) != 0 {
		t.Fatalf("servers = %d, want 0", len(a3.servers))
	}
}

// gateway 能力声明的 driver 语义：网关是 server.http 段的一种模式
// （契约 server.http.driver 选择），不是独立服务器。
func TestFromBootstrap_GatewayDriver(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	gwRegister := func(context.Context, *grpc.ClientConn) (http.Handler, error) {
		return new(http.ServeMux), nil
	}

	// ① gateway 能力声明但 server.http 段缺失 → 构造期 fail-fast。
	cfgNoHttp := bconf.NewBootstrap()
	cfgNoHttp.Server.Http = nil
	if _, err := FromBootstrap(cfgNoHttp, WithGatewayRegister(gwRegister)); err == nil {
		t.Fatal("expected error: WithGatewayRegister declared but server.http is nil")
	}

	// ② 单声明 + driver 留空 → 默认转码面（GatewayServer 承载 server.http 段）。
	a, err := FromBootstrap(bconf.NewBootstrap(), WithGatewayRegister(gwRegister))
	if err != nil {
		t.Fatalf("FromBootstrap(gateway only): %v", err)
	}
	if len(a.servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(a.servers))
	}
	if _, ok := a.servers[0].(*gateway.GatewayServer); !ok {
		t.Fatalf("server type = %T, want *gateway.GatewayServer", a.servers[0])
	}

	// ③ 双能力声明 + driver 留空 → fail-fast：两个能力竞争同一端口，
	//    必须由配置显式表态（显式 > 隐式）。
	_, err = FromBootstrap(bconf.NewBootstrap(),
		WithHTTP(new(http.ServeMux)), WithGatewayRegister(gwRegister))
	if err == nil || !strings.Contains(err.Error(), "driver") {
		t.Fatalf("expected driver fail-fast, got %v", err)
	}

	// ④ 双能力声明 + driver=grpc-gateway → 转码面生效（handler 被显式替代）。
	cfg4 := bconf.NewBootstrap()
	cfg4.GetServer().GetHttp().Driver = baldbootstrap.DriverGrpcGateway
	a4, err := FromBootstrap(cfg4, WithHTTP(new(http.ServeMux)), WithGatewayRegister(gwRegister))
	if err != nil {
		t.Fatalf("FromBootstrap(both + grpc-gateway): %v", err)
	}
	if _, ok := a4.servers[0].(*gateway.GatewayServer); !ok {
		t.Fatalf("server type = %T, want *gateway.GatewayServer", a4.servers[0])
	}

	// ⑤ gateway 能力声明 + driver=其他值 → fail-fast（转码能力无消费面）。
	cfg5 := bconf.NewBootstrap()
	cfg5.GetServer().GetHttp().Driver = "gin"
	if _, err := FromBootstrap(cfg5, WithGatewayRegister(gwRegister)); err == nil {
		t.Fatal("expected error: gateway register declared but driver not grpc-gateway")
	}
}

// 日志两阶段生命周期：阶段 A（构造期，nil 段）→ 阶段 B（BeforeStart，契约段）
// → 停机恢复原全局 Logger。
func TestFromBootstrap_LoggerLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	// 注册表 stub provider：记录阶段 B 查表收到的后端声明项。
	var got []*bootstrapv1.Logger_Backend
	lr := baldbootstrap.NewLogRegistry()
	lr.MustRegister("slog", func(_ context.Context, b *bootstrapv1.Logger_Backend) (log.Logger, func(), error) {
		got = append(got, b) // 构造与 BeforeStart 均主 goroutine，无需锁。
		return stubLogger(), nil, nil
	})
	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)), WithLogRegistry(lr))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}

	// 阶段 A 已发生：全局句柄已是回退 bslog（非 old），且未查表。
	if log.GetLogger() == old {
		t.Fatal("phase A should install fallback logger, global still old")
	}
	if len(got) != 0 {
		t.Fatalf("phase A must not consult registry, hits = %d", len(got))
	}

	runBriefly(t, a, 30*time.Millisecond)

	// 阶段 B：BeforeStart 按契约段查表重建，恰好一次，入参为契约终值。
	if len(got) != 1 {
		t.Fatalf("registry hits after run = %d, want 1", len(got))
	}
	if got[0] == nil || got[0].GetSlog().GetLevel() != cfg.GetLogger().GetBackends()[0].GetSlog().GetLevel() {
		t.Fatal("phase B should receive contract logger segment")
	}

	// 停机后恢复原 Logger。
	if log.GetLogger() != old {
		t.Fatal("global logger not restored after stop")
	}
}

// WithConfigRegistry：契约 Config 段经 Registry.Build 产出层参与合并，
// cleanup 挂 Effect 停机释放；层内容（logger.level=debug）经装载写回契约。
func TestFromBootstrap_ConfigRegistry(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	closed := make(chan struct{})
	reg := baldbootstrap.NewRegistry()
	reg.MustRegister("fake", func(context.Context, *bootstrapv1.BootstrapConfig) (*baldconfig.Layer, func(), error) {
		l := &baldconfig.Layer{
			Name:   "fake",
			Reader: &fakeReader{data: []byte("logger:\n  backends:\n    - type: slog\n      slog:\n        level: debug\n        format: console\n        output_path: stdout\n")},
		}
		return l, func() { close(closed) }, nil
	})

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)), WithConfigRegistry(reg))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
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
	cf := &countingFactory{}
	spec := &bootstrapSpec{logFac: cf.factory}

	var cc atomic.Pointer[func()]
	lg, cleanup, err := spec.logFac(context.Background(), nil)
	if err != nil {
		t.Fatalf("phase A: %v", err)
	}
	if cleanup == nil { // 镜像 FromBootstrap 的归一化（curCleanup 钩子恒非 nil）。
		cleanup = func() {}
	}
	log.SetLogger(lg)
	cc.Store(&cleanup)

	hotReload(cfg, spec, &cc, map[string]any{
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
	spec := &bootstrapSpec{logFac: func(context.Context, *bootstrapv1.Logger) (log.Logger, func(), error) {
		if first {
			first = false
			return stubLogger(), func() { flushed <- struct{}{} }, nil
		}
		return stubLogger(), nil, nil
	}}

	var cc atomic.Pointer[func()]
	// 阶段 A：初始后端 + cleanup 入表。
	lg, cleanup, err := spec.logFac(context.Background(), nil)
	if err != nil {
		t.Fatalf("phase A: %v", err)
	}
	log.SetLogger(lg)
	cc.Store(&cleanup)

	// 阶段 B：换后端——旧 cleanup 应被同步调用。
	if err := rebuildLogger(&bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{{Type: "slog"}},
	}, spec, &cc); err != nil {
		t.Fatalf("rebuildLogger: %v", err)
	}
	select {
	case <-flushed:
	default:
		t.Fatal("old backend cleanup should be invoked on backend swap (buffered tail loss)")
	}
}

// 双服务器（HTTP+gRPC）动态端口 :0 完整 Run：Endpoint 必须解析出真实端口。
func TestFromBootstrap_AllServersDynamicPort(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)), WithGRPC(func(*grpc.Server) {}))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}

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
	spec := &bootstrapSpec{logFac: (&countingFactory{}).factory}
	var cc atomic.Pointer[func()]
	lg, cleanup, err := spec.logFac(context.Background(), nil)
	if err != nil {
		t.Fatalf("phase A: %v", err)
	}
	if cleanup == nil { // 镜像 FromBootstrap 的归一化（curCleanup 钩子恒非 nil）。
		cleanup = func() {}
	}
	log.SetLogger(lg)
	cc.Store(&cleanup)

	hotReload(cfg, spec, &cc, map[string]any{
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
// 业务透传（U1）：WithBeforeStart/WithBeforeStop/WithEffect/WithReconcile/
// WithProvides/WithRequires/WithOnKeyChange 的转发语义。
// ---------------------------------------------------------------------------

// 钩子顺序：WithBeforeStart 在 FromBootstrap 内部装载链之后执行（钩子里
// 契约已是配置终值）；停机时 WithBeforeStop 先于 Effect 回放、业务 Effect
// 逆序回放先于框架 Effect（业务资源先收）。
func TestFromBootstrap_PassthroughOrder(t *testing.T) {
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
	a, err := FromBootstrap(cfg,
		WithConfigFile(yaml),
		WithHTTP(new(http.ServeMux)),
		WithBeforeStart(func(context.Context) error {
			// 契约终值验证：装载链（Unmarshal→Validate→Logger）已跑完。
			nameInHook.Store(cfg.GetApp().GetName())
			record("beforeStart")
			return nil
		}),
		WithEffect("biz:one", func(context.Context) error { record("effect:one"); return nil }),
		WithEffect("biz:two", func(context.Context) error { record("effect:two"); return nil }),
		WithBeforeStop(func(context.Context) error { record("beforeStop"); return nil }),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}

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
	// 停机五阶段（AppKit 既有语义）：阶段 0 Effect 逆序回放 → 阶段 1 BeforeStop。
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

// WithReconcile 声明经透传注册：启动收敛期首次触发（Run 内 loadConfig 基线后）。
func TestFromBootstrap_PassthroughReconcile(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	cfg.GetServer().GetHttp().Addr = "127.0.0.1:0"

	var triggered atomic.Bool
	a, err := FromBootstrap(cfg,
		WithHTTP(new(http.ServeMux)),
		WithReconcile("test.passthrough", func(context.Context, *ReconcileCtx) error {
			triggered.Store(true)
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}

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

// WithRequires 的 S1 fail-fast 经透传生效：声明依赖但未 Provide → 启动期报错。
func TestFromBootstrap_PassthroughCapabilityFailFast(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	cfg.GetServer().GetHttp().Addr = "127.0.0.1:0"

	a, err := FromBootstrap(cfg,
		WithHTTP(new(http.ServeMux)),
		WithProvides("db"),
		WithRequires("audit.store", "db", "cache"),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}

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

// WithComponents 透传：C1 生命周期经 FromBootstrap 声明（Start 于监听前、
// Dispose 于停机末段）。
func TestFromBootstrap_PassthroughComponents(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	cfg.GetServer().GetHttp().Addr = "127.0.0.1:0"

	var started, disposed atomic.Bool
	a, err := FromBootstrap(cfg,
		WithHTTP(new(http.ServeMux)),
		WithComponents(ComponentFunc("test.comp", func(context.Context) error {
			started.Store(true)
			return nil
		})),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	// Dispose 观测：包一层组件捕获。
	a, err = FromBootstrap(cfg,
		WithHTTP(new(http.ServeMux)),
		WithComponents(&disposeProbe{disposed: &disposed}, ComponentFunc("test.comp2", func(context.Context) error {
			started.Store(true)
			return nil
		})),
	)
	if err != nil {
		t.Fatalf("FromBootstrap (2nd): %v", err)
	}

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
