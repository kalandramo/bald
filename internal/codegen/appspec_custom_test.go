package codegen

// appspec_custom_test.go 钉「自定义接线扩展」（设计 §7.3）：生成器不认识
// 的声明（第三方 Provider/拦截器/探针）经三个 seeded 钩子进入装配，生成物
// 只调用、从不覆盖用户文件——这正是「生成物确定性」与「手写超集」并存的
// 机制。两态验收：落盘形态（快）+ 真编译并进计划（慢，需 go）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// customWiringSource 是替换 seeded custom.go 后的用户接线：一个自定义
// Provider（计划节点 custom.probe）经 customOptions 进入装配。
const customWiringSource = `package bootstrap

import (
	"context"

	"github.com/kalandramo/bald/pkg/appkit"
)

// customProvider 是第三方 Provider 的最小形态（段驱动 + 计划节点）。
type customProvider struct{}

func (customProvider) Segments() []string { return []string{"database.sql"} }

func (customProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, customProvider{}) }

func (customProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{Key: "custom.probe", Phase: appkit.PhaseBootstrap}}, nil
}

func (customProvider) Build(context.Context, appkit.ApplyScope, appkit.Node) (any, appkit.Cleanup, error) {
	return map[string]any{"probe": struct{}{}}, nil, nil
}

var variantOpts = map[string][]appkit.Option{}

func customVariantOpts(name string) []appkit.Option { return variantOpts[name] }

// customOptions 在生成物的默认声明之后追加第三方 Provider（末者胜）。
func customOptions(role string) []appkit.Option {
	return []appkit.Option{customProvider{}}
}
`

// customPlanTestSource 断言 customOptions 的声明真的进入计划。
const customPlanTestSource = `package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kalandramo/bald/pkg/appkit"
)

func TestCustomWiring(t *testing.T) {
	t.Chdir(filepath.Join("..", ".."))
	p, err := appkit.Compile(context.Background(), Options()...)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	for _, n := range p.Nodes {
		if n.Key == "custom.probe" {
			return
		}
	}
	t.Fatalf("customOptions declaration did not reach the plan: %+v", p.Nodes)
}
`

// TestTree_CustomWiringHooks 落盘形态：三个钩子都是 seeded（生成一次、归
// 用户维护），生成物只调用它们；单角色与多角色的钩子实参按形态派生。
func TestTree_CustomWiringHooks(t *testing.T) {
	single, singleRendered := renderAppTree(t, guardFixture())
	bootstrapCustom := fileAt(t, single, "internal", "bootstrap", "custom.go")
	if !strings.Contains(bootstrapCustom, "生成一次") {
		t.Error("bootstrap/custom.go must be marked 生成一次 (seed-once)")
	}
	for _, want := range []string{
		"func customOptions(role string)",
		"var variantOpts",
		"func customVariantOpts(name string)",
	} {
		if !strings.Contains(bootstrapCustom, want) {
			t.Errorf("bootstrap/custom.go missing hook %q:\n%s", want, bootstrapCustom)
		}
	}
	appCustom := fileAt(t, single, "cmd", "demoapp", "app", "custom.go")
	if !strings.Contains(appCustom, "生成一次") || !strings.Contains(appCustom, "func customSubcommands()") {
		t.Errorf("cmd/<app>/app/custom.go must seed customSubcommands():\n%s", appCustom)
	}
	if !strings.Contains(singleRendered, `customOptions("")`) {
		t.Errorf("single-role assembly must call customOptions(\"\"):\n%s", singleRendered)
	}
	if !strings.Contains(singleRendered, "customSubcommands()") {
		t.Errorf("command.go must call customSubcommands():\n%s", singleRendered)
	}

	multi, multiRendered := renderAppTree(t, fanoutFixture(t))
	if !strings.Contains(multiRendered, "customOptions(role)") {
		t.Errorf("multi-role assembly must call customOptions(role):\n%s", multiRendered)
	}
	for _, role := range []string{"demoapp-api", "demoapp-runner"} {
		fileAt(t, multi, "cmd", role, "app", "custom.go")
	}

	withVariants, _ := renderAppTree(t, variantFixture(guardFixture()))
	variantImpl := fileAt(t, withVariants, "internal", "bootstrap", "variant_grpcgw.gen.go")
	variantsGen := fileAt(t, withVariants, "internal", "bootstrap", "variants.gen.go")
	if !strings.Contains(variantsGen, "customVariantOpts(variantImpls[i].Name)") {
		t.Errorf("variantOptions must late-bind app-level wiring through customVariantOpts:\n%s", variantsGen)
	}
	if strings.Contains(variantImpl, "customVariantOpts") {
		t.Errorf("variant impl init must not read variantOpts (init order is not a contract):\n%s", variantImpl)
	}
}

// TestTree_CustomWiringReachesPlan 端到端：把 seeded custom.go 换成用户接线
// （第三方 Provider + customOptions），生成的工程仍编译通过，且声明真的进入
// appkit.Compile 的计划——钩子不是装饰，是装配的实参。Provider 消费的
// database.sql 段由夹具契约提供（段缺失 = Provider 跳过，节点不进计划）。
func TestTree_CustomWiringReachesPlan(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}
	as := guardFixture()
	as = withDatabaseSegment(as)
	dir, _ := runGenAppInTemp(t, as)
	if err := os.WriteFile(filepath.Join(dir, "internal", "bootstrap", "custom.go"),
		[]byte(customWiringSource), 0o644); err != nil {
		t.Fatalf("write custom.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "bootstrap", "custom_wiring_test.go"),
		[]byte(customPlanTestSource), 0o644); err != nil {
		t.Fatalf("write custom plan test: %v", err)
	}
	run := exec.Command("go", "test", "./internal/bootstrap", "-run", "TestCustomWiring", "-count=1")
	run.Dir = dir
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("custom wiring did not reach the plan: %v\n%s", err, out)
	}
}

// withDatabaseSegment 给夹具契约补 database.sql 段：自定义 Provider 以该段
// 为消费路径（段驱动门要求段存在，节点才进计划）。
func withDatabaseSegment(as *appspecv1.AppSpec) *appspecv1.AppSpec {
	as.Bootstrap.Database = &bootstrapv1.Database{
		Sql: &bootstrapv1.Database_SQL{Driver: "sqlite", Source: "file::memory:"},
	}
	return as
}

// TestTree_ContractHookScaffold 钉两面脚手架的对称形状：HTTP 面的 seeded 钩子
// 是契约签名（server.http.middleware 经桥进链），gRPC 面的链在 custom.go 里
// 同点求值（server.grpc.middleware 同理），生成物登记的都是 Contract 形态。
// 「配置文件与链之间缺一次调用」正是本项修复消灭的缺陷，判据要能在接线被删时
// 变红；负面判据同样要紧：没有那条面的树不得带上钩子与相应导入（unused import
// 让生成物编译失败），转码面不挂 gin 链因此也不带 WithHTTPContract。
func TestTree_ContractHookScaffold(t *testing.T) {
	files, _ := renderAppTree(t, guardFixture()) // 单角色，仅 HTTP 面
	api := fileAt(t, files, "internal", "apiserver", "apiserver.go")
	for _, want := range []string{
		"func NewRouter(mw *bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error)",
		"bundle.FromMiddleware(mw)",
		"bundle.Public()", // 认证姿态仍写在业务看得见的那一行
	} {
		if !strings.Contains(api, want) {
			t.Errorf("apiserver.go 缺契约钩子形态 %q:\n%s", want, api)
		}
	}
	assemble := fileAt(t, files, "internal", "bootstrap", "assemble.gen.go")
	if !strings.Contains(assemble, "appkit.WithHTTPContract(apiserver.NewRouter)") {
		t.Errorf("HTTP 面必须以契约钩子登记:\n%s", assemble)
	}
	if strings.Contains(assemble, "appkit.WithHTTP(apiserver.NewRouter())") {
		t.Errorf("装配声明还停在旧形状（看不到契约段）:\n%s", assemble)
	}
	custom := fileAt(t, files, "internal", "bootstrap", "custom.go")
	for _, forbidden := range []string{"newGRPCChain", "bundle.", "google.golang.org/grpc"} {
		if strings.Contains(custom, forbidden) {
			t.Errorf("纯 HTTP 树的 custom.go 不得出现 %q:\n%s", forbidden, custom)
		}
	}

	gwSpec := guardFixture()
	gwSpec.Bootstrap.Server.Http.Driver = gatewayDriver
	gwFiles, _ := renderAppTree(t, gwSpec)
	gwAssemble := fileAt(t, gwFiles, "internal", "bootstrap", "assemble.gen.go")
	if strings.Contains(gwAssemble, "WithHTTPContract") {
		t.Errorf("转码面接管 server.http（不挂 gin 链），不得登记 WithHTTPContract:\n%s", gwAssemble)
	}

	grpcSpec := guardFixture()
	grpcSpec.Bootstrap.Server.Grpc = &bootstrapv1.Server_Grpc{Addr: ":19090"}
	grpcFiles, _ := renderAppTree(t, grpcSpec)
	grpcCustom := fileAt(t, grpcFiles, "internal", "bootstrap", "custom.go")
	for _, want := range []string{
		"func newGRPCChain(mw *bootstrapv1.Server_Grpc_Middleware) ([]grpc.ServerOption, func() error, error)",
		"bundle.FromGRPCMiddleware(mw)",
		"appkit.WithGRPCOptionsContract(newGRPCChain)",
		"bundle.Public()",
	} {
		if !strings.Contains(grpcCustom, want) {
			t.Errorf("gRPC 树的 custom.go 缺 %q:\n%s", want, grpcCustom)
		}
	}
	if strings.Contains(grpcCustom, "switch role") {
		t.Errorf("单角色树不该按角色分叉:\n%s", grpcCustom)
	}

	multiSpec := rolesOn(
		&appspecv1.RoleSpec{Name: "api", Domains: []string{"greet"},
			Server: &appspecv1.ServerSpec{Http: true, HttpAddr: ":18080", Grpc: true, GrpcAddr: ":19090"}},
		&appspecv1.RoleSpec{Name: "runner", Server: &appspecv1.ServerSpec{Grpc: true, GrpcAddr: ":19091"}},
	)
	multiFiles, _ := renderAppTree(t, multiSpec)
	multiCustom := fileAt(t, multiFiles, "internal", "bootstrap", "custom.go")
	if !strings.Contains(multiCustom, "switch role") || strings.Count(multiCustom, "WithGRPCOptionsContract(newGRPCChain)") != 2 {
		t.Errorf("多角色树必须按角色各出一条链钩子接线:\n%s", multiCustom)
	}
	if !strings.Contains(multiCustom, `case "api":`) || !strings.Contains(multiCustom, `case "runner":`) {
		t.Errorf("两个 gRPC 角色都要进 switch:\n%s", multiCustom)
	}
}
