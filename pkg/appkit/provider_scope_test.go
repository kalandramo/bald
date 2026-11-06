package appkit

// provider_scope_test.go —— ApplyScope.Discovery 的取用顺序与失败面：
// 阶段 B 的 App 优先于声明，声明是兜底；两者都没有或注册中心不实现发现时
// fail-fast（错误文案要能直接指向「没装配注册中心」这种配置错）。

import (
	"strings"
	"testing"

	"github.com/kalandramo/bald/registry/inmemory"
)

func TestApplyScope_Discovery(t *testing.T) {
	t.Run("none assembled", func(t *testing.T) {
		_, err := (ApplyScope{}).Discovery()
		if err == nil || !strings.Contains(err.Error(), "no registry is assembled") {
			t.Fatalf("err = %v, want no-registry failure", err)
		}
	})

	t.Run("decl fallback", func(t *testing.T) {
		reg := inmemory.New()
		got, err := (ApplyScope{Decl: &Decl{Registrar: reg}}).Discovery()
		if err != nil {
			t.Fatalf("Discovery: %v", err)
		}
		if got != reg {
			t.Fatalf("Discovery = %T(%v), want the decl registrar", got, got)
		}
	})

	t.Run("app wins over decl", func(t *testing.T) {
		appReg := inmemory.New()
		declReg := inmemory.New()
		a := NewApp(Name("acc"), Registrar(appReg))
		got, err := ApplyScope{App: a, Decl: &Decl{Registrar: declReg}}.Discovery()
		if err != nil {
			t.Fatalf("Discovery: %v", err)
		}
		if got != appReg {
			t.Fatalf("Discovery = %T(%v), want the App registrar", got, got)
		}
	})

	t.Run("registrar is not a discovery", func(t *testing.T) {
		_, err := (ApplyScope{Decl: &Decl{Registrar: &stubRegistrar{}}}).Discovery()
		if err == nil || !strings.Contains(err.Error(), "does not implement registry.Discovery") {
			t.Fatalf("err = %v, want not-a-discovery failure", err)
		}
	})
}
