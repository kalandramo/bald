// plan_acceptance_test.go 是参考工程的计划验收门：Opaque 预算、路由可见性、
// 无逃逸警告、段消费完整。
package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kalandramo/bald/pkg/appkit"
)

func TestPlan_ReferenceProject(t *testing.T) {
	t.Chdir(filepath.Join("..", ".."))

	opts := Options(RoleAPI)
	if len(opts) == 0 {
		t.Fatal("Options(api) returned no assembly; role wiring is broken")
	}
	p, err := appkit.Compile(context.Background(), opts...)
	if err != nil {
		t.Fatalf("Compile(api): %v", err)
	}

	// 示例自设预算 2：std server 构造 + 程序化 registry。
	if p.OpaqueCount > 2 {
		t.Errorf("OpaqueCount = %d, want <= 2 (std server construction + programmatic registry)", p.OpaqueCount)
	}

	// 域 RouteTable 必须进计划，而不是只存在于闭包里。示例用 gen domain 生成的
	// routes.Greet（注解派生的两个入口），清单与 BindGreet 的实际注册同源。
	routes := map[string]bool{}
	for _, r := range p.Routes {
		routes[r.Domain+" "+r.Method+" "+r.Path] = true
	}
	for _, want := range []string{"greet POST /v1/greet", "greet GET /v1/greet/:name"} {
		if !routes[want] {
			t.Errorf("Plan.Routes = %+v, want %s", p.Routes, want)
		}
	}

	// 变体声明恒进计划；Active 只在对应 tag 编进来时为真。
	tags := appkit.BuildTags()
	grpcgwOn := appkit.HasBuildTags(tags, "grpcgw")
	if len(p.Variants) != 2 {
		t.Fatalf("Plan.Variants = %+v, want the two declared build-tag variants", p.Variants)
	}
	for _, v := range p.Variants {
		if want := v.Name == "grpcgw" && grpcgwOn; v.Active != want {
			t.Errorf("variant %q active = %v, want %v (build tags: %v)", v.Name, v.Active, want, tags)
		}
	}

	for _, w := range p.Warnings {
		switch w.Code {
		case "custom-routes", "opaque-budget":
			t.Errorf("unexpected %s warning: %s (%s)", w.Code, w.Message, w.Subject)
		case "ambient":
			t.Errorf("unexpected ambient warning: %s (%s)", w.Message, w.Subject)
		}
	}

	// 每个出现的段都要有消费者（或框架无条件消费/显式白名单）。
	for _, s := range p.Segments {
		if len(s.Consumers) == 0 && !s.Framework && !s.Whitelisted {
			t.Errorf("segment %q has no consumer (config would be silently ignored)", s.Path)
		}
	}

	p2, err := appkit.Compile(context.Background(), opts...)
	if err != nil {
		t.Fatalf("Compile(api) rerun: %v", err)
	}
	if p.Hash() != p2.Hash() {
		t.Errorf("plan hash unstable: %s != %s", p.Hash(), p2.Hash())
	}
}
