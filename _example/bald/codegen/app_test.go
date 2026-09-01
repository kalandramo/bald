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
	// 关键锚点：全部第二轮原语必须在模板中示范。
	for _, want := range []string{
		"appkit.Provides", "appkit.Requires", "appkit.Effect",
		"appkit.Components", "appkit.ComponentFunc", "appkit.OnKeyChange",
		"bundle.New", "bundle.Normalized", "b.GRPCChain",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("template missing anchor %q", want)
		}
	}
}

// TestGenApp_GeneratedCodeCompiles 端到端：实际生成到临时目录并在 bald module 内
// go build 验证生成物可编译（P12 验收标准：生成物 go build 通过）。
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
	// 在 bald module 内编译生成物（import 均指向核心包，bald module 即可解析）。
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "demoapp.bin"), path)
	cmd.Dir = "../../.." // bald module root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated main.go does not compile: %v\n%s", err, out)
	}
}
