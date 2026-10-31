package appkit

import (
	"context"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
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
	if a.auditorValue() == nil {
		t.Fatal("auditor was dropped by assemble")
	}
}

// forwardConfigProvider 测试桩：config 段 Provider，产出一个契约源层。
type forwardConfigProvider struct{}

func (forwardConfigProvider) Apply(d *Decl) {
	d.Providers = append(d.Providers, forwardConfigProvider{})
}
func (forwardConfigProvider) Segments() []string { return []string{"config"} }

func (forwardConfigProvider) Plan(context.Context, PlanScope) ([]Node, error) {
	return []Node{{Key: "config.provider", Segments: []string{"config"}, Phase: PhaseConfigure}}, nil
}

func (forwardConfigProvider) Build(context.Context, ApplyScope, Node) (any, Cleanup, error) {
	return []baldconfig.Layer{{Name: "contract"}}, nil, nil
}

// TestAssemble_MergesUserConfigLayers 钉住显式 ConfigLayers 在 Serve 路径不被
// 丢弃：契约源层（config 段 Provider 产出）居首，显式层追加在后。
func TestAssemble_MergesUserConfigLayers(t *testing.T) {
	a := assembleForTest(t, bconf.NewBootstrap(),
		Providers(forwardConfigProvider{}),
		ConfigLayers(baldconfig.Layer{Name: "user"}),
	)
	if len(a.cfg.layers) != 2 {
		t.Fatalf("layers = %d, want 2 (contract + user)", len(a.cfg.layers))
	}
	if a.cfg.layers[0].Name != "contract" || a.cfg.layers[1].Name != "user" {
		t.Fatalf("layers = [%s %s], want [contract user]",
			a.cfg.layers[0].Name, a.cfg.layers[1].Name)
	}
}

// TestAssemble_ForwardsRemainingDeclSurfaces 是 kitOpts 重建完整性的第二组
// 钉子：钩子链、配置源声明、能力声明与严格契约白名单同样必须逐项透传——
// 漏传即 Serve 路径静默丢声明（与 OnConfigChange/AfterStop 同类缺陷）。
func TestAssemble_ForwardsRemainingDeclSurfaces(t *testing.T) {
	// 框架自身的 beforeStart（契约装载链）也在链上，用无用户钩子的基线做差。
	base := assembleForTest(t, bconf.NewBootstrap())
	a := assembleForTest(t, bconf.NewBootstrap(),
		BeforeStart(func(context.Context) error { return nil }),
		AfterStart(func(context.Context) error { return nil }),
		BeforeStop(func(context.Context) error { return nil }),
		ConfigFile("/tmp/forward.yaml"),
		WatchConfigFile(true),
		providesOpt("cap.a"),
		requiresOpt("db", "cap.a"),
		componentsOpt(planComponent{name: "c1"}),
		StrictContract("biz"),
	)

	if len(a.beforeStart) != len(base.beforeStart)+1 || len(a.afterStart) != 1 || len(a.beforeStop) != 1 {
		t.Fatalf("hooks dropped: before=%d (base %d) after=%d beforeStop=%d",
			len(a.beforeStart), len(base.beforeStart), len(a.afterStart), len(a.beforeStop))
	}
	if a.cfg.cfgFile != "/tmp/forward.yaml" || !a.cfg.watchFile {
		t.Fatalf("config source declarations dropped: file=%q watch=%v", a.cfg.cfgFile, a.cfg.watchFile)
	}
	if len(a.provides) != 1 || a.provides[0] != "cap.a" {
		t.Fatalf("provides = %v, want [cap.a]", a.provides)
	}
	if len(a.requires) != 1 || a.requires[0].component != "db" {
		t.Fatalf("requires = %v, want db->cap.a", a.requires)
	}
	if len(a.components) != 1 || a.components[0].Name() != "c1" {
		t.Fatalf("components = %v, want [c1]", a.components)
	}
	if !a.cfg.strictContract || len(a.cfg.businessSections) != 1 || a.cfg.businessSections[0] != "biz" {
		t.Fatalf("strict contract dropped: on=%v sections=%v", a.cfg.strictContract, a.cfg.businessSections)
	}
}
