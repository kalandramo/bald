package appkit

import (
	"context"
	"strings"
	"testing"
)

// TestResolve_Ok 满足声明：全部 Requires 被 Provides 覆盖 → 通过。
func TestResolve_Ok(t *testing.T) {
	app := New(
		Provides("db", "cache-redis"),
		Requires("audit.store", "db"),
		Requires("audit.stream", "cache-redis"),
		Requires("audit.store", "db"), // 重复 (component, cap) 幂等
	)
	if err := app.Resolve(); err != nil {
		t.Fatalf("Resolve should pass: %v", err)
	}
}

// TestResolve_MissingCapability 缺失依赖：报错须含能力名与依赖方组件名，并附已提供列表。
func TestResolve_MissingCapability(t *testing.T) {
	app := New(
		Provides("db"),
		Requires("audit.stream", "cache-redis"),
		Requires("metrics", "mq", "db"),
	)
	err := app.Resolve()
	if err == nil {
		t.Fatal("Resolve should fail with missing capabilities")
	}
	for _, want := range []string{`"cache-redis"`, "audit.stream", `"mq"`, "metrics", `provided: [db]`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

// TestResolve_AggregatedErrors 多个缺失一次列全（聚合报错，便于一次修完）。
func TestResolve_AggregatedErrors(t *testing.T) {
	app := New(
		Requires("a", "cap1"),
		Requires("b", "cap2"),
	)
	err := app.Resolve()
	if err == nil {
		t.Fatal("Resolve should fail")
	}
	if !strings.Contains(err.Error(), `"cap1"`) || !strings.Contains(err.Error(), `"cap2"`) {
		t.Errorf("both missing caps should be reported together: %v", err)
	}
}

// TestResolve_DuplicateProvides 重复提供同一能力：装配笔误，报错。
func TestResolve_DuplicateProvides(t *testing.T) {
	app := New(
		Provides("db"),
		Provides("db"),
	)
	if err := app.Resolve(); err == nil {
		t.Fatal("duplicate Provides should fail")
	} else if !strings.Contains(err.Error(), `provided more than once`) {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestResolve_EmptyNames 空能力名/空组件名：报错。
func TestResolve_EmptyNames(t *testing.T) {
	if err := New(Provides("")).Resolve(); err == nil {
		t.Error("empty capability should fail")
	}
	if err := New(Requires("", "db")).Resolve(); err == nil {
		t.Error("empty component name should fail")
	}
	if err := New(Provides("db"), Requires("c", "")).Resolve(); err == nil {
		t.Error("component requiring empty cap should fail")
	}
}

// TestRun_ResolveFailsFast 缺依赖时 Run 在启动早期失败（beforeStart 不应执行）。
func TestRun_ResolveFailsFast(t *testing.T) {
	beforeStartRan := false
	app := New(
		Requires("audit.store", "db"), // 无 Provides("db")
		BeforeStart(func(context.Context) error {
			beforeStartRan = true
			return nil
		}),
	)
	err := app.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unresolved capabilities") {
		t.Fatalf("Run should fail fast on unresolved capability, got %v", err)
	}
	if beforeStartRan {
		t.Error("beforeStart must not run when capability resolution fails")
	}
}
