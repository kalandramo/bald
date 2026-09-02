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

	appspecv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/appspec/v1"
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

// TestGenAppSpec_TemplateFormats P12 第二步：AppSpec 方言模板渲染 + gofmt 必须成功，
// 且关键原语锚点齐备（Reconcile/R1-2、MountComponent/C1+A1、Servers/P10 bundle）。
func TestGenAppSpec_TemplateFormats(t *testing.T) {
	data := appspecData{
		Meta:             &appspecv1.AppMeta{Name: "demo", Desc: "d"},
		Server:           &appspecv1.ServerSpec{Http: true, Grpc: true},
		Capability:       &appspecv1.CapabilitySpec{Provides: []string{"api"}, Requires: []string{"db"}},
		Components:       []*appspecv1.ComponentSpec{{Kind: "heartbeat", Name: "demo.hb", ConfigPrefix: "demo"}},
		AuditBackends:    []string{"log", "store"},
		BundleNormalized: true,
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := renderAppSpec(data, path); err != nil {
		t.Fatalf("renderAppSpec: %v", err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{
		"appkit.Reconcile", "app.MountComponent", "appkit.Servers",
		"bundle.Normalized", "appkit.Provides", "reconcileAudit",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("spec template missing anchor %q", want)
		}
	}
}

// TestGenAppSpec_GeneratedCompiles P12 第二步端到端：按 AppSpec 生成的 main.go
// 在 bald module 内真实 go build 通过（http+grpc / grpc-only 两分支）。
func TestGenAppSpec_GeneratedCompiles(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}
	cases := map[string]appspecData{
		"httpgrpc": {
			Meta: &appspecv1.AppMeta{Name: "a"}, Server: &appspecv1.ServerSpec{Http: true, Grpc: true},
			Components: []*appspecv1.ComponentSpec{{Kind: "heartbeat", Name: "a.hb"}},
			AuditBackends: []string{"log"}, BundleNormalized: true,
		},
		"grpconly": {
			Meta: &appspecv1.AppMeta{Name: "b"}, Server: &appspecv1.ServerSpec{Grpc: true},
			Capability: &appspecv1.CapabilitySpec{Requires: []string{"db"}},
			AuditBackends: []string{"log"}, BundleNormalized: false,
		},
	}
	for name, data := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "main.go")
		if err := renderAppSpec(data, path); err != nil {
			t.Fatalf("%s render: %v", name, err)
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "bin"), path)
		cmd.Dir = "../../.."
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s generated main.go does not compile: %v\n%s", name, err, out)
		}
	}
}
