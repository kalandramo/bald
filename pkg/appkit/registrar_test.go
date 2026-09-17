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
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
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

// --- FromBootstrap 契约装配：全生命周期（阶段 B 构造 → start 注册 → 停机反注册 + cleanup） ---
//
// 注：RegistrarRegistry 自身的「显式注册 + fail-fast + 单选分发」单测在
// bootstrap module（bootstrap/registrar_test.go）——2026-09-17 该注册表迁入
// bootstrap 装配层，本文件只保留 appkit 侧的生命周期编排断言。

func TestFromBootstrap_ContractRegistrarLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	stub := &stubRegistrar{}
	cleaned := 0
	rr := baldbootstrap.NewRegistrarRegistry()
	rr.MustRegister("fake", func(context.Context, *bootstrapv1.Registry) (registry.Registrar, func(), error) {
		return stub, func() { cleaned++ }, nil
	})

	cfg := bconfNewBootstrapWithRegistry("fake")
	dynamicAddr(cfg)

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)), WithRegistrarRegistry(rr))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}

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

// 显式 WithRegistrar 优先：契约段存在但显式实例非 nil 时不走契约装配。
func TestFromBootstrap_ExplicitRegistrarWins(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	built := 0
	rr := baldbootstrap.NewRegistrarRegistry()
	rr.MustRegister("fake", func(context.Context, *bootstrapv1.Registry) (registry.Registrar, func(), error) {
		built++
		return &stubRegistrar{}, func() {}, nil
	})

	cfg := bconfNewBootstrapWithRegistry("fake")
	dynamicAddr(cfg)

	explicit := &stubRegistrar{}
	a, err := FromBootstrap(cfg,
		WithHTTP(new(http.ServeMux)),
		WithRegistrar(explicit),
		WithRegistrarRegistry(rr),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}

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

// 契约 registry 段存在但未接 RegistrarRegistry → 阶段 B fail-fast。
func TestFromBootstrap_RegistrySectionWithoutTable(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconfNewBootstrapWithRegistry("fake")
	dynamicAddr(cfg)

	a, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)))
	if err != nil {
		t.Fatalf("FromBootstrap (构造期不校验合并后配置): %v", err)
	}
	err = a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "RegistrarRegistry") {
		t.Fatalf("expected RegistrarRegistry missing error, got %v", err)
	}
}

// --- New 构造路径：SetRegistrar 运行期注入（go-bald-admin T7 装配模式） ---

// BeforeStart 里按契约 Build + SetRegistrar（New 路径等价于 FromBootstrap 的
// buildRegistrar），cleanup 挂停机 Effect：断言 register 发生（SetRegistrar
// 在 register 窗口之前即时生效）、停机 Deregister + cleanup 恰好各一次。
func TestNew_SetRegistrarLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	stub := &stubRegistrar{}
	var mu sync.Mutex
	cleaned := 0
	var cleanup func()
	rr := baldbootstrap.NewRegistrarRegistry()
	rr.MustRegister("fake", func(context.Context, *bootstrapv1.Registry) (registry.Registrar, func(), error) {
		return stub, func() {
			mu.Lock()
			cleaned++
			mu.Unlock()
		}, nil
	})

	srv := newMock("setreg")
	var app *AppKit
	app = New(
		Name("setreg"),
		Servers(srv),
		Effect("appkit:registrar-client", func(context.Context) error {
			if cleanup != nil {
				cleanup()
			}
			return nil
		}),
		BeforeStart(func(ctx context.Context) error {
			reg, cl, err := rr.Build(ctx, &bootstrapv1.Registry{Type: "fake"})
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
