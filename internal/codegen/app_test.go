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
	// 防回归（可逆性）：Effect 的逆操作必须是**可执行调用**，不是注释空壳——
	// 此前模板把 store.UnregisterTenant 注释掉，新项目照抄即继承「全局注册点无
	// 逆操作」的漏洞（e2e 用 UndoEffects 隔离时策略泄漏到下一用例）。
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

// TestGenAppSpec_TemplateFormats 方言模板渲染 + gofmt 必须成功；批 2 目标惯用法
// 与关键原语锚点齐备（MustServe 单入口、std、Reconcile/R1-2、Components/C1、
// bundle/P10、健康检查），且装配纪律防回归（过渡形态产物必须绝迹）。
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
	if err := renderAppSpec(data, path, false, NewDefaultIOStreams()); err != nil {
		t.Fatalf("renderAppSpec: %v", err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	srcStr := string(src)
	for _, want := range []string{
		"appkit.MustServe",            // 批 2 单入口
		"std.Standard()",              // 框架自有后端
		"mech.Reconcile",              // R1-2 期望态协调
		"mech.Components",             // C1 组件
		"mech.Provides",               // S1 能力声明
		`mech.Requires("audit.store"`, // 结构化 requires：component + caps
		"reconcileAudit",              // R1-2 协调器实现体
		"mech.OnKeyChange",            // R1 热更新骨架
		"bundle.Normalized",           // P10 bundle 接线
		"appkit.WithHealth(",          // 健康检查默认装配
		"appkit.WithGRPC(",            // gRPC 能力声明
		"appkit.WithHTTP(",            // HTTP 能力声明
	} {
		if !strings.Contains(srcStr, want) {
			t.Errorf("spec template missing anchor %q", want)
		}
	}

	// ---- 装配纪律防回归（过渡形态产物绝迹）----
	// 恰好一次装配入口调用：MustServe 是唯一入口，多次出现即声明分裂。
	if n := strings.Count(srcStr, "appkit.MustServe("); n != 1 {
		t.Errorf("exactly one appkit.MustServe call expected, got %d", n)
	}
	// NewApp 是测试构造器，生成物不得走第二条装配路径（过渡形态回归哨兵）。
	if strings.Contains(srcStr, "appkit.NewApp(") {
		t.Error("spec template must not emit appkit.NewApp (single entry is MustServe)")
	}
	// Bind×N 已由引擎内化（server.*/--log.*），生成物不得再手写（过渡形态回归哨兵）。
	if strings.Contains(srcStr, "appkit.Bind(") {
		t.Error("spec template must not emit appkit.Bind (contract binds are internalized)")
	}
	// 手工服务器注册（appkit.Servers + http/grpc server 构造器）已被 std server
	// Provider 取代，出现即过渡形态回归。
	if strings.Contains(srcStr, "appkit.Servers(") || strings.Contains(srcStr, "bconf.NewBootstrap") {
		t.Error("spec template must not emit manual server assembly (appkit.Servers/bconf.NewBootstrap)")
	}
	// Run 之前不可调 MountComponent（要求运行态，Run 内才置位）——
	// 组件一律走 mech.Components Option（匹配调用形式 ".MountComponent("，注释提及不算）。
	if strings.Contains(srcStr, ".MountComponent(") {
		t.Errorf("spec template must not emit MountComponent calls (components go through mech.Components Option)")
	}
}

// TestGenAppSpec_GeneratedCompiles 方言模板端到端：按 AppSpec 生成的 main.go
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
		if err := renderAppSpec(data, path, false, NewDefaultIOStreams()); err != nil {
			t.Fatalf("%s render: %v", name, err)
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "bin"), path)
		cmd.Dir = exampleModule(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s generated main.go does not compile: %v\n%s", name, err, out)
		}
	}
}

// TestGenAppSpec_GeneratedRuns 运行时冒烟：编译并真实运行生成物（无传输 server，
// 避免端口占用），断言其在宽限期内保持存活——生成物走 MustServe 全链路
// （--config 解析、契约装载、段驱动 Provider 装配、组件与审计协调器启动）、
// Run 阻塞在信号等待。编译通过但启动即崩（装配错误/L3 段覆盖报错）会被本测试捕获。
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
	if err := renderAppSpec(data, path, false, NewDefaultIOStreams()); err != nil {
		t.Fatalf("render: %v", err)
	}
	// 提供最小配置文件（MustServe 契约装载消费；app.name 驱生命名空间——B6
	// 必填；audit.backends 驱动 std 审计装配）。
	if err := os.MkdirAll(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configs", "smoke.yaml"),
		[]byte("app:\n  name: smoke\naudit:\n  backends: log\n"), 0o644); err != nil {
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

// TestGenAppSpec_CommandOmitsName CLI 参数层（UX 修复）：spec 方言模式下 <name>
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
	cmd := genAppCmd(NewDefaultIOStreams())
	cmd.SetArgs([]string{"--spec", specPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gen app --spec without <name> should succeed: %v", err)
	}
	want := filepath.Join("cmd", "cli-demo", "main.go")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("default out %q not created: %v", want, err)
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
