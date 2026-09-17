package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/registry"
)

// stubRegistrar 记录 Register/Deregister 调用（域注册表单测用最小实现）。
type stubRegistrar struct{}

func (s *stubRegistrar) Register(_ context.Context, _ *registry.ServiceInstance) error { return nil }

func (s *stubRegistrar) Deregister(_ context.Context, _ *registry.ServiceInstance) error { return nil }

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
