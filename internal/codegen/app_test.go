package codegen

import (
	"bytes"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

// exampleModule 返回 _example/bald 消费者 module 的绝对路径——生成物编译/运行以它
// 作模块上下文（其 go.mod 含 cobra/gin/grpc，replace 指向核心当前工作树，闭环验证）。
// go test 的进程 cwd 恒为包目录（本包位于 <root>/internal/codegen），上溯两级即仓库根。
func exampleModule(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "_example", "bald")
}

// repoRoot 返回 bald 仓库根（go test 的进程 cwd 恒为包目录，上溯两级）。
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// TestGenApp_TemplateFormats 渲染 + go/format 必须成功（否则生成物不可编译）。
func TestGenApp_TemplateFormats(t *testing.T) {
	var buf bytes.Buffer
	tp := template.Must(template.New("app").Parse(appTmpl))
	if err := tp.Execute(&buf, map[string]string{"Name": "demo"}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, err := format.Source(buf.Bytes()); err != nil {
		t.Fatalf("generated code not gofmt-able: %v\n%s", err, buf.String())
	}
	// 关键锚点：批 2 目标惯用法 + 全部第二轮原语必须在模板中示范。
	for _, want := range []string{
		"appkit.MustServe", "std.Standard()",
		"mech.Provides", "mech.Requires", "mech.Effect",
		"mech.Components", "mech.ComponentFunc", "mech.OnKeyChange",
		"bundle.New", "bundle.Normalized",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("template missing anchor %q", want)
		}
	}
	// 防回归（可逆性）：Effect 的逆操作必须是**可执行调用**，不是注释空壳。
	if strings.Contains(buf.String(), "// store.UnregisterTenant") {
		t.Error("Effect undo is a commented-out shell; enable store.UnregisterTenant(...)")
	}
	if !strings.Contains(buf.String(), `store.UnregisterTenant("tenant_id")`) {
		t.Error(`template missing executable store.UnregisterTenant("tenant_id") in Effect undo`)
	}
}

// TestGenApp_GeneratedCodeCompiles 端到端：实际生成到临时目录并在 bald module 内
// go build 验证生成物可编译（验收标准：生成物 go build 通过）。
func TestGenApp_GeneratedCodeCompiles(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}
	dir := t.TempDir()
	var buf bytes.Buffer
	tp := template.Must(template.New("app").Parse(appTmpl))
	if err := tp.Execute(&buf, map[string]string{"Name": "demoapp"}); err != nil {
		t.Fatalf("render: %v", err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, formatted, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// 在 _example/bald 模块内编译生成物：它是 bald 核心的真实消费者模块
	// （依赖 cobra/gin/grpc，replace 指向根模块）——生成物即面向该形态的模块。
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "demoapp.bin"), path)
	cmd.Dir = exampleModule(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated main.go does not compile: %v\n%s", err, out)
	}
}

// TestGenAppSpec_CommandOmitsName CLI 参数层（UX）：spec 模式下 <name> 可省略
// ——应用名与目录树均以 AppSpec 为准（meta.name/module 现为必填，Gate1 校验）；
// 模板模式（无 --spec）缺 <name> 仍须报错。
func TestGenAppSpec_CommandOmitsName(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // 默认产出根（layout.root="."）落在临时目录
	specPath := filepath.Join(dir, "appspec.json")
	spec := `{
  "meta": {"name": "cli-demo", "module": "example.com/cli-demo", "desc": "d"},
  "auditBackends": ["log"]
}`
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	// spec 模式：不带 <name> 应成功，产出目录树（入口 cmd/cli-demo/main.go）。
	cmd := genAppCmd(NewDefaultIOStreams())
	cmd.SetArgs([]string{"--spec", specPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gen app --spec without <name> should succeed: %v", err)
	}
	for _, want := range []string{
		filepath.Join("cmd", "cli-demo", "main.go"),
		filepath.Join("cmd", "cli-demo", "app", "command.go"),
		filepath.Join("internal", "bootstrap", "assemble.gen.go"),
		filepath.Join("internal", "bootstrap", "domains.gen.go"),
		filepath.Join("internal", "apiserver", "domains.gen.go"),
		filepath.Join("configs", "cli-demo.yaml"),
		filepath.Join("api", "buf.yaml"),
		"taskfile.yml",
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("tree file %q not created: %v", want, err)
		}
	}

	// 模板模式（无 --spec）：缺 <name> 仍报错。
	cmd2 := genAppCmd(NewDefaultIOStreams())
	cmd2.SetArgs(nil)
	if err := cmd2.Execute(); err == nil {
		t.Fatalf("template mode without <name> must fail")
	} else if !strings.Contains(err.Error(), "accepts 1 arg(s)") {
		t.Fatalf("unexpected error: %v", err)
	}
}
