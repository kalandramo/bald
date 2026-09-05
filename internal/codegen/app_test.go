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
	"time"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// exampleModule 返回 _example/bald 消费者 module 的绝对路径——生成物编译/运行以它
// 作模块上下文（其 go.mod 含 cobra/gin/grpc，replace 指向核心当前工作树，闭环验证）。
// go test 的进程 cwd 恒为包目录（本包位于 <root>/internal/codegen），上溯两级即仓库根。
func exampleModule(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	return filepath.Join(root, "_example", "bald")
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
	// 在 _example/bald 模块内编译生成物：它是 bald 核心的真实消费者模块
	// （依赖 cobra/gin/grpc，replace 指向根模块）——生成物即面向该形态的模块。
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "demoapp.bin"), path)
	cmd.Dir = exampleModule(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated main.go does not compile: %v\n%s", err, out)
	}
}

// TestGenAppSpec_TemplateFormats P12 第二步：AppSpec 方言模板渲染 + gofmt 必须成功，
// 关键原语锚点齐备（Reconcile/R1-2、Components/C1、Servers/P10 bundle），且装配
// 顺序正确（审查修复的防回归断言）。
func TestGenAppSpec_TemplateFormats(t *testing.T) {
	data := appspecData{
		Meta:             &appspecv1.AppMeta{Name: "demo", Desc: "d"},
		Server:           &appspecv1.ServerSpec{Http: true, Grpc: true},
		Capability:       &appspecv1.CapabilitySpec{Provides: []string{"api"}, Requires: []*appspecv1.Requirement{{Component: "audit.store", Caps: []string{"db"}}}},
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
		"appkit.Reconcile", "appkit.Components", "appkit.Servers",
		"bundle.Normalized", "appkit.Provides", "reconcileAudit",
		"appkit.OnKeyChange",            // R1 热更新骨架
		`appkit.Requires("audit.store"`, // 结构化 requires：component + caps
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("spec template missing anchor %q", want)
		}
	}

	// ---- 装配纪律防回归（P12 审查修复的两个 P0）----
	srcStr := string(src)
	// P0-1：Option 只有 New 消费——New 调用必须出现在最后一次 capOpts append 之后。
	newIdx := strings.Index(srcStr, "app := appkit.New(capOpts...)")
	lastAppend := strings.LastIndex(srcStr, "capOpts = append")
	if newIdx < 0 || lastAppend < 0 {
		t.Fatalf("template lost New/append anchors (newIdx=%d lastAppend=%d)", newIdx, lastAppend)
	}
	if newIdx < lastAppend {
		t.Errorf("appkit.New must come AFTER the last capOpts append: New@%d < append@%d", newIdx, lastAppend)
	}
	// P0-2：Run 之前不可调 MountComponent（要求运行态，Run 内才置位）——
	// 模板生成物不应出现 MountComponent 调用（组件一律走 Components Option；
	// 匹配调用形式 ".MountComponent("，注释中的提及不算）。
	if strings.Contains(srcStr, ".MountComponent(") {
		t.Errorf("spec template must not emit MountComponent calls (components go through appkit.Components Option)")
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
			Components:    []*appspecv1.ComponentSpec{{Kind: "heartbeat", Name: "a.hb"}},
			AuditBackends: []string{"log"}, BundleNormalized: true,
		},
		"grpconly": {
			Meta: &appspecv1.AppMeta{Name: "b"}, Server: &appspecv1.ServerSpec{Grpc: true},
			Capability:    &appspecv1.CapabilitySpec{Requires: []*appspecv1.Requirement{{Component: "audit.store", Caps: []string{"db"}}}},
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
		cmd.Dir = exampleModule(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s generated main.go does not compile: %v\n%s", name, err, out)
		}
	}
}

// TestGenAppSpec_GeneratedRuns P12 运行时冒烟（审查修复新增）：编译并真实运行
// 生成物（无传输 server，避免端口占用），断言其在宽限期内保持存活——即装配
// 正确（servers/组件经 Option 传入、无 Run 前 MountComponent 崩溃）、Run 阻塞在
// 信号等待。编译通过但运行即崩（两个 P0 的病状）会被本测试捕获。
func TestGenAppSpec_GeneratedRuns(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}
	data := appspecData{
		Meta:          &appspecv1.AppMeta{Name: "smoke", Desc: "smoke"},
		Server:        &appspecv1.ServerSpec{}, // 无传输：Run 阻塞在信号等待
		Components:    []*appspecv1.ComponentSpec{{Kind: "heartbeat", Name: "smoke.hb"}},
		AuditBackends: []string{"log"},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := renderAppSpec(data, path); err != nil {
		t.Fatalf("render: %v", err)
	}
	// 提供最小配置文件（ConfigFile + WatchConfigFile 均指向它）。
	if err := os.MkdirAll(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configs", "smoke.yaml"),
		[]byte("audit.backends: log\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	bin := filepath.Join(dir, "smoke.bin")
	build := exec.Command("go", "build", "-o", bin, path)
	build.Dir = exampleModule(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("generated main.go does not compile: %v\n%s", err, out)
	}

	cmd := exec.Command(bin)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start generated app: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		t.Fatalf("generated app exited prematurely (err=%v):\n%s", err, out.String())
	case <-time.After(3 * time.Second):
		// 3s 仍存活：装配正确，Run 正常阻塞在信号等待。清理并结束。
	}
	_ = cmd.Process.Kill()
	<-done
}

// TestGenAppSpec_CommandOmitsName CLI 参数层（P12 UX 修复）：spec 方言模式下 <name>
// 可省略——应用名与默认输出路径 cmd/<AppSpec.meta.name>/main.go 以 AppSpec 为准；
// 模板模式（无 --spec）缺 <name> 仍须报错。
func TestGenAppSpec_CommandOmitsName(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // 默认相对输出落在临时目录
	specPath := filepath.Join(dir, "appspec.json")
	if err := os.WriteFile(specPath, []byte(`{
  "meta": {"name": "cli-demo", "module": "example.com/demo", "desc": "d"},
  "server": {"http": true},
  "auditBackends": ["log"]
}`), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	// spec 模式：不带 <name> 应成功，默认输出 cmd/cli-demo/main.go。
	cmd := genAppCmd()
	cmd.SetArgs([]string{"--spec", specPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gen app --spec without <name> should succeed: %v", err)
	}
	want := filepath.Join("cmd", "cli-demo", "main.go")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("default out %q not created: %v", want, err)
	}

	// 模板模式（无 --spec）：缺 <name> 仍报错。
	cmd2 := genAppCmd()
	cmd2.SetArgs(nil)
	if err := cmd2.Execute(); err == nil {
		t.Fatalf("template mode without <name> must fail")
	} else if !strings.Contains(err.Error(), "accepts 1 arg(s)") {
		t.Fatalf("unexpected error: %v", err)
	}
}
