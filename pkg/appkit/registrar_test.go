package appkit

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/registry"
)

// bconfNewBootstrapWithRegistry 构造带 registry 段的契约（type 指向后端）。
func bconfNewBootstrapWithRegistry(typ string) *bootstrapv1.BootstrapConfig {
	cfg := bconf.NewBootstrap()
	cfg.Registry = &bootstrapv1.Registry{Type: typ}
	return cfg
}

// stubRegistrar 记录 Register/Deregister 调用。
type stubRegistrar struct {
	mu           sync.Mutex
	registered   int
	deregistered int
}

func (s *stubRegistrar) Register(_ context.Context, _ *registry.ServiceInstance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered++
	return nil
}

func (s *stubRegistrar) Deregister(_ context.Context, _ *registry.ServiceInstance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deregistered++
	return nil
}

func (s *stubRegistrar) snapshot() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registered, s.deregistered
}

// --- 约定装配（契约驱动）：全生命周期（阶段 B 构造 → start 注册 → 停机反注册 + cleanup） ---
//
// 注：registry 段由后端 package 导出的 Provider 消费（生产侧由各注册中心
// contract 包提供）；本文件只保留 appkit 侧的生命周期编排断言。

func TestAssemble_ContractRegistrarLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	stub := &stubRegistrar{}
	cleaned := 0
	prov := stubRegistrarProvider(stub, func() { cleaned++ })

	cfg := bconfNewBootstrapWithRegistry("fake")
	dynamicAddr(cfg)

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)), prov)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	if reg, ok := a.registrar.(*stubRegistrar); !ok || reg != stub {
		t.Fatalf("registrar not built from contract: %T", a.registrar)
	}
	if n, _ := stub.snapshot(); n != 1 {
		t.Fatalf("registered = %d, want 1", n)
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

	if _, n := stub.snapshot(); n != 1 {
		t.Fatalf("deregistered = %d, want 1", n)
	}
	if cleaned != 1 {
		t.Fatalf("cleanup called %d times, want 1", cleaned)
	}
}

// 显式 Registrar 优先：契约段存在但显式实例非 nil 时不走契约装配。
func TestAssemble_ExplicitRegistrarWins(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	built := 0
	prov := stubRegistrarProvider(&stubRegistrar{}, func() { built++ })

	cfg := bconfNewBootstrapWithRegistry("fake")
	dynamicAddr(cfg)

	explicit := &stubRegistrar{}
	a := assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		Registrar(explicit),
		prov,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done

	if a.registrar != registry.Registrar(explicit) {
		t.Fatalf("explicit registrar should win, got %T", a.registrar)
	}
	if built != 0 {
		t.Fatal("contract provider should not be invoked when explicit registrar set")
	}
}

// 契约 registry 段存在但无 registry Provider → L3 启动期报错（原
// 「未接 RegistrarRegistry」fail-fast 的段驱动等价语义）。
func TestAssemble_RegistrySectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconfNewBootstrapWithRegistry("fake")
	dynamicAddr(cfg)

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)))
	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no consumer") || !strings.Contains(err.Error(), "registry") {
		t.Fatalf("expected no-consumer error for registry, got %v", err)
	}
}

// --- New 构造路径：SetRegistrar 运行期注入 ---

// BeforeStart 里按契约构造 + SetRegistrar（NewApp 路径的注册中心接线自持），
// cleanup 挂停机 Effect：断言 register 发生（SetRegistrar 在 register 窗口
// 之前即时生效）、停机 Deregister + cleanup 恰好各一次。
func TestNew_SetRegistrarLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	stub := &stubRegistrar{}
	var mu sync.Mutex
	cleaned := 0
	var cleanup func()
	// NewApp 路径的注册中心构建由应用自持（契约段驱动在此路径不参与）。
	newRegistrar := func(context.Context) (registry.Registrar, func(), error) {
		return stub, func() {
			mu.Lock()
			cleaned++
			mu.Unlock()
		}, nil
	}

	srv := newMock("setreg")
	var app *App
	app = NewApp(
		Name("setreg"),
		Servers(srv),
		effectOpt("appkit:registrar-client", func(context.Context) error {
			if cleanup != nil {
				cleanup()
			}
			return nil
		}),
		BeforeStart(func(ctx context.Context) error {
			reg, cl, err := newRegistrar(ctx)
			if err != nil {
				return err
			}
			app.SetRegistrar(reg)
			cleanup = cl
			return nil
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if err := app.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if n, _ := stub.snapshot(); n != 1 {
		t.Fatalf("registered = %d, want 1 (SetRegistrar before register window)", n)
	}
	if _, n := stub.snapshot(); n != 1 {
		t.Fatalf("deregistered = %d, want 1", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if cleaned != 1 {
		t.Fatalf("cleanup called %d times, want 1", cleaned)
	}
}
