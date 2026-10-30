package appkit

import (
	"context"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	"github.com/kalandramo/bald/pkg/audit"
)

// TestAssemble_ForwardsDeclMechanismFields 钉住 kitOpts 重建的逐字段透传：
// assemble 以新 Decl 重建装配声明，任何未显式转发的 Decl 字段（此处为
// AfterStop / 机制超时 / Identity / Auditor）都会在 Serve 路径上静默失效。
func TestAssemble_ForwardsDeclMechanismFields(t *testing.T) {
	aud := audit.NopAuditor()
	a := assembleForTest(t, bconf.NewBootstrap(),
		AfterStop(func(context.Context) error { return nil }),
		effectTimeoutOpt(3*time.Second),
		componentTimeoutOpt(4*time.Second),
		Identity(func(context.Context) (string, string) { return "u1", "t1" }),
		auditorOpt(aud),
	)

	if len(a.afterStop) != 1 {
		t.Fatalf("afterStop hooks = %d, want 1", len(a.afterStop))
	}
	if a.effectTimeout != 3*time.Second {
		t.Fatalf("effectTimeout = %v, want 3s", a.effectTimeout)
	}
	if a.componentTimeout != 4*time.Second {
		t.Fatalf("componentTimeout = %v, want 4s", a.componentTimeout)
	}
	if a.identity == nil {
		t.Fatal("identity port was dropped by assemble")
	}
	if sub, tenant := a.identity(context.Background()); sub != "u1" || tenant != "t1" {
		t.Fatalf("identity = (%q,%q), want (u1,t1)", sub, tenant)
	}
	if a.auditor == nil {
		t.Fatal("auditor was dropped by assemble")
	}
}
