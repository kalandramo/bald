package appkit

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/audit"
)

// stubAuditProvider 构造桩 Provider（记录调用）。
func stubAuditProvider(called *bool) AuditProvider {
	return func(context.Context, *bootstrapv1.Audit) (audit.Auditor, func(context.Context) error, error) {
		*called = true
		return audit.NopAuditor(), nil, nil
	}
}

// TestAuditRegistry_RegisterValidation 契约：空名/nil/重名 fail-fast。
func TestAuditRegistry_RegisterValidation(t *testing.T) {
	r := NewAuditRegistry()
	if err := r.Register("", stubAuditProvider(new(bool))); err == nil {
		t.Error("empty type should fail")
	}
	if err := r.Register("store", nil); err == nil {
		t.Error("nil provider should fail")
	}
	if err := r.Register("store", stubAuditProvider(new(bool))); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register("store", stubAuditProvider(new(bool))); err == nil {
		t.Error("duplicate register should fail")
	}
}

// TestAuditRegistry_Build 契约：段缺省不装配；type 空 fail-fast；
// log 内置；未注册 fail-fast；已注册分发命中。
func TestAuditRegistry_Build(t *testing.T) {
	r := NewAuditRegistry()

	// 段缺省：不装配。
	a, cleanup, err := r.Build(context.Background(), nil)
	if a != nil || cleanup != nil || err != nil {
		t.Fatalf("nil section: a=%v cleanup-set=%v err=%v", a, cleanup != nil, err)
	}

	// type 空：fail-fast。
	a, _, err = r.Build(context.Background(), &bootstrapv1.Audit{})
	if err == nil || a != nil {
		t.Fatalf("empty type should fail: a=%v err=%v", a, err)
	}

	// log 内置：无需注册即得 LoggerAuditor。
	a, cleanup, err = r.Build(context.Background(), &bootstrapv1.Audit{Type: "log"})
	if err != nil || a == nil || cleanup != nil {
		t.Fatalf("log builtin: a=%v cleanup-set=%v err=%v", a, cleanup != nil, err)
	}
	if _, ok := a.(audit.LoggerAuditor); !ok {
		t.Fatalf("log builtin should return LoggerAuditor, got %T", a)
	}

	// 未注册：fail-fast。
	if _, _, err := r.Build(context.Background(), &bootstrapv1.Audit{Type: "store"}); err == nil {
		t.Fatal("unregistered store should fail")
	}

	// 已注册：分发命中。
	called := false
	r.MustRegister("store", stubAuditProvider(&called))
	a, _, err = r.Build(context.Background(), &bootstrapv1.Audit{Type: "store"})
	if err != nil || a == nil || !called {
		t.Fatalf("store hit: a=%v called=%v err=%v", a, called, err)
	}
}

// TestBuildAudit 契约：段缺省 no-op；log 无 Registry 也装配；store 无
// Registry fail-fast；装配后全局被替换、prev 保留。
func TestBuildAudit(t *testing.T) {
	old := audit.GetAuditor()
	t.Cleanup(func() { audit.SetAuditor(old) })

	// 段缺省：no-op（全局不动）。
	st, err := buildAudit(&bootstrapv1.BootstrapConfig{}, &bootstrapSpec{})
	if err != nil || st != nil {
		t.Fatalf("no section: st=%v err=%v", st, err)
	}

	// type 空：fail-fast。
	cfg := &bootstrapv1.BootstrapConfig{}
	cfg.Audit = &bootstrapv1.Audit{}
	if _, err := buildAudit(cfg, &bootstrapSpec{}); err == nil {
		t.Fatal("empty type should fail")
	}

	// log：无 Registry 也装配（内置）。
	cfg.Audit = &bootstrapv1.Audit{Type: "log"}
	st, err = buildAudit(cfg, &bootstrapSpec{})
	if err != nil {
		t.Fatalf("log builtin: %v", err)
	}
	if st == nil || st.prev != old {
		t.Fatalf("log state should carry prev: %+v", st)
	}
	if _, ok := audit.GetAuditor().(audit.LoggerAuditor); !ok {
		t.Fatalf("global should be LoggerAuditor, got %T", audit.GetAuditor())
	}

	// store：无 Registry fail-fast。
	cfg.Audit = &bootstrapv1.Audit{Type: "store"}
	if _, err := buildAudit(cfg, &bootstrapSpec{}); err == nil {
		t.Fatal("store without registry should fail")
	}

	// store：有 Registry 分发命中。
	spec := &bootstrapSpec{auditRegistry: NewAuditRegistry()}
	called := false
	spec.auditRegistry.MustRegister("store", stubAuditProvider(&called))
	st, err = buildAudit(cfg, spec)
	if err != nil || st == nil || !called {
		t.Fatalf("store hit: st=%v called=%v err=%v", st, called, err)
	}
}
