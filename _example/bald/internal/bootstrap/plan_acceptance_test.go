// plan_acceptance_test.go 把设计 §7.1 的参考工程口径变成可执行门：
// Opaque 预算、路由可见性、无逃逸警告、段消费完整。示例此前没有任何计划
// 断言——「参考工程在计划里是什么样」只靠人跑一遍 --plan 目测。
package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kalandramo/bald/pkg/appkit"
)

func TestPlan_ReferenceProject(t *testing.T) {
	// 契约路径相对 module 根（Options/configFileForRole 的约定），测试从包目录
	// 切到根：Options 返回的 ConfigFile 是相对路径，Compile 原样读取。
	t.Chdir(filepath.Join("..", ".."))

	opts := Options(RoleAPI)
	if len(opts) == 0 {
		t.Fatal("Options(api) returned no assembly; role wiring is broken")
	}
	p, err := appkit.Compile(context.Background(), opts...)
	if err != nil {
		t.Fatalf("Compile(api): %v", err)
	}

	// Opaque 预算（§7.1 v0.28 参考工程 ≤ 3；示例自设 2，超预算由
	// WithOpaqueBudget 产出警告——预算失守必须在验收里失败，而不是躺在
	// --plan 输出里）。
	if p.OpaqueCount > 2 {
		t.Errorf("OpaqueCount = %d, want <= 2 (std server construction + programmatic registry)", p.OpaqueCount)
	}

	// 路由可见（§7.1 冲突可见 / Q5 可见性）：greet 域的 RouteTable 必须进
	// 计划，而不是只存在于闭包里。
	routes := map[string]bool{}
	for _, r := range p.Routes {
		routes[r.Domain+" "+r.Method+" "+r.Path] = true
	}
	if !routes["greet POST /v1/greet"] {
		t.Errorf("Plan.Routes = %+v, want greet POST /v1/greet", p.Routes)
	}

	// 构建期变体（Q7）：声明恒进计划——任何构建都列出 grpcgw / nacos 两条
	// 分叉；Active 只在对应 tag 编进来时为真（同一个测试在默认与 -tags
	// grpcgw 两种构建下都成立）。
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

	// 逃逸警告为 0：域有 RouteTable（不该 custom-routes）、预算达标（不该
	// opaque-budget）、已声明变体不进 ambient（I11）。
	for _, w := range p.Warnings {
		switch w.Code {
		case "custom-routes", "opaque-budget":
			t.Errorf("unexpected %s warning: %s (%s)", w.Code, w.Message, w.Subject)
		case "ambient":
			t.Errorf("unexpected ambient warning: %s (%s)", w.Message, w.Subject)
		}
	}

	// 段覆盖（§7.1）：每个出现的段要么有消费者，要么由框架无条件消费，要么
	// 显式白名单——「无消费者」就是用户配置静默失效。
	for _, s := range p.Segments {
		if len(s.Consumers) == 0 && !s.Framework && !s.Whitelisted {
			t.Errorf("segment %q has no consumer (config would be silently ignored)", s.Path)
		}
	}

	// 计划可重复（同一声明 → 同一哈希）；置换不变性由 pkg/appkit 的单测钉住，
	// 这里只保证参考工程本身没有引入非确定性来源。
	p2, err := appkit.Compile(context.Background(), opts...)
	if err != nil {
		t.Fatalf("Compile(api) rerun: %v", err)
	}
	if p.Hash() != p2.Hash() {
		t.Errorf("plan hash unstable: %s != %s", p.Hash(), p2.Hash())
	}
}
