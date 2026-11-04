// plan_acceptance_test.go 是参考工程的计划验收门：Opaque 预算、路由可见性、
// 无逃逸警告、段消费完整。
package bootstrap

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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

	// 变体声明恒进计划；Active 必须等于「按本二进制实际构建约束求值 When」。
	// 判据走 appkit 的求值口（与 go build 同源，含平台/语言隐式 tag），测试不猜
	// tag 组合，也不按变体名硬编——写死单个变体名会让第二个变体在带自己的 tag
	// 的构建里假红（nacos 变体就是这么红的）。
	//
	// 这份求值结果同时喂给下面的逃逸舱清单：registry 这一舱在不在，取决于本二进制
	// 到底有没有按契约接注册中心，与变体激活是同一个判据。
	tags := appkit.BuildTags()
	if len(p.Variants) != 2 {
		t.Fatalf("Plan.Variants = %+v, want the two declared build-tag variants", p.Variants)
	}
	variantActive := map[string]bool{}
	for _, v := range p.Variants {
		expr := v.When
		if expr == "" {
			expr = strings.Join(v.BuildTags, " && ") // BuildTags 糖：等价 && 连接
		}
		want, err := appkit.EvaluateBuildConstraint(expr)
		if err != nil {
			t.Fatalf("variant %q: When %q is not a build constraint: %v", v.Name, expr, err)
		}
		if v.Active != want {
			t.Errorf("variant %q active = %v, want %v (When=%q, build tags: %v)", v.Name, v.Active, want, expr, tags)
		}
		variantActive[v.Name] = want
	}

	// 逃逸舱按**清单**钉死，不按计数：计数放宽到 3 只是 std 的 server 节点
	// 按段拆分（http/grpc 各自可被第三方 Provider 接管），新增的舱才会变动
	// 这张表。预算与之一致（custom.go 的 WithOpaqueBudget）。
	//
	// registry 那一行只在默认构建里存在：那里 customOptions 钉了程序化 Registrar
	// 实例，registry 段被判「已消费」，装进去的东西不可分析；nacos 变体不钉，同一
	// 段改由契约派生真后端，节点就不再是舱（实测 -tags nacos 下只剩两个面）。
	var opaque []string
	for _, n := range p.Nodes {
		if n.Opaque {
			opaque = append(opaque, string(n.Key))
		}
	}
	slices.Sort(opaque)
	wantOpaque := []string{"server.grpc", "server.http"}
	if !variantActive["nacos"] {
		wantOpaque = append(wantOpaque, "registry")
	}
	slices.Sort(wantOpaque)
	if !reflect.DeepEqual(opaque, wantOpaque) {
		t.Errorf("opaque nodes = %v, want %v (new escape hatch or lost accounting)", opaque, wantOpaque)
	}

	// 「registry 段被程序化接管」必须作为警告进计划：它是契约里那份后端选择**不生效**
	// 的唯一合法解释。反过来，nacos 变体里它一出现就说明又有人把实例钉回去了——那正是
	// §7.10 那个洞的形状，所以这条按构建形态双向判，而不是只放行默认构建。
	takeover := false
	for _, w := range p.Warnings {
		if w.Code == "programmatic-takeover" && w.Subject == "registry" {
			takeover = true
		}
	}
	if want := !variantActive["nacos"]; takeover != want {
		t.Errorf("registry programmatic-takeover warning = %v, want %v (build tags: %v)", takeover, want, tags)
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
