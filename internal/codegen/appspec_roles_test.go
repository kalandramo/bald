package codegen

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// appspec_roles_test.go 钉 Gate1 的 roles 校验块（B2）。负面用例缺一不可：
// 重名 / 引用不存在的域 / 并集缺域——没有失败用例的校验等于没写。正面用例
// 钉「roles 为空 = 单角色，零迁移」：旧 spec 一条不许被新规则误伤。

// rolesOn 在参考夹具（domains 全集 = {greet}）上挂角色声明，其余字段不动。
func rolesOn(roles ...*appspecv1.RoleSpec) *appspecv1.AppSpec {
	as := guardFixture()
	as.Meta.Roles = roles
	return as
}

// TestGate1_RoleDuplicateName 角色重名必须报错（cmd/<app>-<role>/ 目录会撞车）。
func TestGate1_RoleDuplicateName(t *testing.T) {
	as := rolesOn(
		&appspecv1.RoleSpec{Name: "api", Domains: []string{"greet"}},
		&appspecv1.RoleSpec{Name: "api", Domains: []string{"greet"}},
	)
	msg := joinErrs(validateAppSpec(as))
	for _, want := range []string{`meta.roles[1].name "api"`, "重复", "meta.roles.name"} {
		if !strings.Contains(msg, want) {
			t.Errorf("重名未被正确报告: error %q 缺少 %q\n全部错误: %s", msg, want, msg)
		}
	}
}

// TestGate1_RoleUnknownDomain 角色引用了 domains[].name 里不存在的域。
func TestGate1_RoleUnknownDomain(t *testing.T) {
	as := rolesOn(
		&appspecv1.RoleSpec{Name: "api", Domains: []string{"greet", "ghost"}},
	)
	msg := joinErrs(validateAppSpec(as))
	for _, want := range []string{`"ghost"`, "未在 domains[].name 中声明", "多余=[ghost]"} {
		if !strings.Contains(msg, want) {
			t.Errorf("引用不存在的域未被正确报告: error %q 缺少 %q\n全部错误: %s", msg, want, msg)
		}
	}
}

// TestGate1_RoleUnionMissingDomain 并集缺域：greet 声明了却没有任何角色承载。
func TestGate1_RoleUnionMissingDomain(t *testing.T) {
	as := rolesOn(
		&appspecv1.RoleSpec{Name: "api", Domains: []string{"other"}},
		&appspecv1.RoleSpec{Name: "runner", Domains: []string{"other"}},
	)
	msg := joinErrs(validateAppSpec(as))
	for _, want := range []string{"缺失=[greet]", "多余=[other]", "并集必须等于 domains 全集"} {
		if !strings.Contains(msg, want) {
			t.Errorf("并集缺域未被正确报告: error %q 缺少 %q\n全部错误: %s", msg, want, msg)
		}
	}
}

// TestGate1_RolesEmptySingleRole 正面：roles 为空 = 单角色，Gate1 一律放行
// （零迁移——旧 6 字段 / 无角色 spec 的既有行为不许被新块破坏）。
func TestGate1_RolesEmptySingleRole(t *testing.T) {
	as := guardFixture()
	if len(as.GetMeta().GetRoles()) != 0 {
		t.Fatalf("夹具必须不带 roles，实际: %v", as.GetMeta().GetRoles())
	}
	if errs := validateAppSpec(as); len(errs) != 0 {
		t.Errorf("roles 为空时不得产生任何错误，got:\n%s", joinErrs(errs))
	}
}

// TestGate1_RolesValidMultiRole 正面：合法的多角色声明不得误伤
// （api 承载 greet、runner 不承载域——并集仍等于全集）。
func TestGate1_RolesValidMultiRole(t *testing.T) {
	as := rolesOn(
		&appspecv1.RoleSpec{Name: "api", Domains: []string{"greet"}},
		&appspecv1.RoleSpec{Name: "runner", Server: &appspecv1.ServerSpec{}},
	)
	if errs := validateAppSpec(as); len(errs) != 0 {
		t.Errorf("合法 roles 必须通过 Gate1，got:\n%s", joinErrs(errs))
	}
}

// ---------------------------------------------------------------------------
// B4 · 角色扇出：机器守卫下探 / 目录树布局 / 每角色契约 / 确定性 / 编译
// ---------------------------------------------------------------------------

// fanoutFixture 扩到两域两角色：api 承载 greet 并开 HTTP 面，runner 承载
// task 且不开任何传输面。域与面都按角色切分，才能证明扇出真的分叉了。
func fanoutFixture(t *testing.T) *appspecv1.AppSpec {
	t.Helper()
	as := rolesOn(
		&appspecv1.RoleSpec{Name: "api", Domains: []string{"greet"},
			Server: &appspecv1.ServerSpec{Http: true, HttpAddr: ":18080"}},
		&appspecv1.RoleSpec{Name: "runner", Domains: []string{"task"},
			Server: &appspecv1.ServerSpec{}},
	)
	as.Domains = append(as.GetDomains(), &appspecv1.DomainSpec{
		Name: "task", ProtoPackage: "task.v1", Service: "task.v1.TaskService",
		Model: "internal/apiserver/model/task.go", GoPackage: "demo.example/app/internal/apiserver/taskv1",
	})
	if errs := validateAppSpec(as); len(errs) != 0 {
		t.Fatalf("fanout 夹具必须通过 Gate1:\n%s", joinErrs(errs))
	}
	return as
}

// renderAppTree 走完整生成流水线（归一化 → Gate2 → 每角色契约 → buildTree），
// 返回落盘项清单与全部生成物拼接文本（机器守卫的断言面）。
func renderAppTree(t *testing.T, as *appspecv1.AppSpec) ([]genFile, string) {
	t.Helper()
	bs := contractBootstrap(as)
	cfg, err := contractGate(as, bs)
	if err != nil {
		t.Fatalf("contractGate: %v", err)
	}
	view := buildTreeView(as, bs)
	cfgs, err := roleContracts(as, bs, cfg, view)
	if err != nil {
		t.Fatalf("roleContracts: %v", err)
	}
	files, err := buildTree(view, cfgs)
	if err != nil {
		t.Fatalf("buildTree: %v", err)
	}
	var sb strings.Builder
	for _, fl := range files {
		sb.Write(fl.Raw)
	}
	return files, sb.String()
}

// fileAt 从落盘项清单里取路径对应的内容（路径按 emitTree 的 Join 口径）。
func fileAt(t *testing.T, files []genFile, elems ...string) string {
	t.Helper()
	p := filepath.Join(elems...)
	for _, fl := range files {
		if fl.Path == p {
			return string(fl.Raw)
		}
	}
	t.Fatalf("落盘项缺少 %s，实际清单：%v", p, pathsOf(files))
	return ""
}

// pathsOf 是断言失败时的清单可读形式。
func pathsOf(files []genFile) []string {
	outs := make([]string, 0, len(files))
	for _, fl := range files {
		outs = append(outs, fl.Path)
	}
	return outs
}

// TestMachineGuard_RoleFieldsConsumed 让守卫真正下探 meta.roles：roles 非空
// 时 checkFieldConsumers 才会进入 repeated 元素，逐字段要求消费者登记。
// 没有本用例，B2 登记的 9 个 meta.roles.* 键等于没被验证过（拼错也无人知）。
func TestMachineGuard_RoleFieldsConsumed(t *testing.T) {
	_, rendered := renderAppTree(t, fanoutFixture(t))
	// 变体字段的消费者只在声明了 variants 时落盘：追加一份带变体的渲染，
	// 守卫才能核对 variants.* 的标记（角色夹具本身不声明变体，布局集合不变）。
	_, vrendered := renderAppTree(t, variantFixture(fanoutFixture(t)))
	if errs := checkFieldConsumers(rendered + vrendered); len(errs) != 0 {
		t.Fatalf("roles 的每个字段都必须有消费者，got:\n%s", joinErrs(errs))
	}
}

// TestMachineGuard_RoleFieldUnregisteredRed 负例：摘掉一个 roles 字段登记，
// 守卫必须报红——证明上一条正例不是「因为没下探所以恒绿」。
func TestMachineGuard_RoleFieldUnregisteredRed(t *testing.T) {
	_, rendered := renderAppTree(t, fanoutFixture(t))
	key := "bald.appspec.v1.AppSpec.meta.roles.server.http_addr"
	saved := builderConsumed[key]
	delete(builderConsumed, key)
	defer func() { builderConsumed[key] = saved }()
	msg := joinErrs(checkFieldConsumers(rendered))
	if !strings.Contains(msg, "no consumer") {
		t.Fatalf("摘除 roles 字段登记必须报 no consumer，got: %s", msg)
	}
	if !strings.Contains(msg, "meta.roles.server.http_addr") {
		t.Errorf("报错必须点名被摘除的字段，got: %s", msg)
	}
}

// TestTree_RoleFanoutLayout 钉两角色时的完整落盘集合：共享文件仍一份，
// cmd 与 configs 按角色各一份（19 项 = 8 共享 + 6 cmd + 2 config + 3 seed）。
func TestTree_RoleFanoutLayout(t *testing.T) {
	files, _ := renderAppTree(t, fanoutFixture(t))
	want := []string{
		filepath.Join("internal", "bootstrap", "assemble.gen.go"),
		filepath.Join("internal", "bootstrap", "domains.gen.go"),
		filepath.Join("internal", "bootstrap", "custom.go"),
		filepath.Join("internal", "apiserver", "domains.gen.go"),
		filepath.Join("internal", "apiserver", "layering_invariant_test.go"),
		filepath.Join("api", "buf.yaml"),
		filepath.Join("api", "buf.gen.yaml"),
		"taskfile.yml",
		filepath.Join("cmd", "demoapp-api", "main.go"),
		filepath.Join("cmd", "demoapp-api", "app", "command.go"),
		filepath.Join("cmd", "demoapp-api", "app", "custom.go"),
		filepath.Join("cmd", "demoapp-runner", "main.go"),
		filepath.Join("cmd", "demoapp-runner", "app", "command.go"),
		filepath.Join("cmd", "demoapp-runner", "app", "custom.go"),
		filepath.Join("configs", "demoapp-api.yaml"),
		filepath.Join("configs", "demoapp-runner.yaml"),
		filepath.Join("internal", "bootstrap", "seed", "seed.go"),
		filepath.Join("internal", "bootstrap", "seed", "policies.go"),
		filepath.Join("internal", "apiserver", "apiserver.go"),
	}
	if len(files) != len(want) {
		t.Fatalf("落盘项数 = %d，期望 %d\n实际: %v", len(files), len(want), pathsOf(files))
	}
	got := map[string]bool{}
	for _, fl := range files {
		got[fl.Path] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("缺少落盘项 %s", w)
		}
	}
	for _, fl := range files {
		if strings.HasPrefix(fl.Path, "cmd"+string(filepath.Separator)) &&
			fl.Path != filepath.Join("cmd", "demoapp-api", "app", "custom.go") &&
			fl.Path != filepath.Join("cmd", "demoapp-runner", "app", "custom.go") &&
			fl.Own != owned {
			t.Errorf("角色 cmd 生成物必须 owned，%s 实际归属 %v", fl.Path, fl.Own)
		}
	}
}

// TestTree_RoleFanoutDiverges 钉「扇出不是空转」：两角色的 command.go、装配
// 入口、域清单必须真的不同——只是目录名不同 = 扇出失败。
func TestTree_RoleFanoutDiverges(t *testing.T) {
	files, rendered := renderAppTree(t, fanoutFixture(t))
	apiCmd := fileAt(t, files, "cmd", "demoapp-api", "app", "command.go")
	runnerCmd := fileAt(t, files, "cmd", "demoapp-runner", "app", "command.go")
	if apiCmd == runnerCmd {
		t.Error("两角色的 command.go 逐字节相同：扇出空转")
	}
	if !strings.Contains(apiCmd, `bootstrap.Options("api")`) {
		t.Errorf("api command.go 必须按角色调 Options，got:\n%s", apiCmd)
	}
	if !strings.Contains(runnerCmd, `bootstrap.Options("runner")`) {
		t.Errorf("runner command.go 必须按角色调 Options，got:\n%s", runnerCmd)
	}
	if !strings.Contains(rendered, "func Options(role string)") {
		t.Error("多角色时 assemble.gen.go 必须生成 Options(role string)")
	}
	if !strings.Contains(rendered, "func domainOptionsForRole(role string)") {
		t.Error("多角色时 domains.gen.go 必须生成 domainOptionsForRole")
	}
	if !strings.Contains(rendered, `case "api":`) || !strings.Contains(rendered, `case "runner":`) {
		t.Errorf("Options 必须按角色出 case，got:\n%s", rendered)
	}
	dom := fileAt(t, files, "internal", "bootstrap", "domains.gen.go")
	if !strings.Contains(dom, "domainOptionsApi") || !strings.Contains(dom, "domainOptionsRunner") {
		t.Errorf("域清单必须按角色各出一份，got:\n%s", dom)
	}
	if strings.Count(dom, "greetv1.Domain,") != 1 || strings.Count(dom, "taskv1.Domain,") != 1 {
		t.Errorf("每个域只能被它的角色承载一次，got:\n%s", dom)
	}
}

// TestTree_RoleFanoutConfigs 钉 D2=(b) 的实质：runner 的契约里没有 server 段。
// serverCovered 只在段存在时才要求覆盖，段缺席即无从要求——「不装 HTTP」
// 因此不依赖任何运行期豁免。
func TestTree_RoleFanoutConfigs(t *testing.T) {
	files, _ := renderAppTree(t, fanoutFixture(t))
	apiCfg := fileAt(t, files, "configs", "demoapp-api.yaml")
	runnerCfg := fileAt(t, files, "configs", "demoapp-runner.yaml")
	if !strings.Contains(apiCfg, "server:") || !strings.Contains(apiCfg, ":18080") {
		t.Errorf("api 契约必须有 server.http 与地址，got:\n%s", apiCfg)
	}
	if strings.Contains(runnerCfg, "server:") {
		t.Errorf("runner 契约不得有 server 段，got:\n%s", runnerCfg)
	}
	if strings.Contains(runnerCfg, "runner") {
		t.Errorf("契约里不得出现角色名（角色是代码侧概念，不进契约），got:\n%s", runnerCfg)
	}
}

// TestTree_RoleFanoutDeterminism 重生成必须逐字节一致（覆盖全部角色文件）。
func TestTree_RoleFanoutDeterminism(t *testing.T) {
	first, _ := renderAppTree(t, fanoutFixture(t))
	second, _ := renderAppTree(t, fanoutFixture(t))
	if len(first) != len(second) {
		t.Fatalf("两次生成项数不同: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Path != second[i].Path {
			t.Fatalf("落盘顺序不稳定: %s vs %s", first[i].Path, second[i].Path)
		}
		if !bytes.Equal(first[i].Raw, second[i].Raw) {
			t.Errorf("%s 重生成不逐字节一致", first[i].Path)
		}
	}
}

// TestTree_RoleFanoutLineBudget 入口预算对每个角色都成立（不是只查首角色）。
func TestTree_RoleFanoutLineBudget(t *testing.T) {
	files, _ := renderAppTree(t, fanoutFixture(t))
	checked := 0
	for _, fl := range files {
		if filepath.Base(fl.Path) != "main.go" {
			continue
		}
		lines := strings.Count(strings.TrimRight(string(fl.Raw), "\n"), "\n") + 1
		if lines > 25 {
			t.Errorf("%s = %d 行，预算 25:\n%s", fl.Path, lines, fl.Raw)
		}
		checked++
	}
	if checked != 2 {
		t.Fatalf("两角色应有 2 个 main.go 受检，实际 %d", checked)
	}
}

// TestTree_RoleFanoutTaskfile 钉 run 目标按角色出，且单角色仍是 run + ./cmd/<app>
// （基础视图的 CmdName 若为空会生成 `go run ./cmd/`，实测踩过）。
func TestTree_RoleFanoutTaskfile(t *testing.T) {
	files, _ := renderAppTree(t, fanoutFixture(t))
	tf := fileAt(t, files, "taskfile.yml")
	for _, want := range []string{"run-api:", "go run ./cmd/demoapp-api", "run-runner:", "go run ./cmd/demoapp-runner"} {
		if !strings.Contains(tf, want) {
			t.Errorf("多角色 taskfile 缺少 %q，got:\n%s", want, tf)
		}
	}
	single, _ := renderAppTree(t, guardFixture())
	stf := fileAt(t, single, "taskfile.yml")
	if !strings.Contains(stf, "run:") || !strings.Contains(stf, "go run ./cmd/demoapp") {
		t.Errorf("单角色 taskfile 必须保持 run + ./cmd/<app>，got:\n%s", stf)
	}
}

// TestTree_RoleFanoutCompiles 是最强的一条：两角色的生成物在独立消费者
// module 内真实 go build 通过。夹具只声明一个域（gen domain 的 proto 夹具
// 只有 greet.v1），runner 不承载域、不开面——正是 inspectd 的进程形状。
func TestTree_RoleFanoutCompiles(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}
	as := rolesOn(
		&appspecv1.RoleSpec{Name: "api", Domains: []string{"greet"},
			Server: &appspecv1.ServerSpec{Http: true, HttpAddr: ":18080"}},
		&appspecv1.RoleSpec{Name: "runner", Server: &appspecv1.ServerSpec{}},
	)
	dir, _ := runGenAppInTemp(t, as)
	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("多角色生成物编译失败: %v\n%s", err, out)
	}
	for _, p := range []string{
		filepath.Join("cmd", "demoapp-api", "main.go"),
		filepath.Join("cmd", "demoapp-runner", "main.go"),
		filepath.Join("configs", "demoapp-runner.yaml"),
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("生成树缺少 %s: %v", p, err)
		}
	}
}
