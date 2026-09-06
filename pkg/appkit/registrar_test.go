package appkit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/registry"
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

// --- RegistrarRegistry：显式注册与 fail-fast ---

func TestRegistrarRegistry_FailFast(t *testing.T) {
	rr := NewRegistrarRegistry()
	rr.MustRegister("fake", func(context.Context, *bootstrapv1.Registry) (registry.Registrar, func(), error) {
		return nil, nil, nil
	})

	// 重名
	err := rr.Register("fake", func(context.Context, *bootstrapv1.Registry) (registry.Registrar, func(), error) {
		return nil, nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	// 空类型
	if err := rr.Register("", nil); err == nil {
		t.Fatal("expected empty type error")
	}
	// nil provider
	if err := rr.Register("other", nil); err == nil {
		t.Fatal("expected nil provider error")
	}
	// Build：段为 nil
	if _, _, err := rr.Build(context.Background(), nil); err == nil {
		t.Fatal("expected nil section error")
	}
	// Build：type 为空
	if _, _, err := rr.Build(context.Background(), &bootstrapv1.Registry{}); err == nil {
		t.Fatal("expected empty type error")
	}
	// Build：type 未注册（显式注册的核心收益：未 import 的后端这里报错）
	_, _, err = rr.Build(context.Background(), &bootstrapv1.Registry{Type: "nope"})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected not-registered error, got %v", err)
	}
}

// Build 按 type 单选分发到对应 provider。
func TestRegistrarRegistry_BuildDispatch(t *testing.T) {
	rr := NewRegistrarRegistry()
	want := &stubRegistrar{}
	called := false
	rr.MustRegister("fake", func(_ context.Context, cfg *bootstrapv1.Registry) (registry.Registrar, func(), error) {
		called = true
		if cfg.GetType() != "fake" {
			return nil, nil, errors.New("unexpected type")
		}
		return want, func() {}, nil
	})

	got, cleanup, err := rr.Build(context.Background(), &bootstrapv1.Registry{Type: "fake"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got != registry.Registrar(want) || !called {
		t.Fatal("provider not dispatched by type")
	}
	if cleanup == nil {
		t.Fatal("cleanup should pass through")
	}
}

// --- FromBootstrap 契约装配：全生命周期（阶段 B 构造 → start 注册 → 停机反注册 + cleanup） ---

func TestFromBootstrap_ContractRegistrarLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	stub := &stubRegistrar{}
	cleaned := 0
	rr := NewRegistrarRegistry()
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
	rr := NewRegistrarRegistry()
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
