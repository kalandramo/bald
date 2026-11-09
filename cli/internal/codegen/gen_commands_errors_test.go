package codegen

// gen_commands_errors_test.go 覆盖 gen / gen proto / gen store / gen domain 的
// 命令编排与绑定派生里尚未触达的失败分支：flag 校验、空位置参数、声明读/解析/
// Gate1、proto 扫描、域包检查、布局覆盖、清理与清单写失败，以及 bindViewOf /
// protoPkgReady / moduleLocalDir / emitAPIServerSide 的门禁与派生边界。
//
// 本文件末尾按结构性不可达登记剩余零覆盖语句。

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// domainRunTree 准备一份可跑通 gen domain 的最小树：scaffold module + 域 proto
// （含 Go 类型桩）+ 声明文件。调用方按需改动声明或树再执行 Run。
func domainRunTree(t *testing.T, as *appspecv1.AppSpec) (dir, specPath string) {
	t.Helper()
	dir = t.TempDir()
	scaffoldModule(t, dir, as.GetMeta().GetModule())
	writeFixtureProto(t, dir)
	specPath = writeSpecJSON(t, dir, as)
	return dir, specPath
}

// runDomainOptions 用显式 domainOptions 执行一次 Run（不走 cobra，聚焦编排分支）。
func runDomainOptions(t *testing.T, dir, specPath string, mut func(*domainOptions)) error {
	t.Helper()
	streams, _ := testStreams()
	o := &domainOptions{Spec: specPath, OutDir: dir, ioStreams: streams}
	if err := o.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if mut != nil {
		mut(o)
	}
	return o.Run()
}

// TestGenCommand_RootHelp gen 中间节点裸跑输出帮助（不承载领域逻辑）。
func TestGenCommand_RootHelp(t *testing.T) {
	var out bytes.Buffer
	cmd := NewCommand(ioStreams{Out: &out, ErrOut: io.Discard})
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gen root help: %v", err)
	}
	if !strings.Contains(out.String(), "gen") {
		t.Errorf("root help should describe the command, got: %q", out.String())
	}
}

// TestGenProtoStore_EmptyNameRejected 空位置参数必须被校验收口（cobra 的
// ExactArgs 不限内容，"" 也是合法的一个参数）。
func TestGenProtoStore_EmptyNameRejected(t *testing.T) {
	pstreams, _ := testStreams()
	pcmd := genProtoCmd(pstreams)
	pcmd.SetArgs([]string{""})
	if err := pcmd.Execute(); err == nil || !strings.Contains(err.Error(), "proto name is required") {
		t.Fatalf("empty proto name: err = %v", err)
	}
	sstreams, _ := testStreams()
	scmd := genStoreCmd(sstreams)
	scmd.SetArgs([]string{""})
	if err := scmd.Execute(); err == nil || !strings.Contains(err.Error(), "entity name is required") {
		t.Fatalf("empty entity name: err = %v", err)
	}
}

// TestDomainOptions_CompleteDefaults 缺省值：产出根 "."、清单 internal/bootstrap、
// proto 命令 buf generate。
func TestDomainOptions_CompleteDefaults(t *testing.T) {
	o := &domainOptions{}
	if err := o.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if o.OutDir != "." {
		t.Errorf("OutDir = %q, want .", o.OutDir)
	}
	if o.Manifest != filepath.Join("internal", "bootstrap", "domains.gen.go") {
		t.Errorf("Manifest = %q", o.Manifest)
	}
	if o.ProtoCmd != defaultProtoCmd {
		t.Errorf("ProtoCmd = %q, want %q", o.ProtoCmd, defaultProtoCmd)
	}
}

// TestDomainOptions_ValidateSpecRequired --spec 是域清单的唯一真相源，必填。
func TestDomainOptions_ValidateSpecRequired(t *testing.T) {
	if err := (&domainOptions{}).Validate(); err == nil || !strings.Contains(err.Error(), "--spec") {
		t.Fatalf("missing --spec must be rejected, got: %v", err)
	}
}

// TestDomainOptions_RunSpecErrors 读 / 解析 / Gate1 三道前置门各自短路。
func TestDomainOptions_RunSpecErrors(t *testing.T) {
	dir := t.TempDir()
	badJSON := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badJSON, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write bad json: %v", err)
	}
	gate := guardFixture()
	gate.Meta.Name = ""
	gatePath := writeSpecJSON(t, dir, gate)

	cases := []struct{ name, spec, want string }{
		{"missing", filepath.Join(dir, "absent.json"), "read spec:"},
		{"bad json", badJSON, "parse AppSpec (protojson):"},
		{"gate1", gatePath, "Gate1 (spec schema):"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			streams, _ := testStreams()
			o := &domainOptions{Spec: tc.spec, OutDir: t.TempDir(), ioStreams: streams}
			if err := o.Complete(); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if err := o.Run(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got: %v", tc.want, err)
			}
		})
	}
}

// TestDomainOptions_ScanError 产出根下的 proto 有语法错误时，扫描阶段即失败
// （错误必须点名文件，不能带着半份扫描结果往下走）。
func TestDomainOptions_ScanError(t *testing.T) {
	dir := t.TempDir()
	badProto := filepath.Join(dir, "api", "proto", "bad", "v1", "bad.proto")
	if err := os.MkdirAll(filepath.Dir(badProto), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(badProto, []byte("this is not a proto"), 0o644); err != nil {
		t.Fatalf("write bad proto: %v", err)
	}
	specPath := writeSpecJSON(t, t.TempDir(), guardFixture())
	err := runDomainOptions(t, dir, specPath, nil)
	if err == nil || !strings.Contains(err.Error(), "scan domain protos:") {
		t.Fatalf("malformed proto must fail the scan, got: %v", err)
	}
}

// TestDomainOptions_APIServerDirOverride --apiserver-dir 覆盖布局目录名：
// apiserver 侧产物落 internal/<name>/，清单 package 仍按清单路径派生。
func TestDomainOptions_APIServerDirOverride(t *testing.T) {
	dir, specPath := domainRunTree(t, guardFixture())
	if err := runDomainOptions(t, dir, specPath, func(o *domainOptions) {
		o.APIServerDir = "myapi"
		o.Adopt = true
	}); err != nil {
		t.Fatalf("run with --apiserver-dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "myapi", "handler", "convert", "greet.gen.go")); err != nil {
		t.Fatalf("apiserver-side output must follow the override: %v", err)
	}
}

// TestDomainOptions_DomainDirError domains[].go_package 不在本 module 下时
// 无法映射目录（域包归属不可判定），生成期报错。
func TestDomainOptions_DomainDirError(t *testing.T) {
	as := guardFixture()
	as.Domains[0].GoPackage = "other.example/elsewhere"
	dir, specPath := domainRunTree(t, as)
	err := runDomainOptions(t, dir, specPath, nil)
	if err == nil || !strings.Contains(err.Error(), "other.example/elsewhere") {
		t.Fatalf("out-of-module go_package must fail, got: %v", err)
	}
}

// TestDomainOptions_DomainPackageParseError 域包里已有 .go 但无法解析时，
// 「是否已声明 Domain」的判定失败必须上报（不能当成缺失去覆盖）。
func TestDomainOptions_DomainPackageParseError(t *testing.T) {
	dir, specPath := domainRunTree(t, guardFixture())
	pkgDir := filepath.Join(dir, "internal", "apiserver", "greetv1")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir domain package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "broken.go"), []byte("package greetv1\nfunc {"), 0o644); err != nil {
		t.Fatalf("write broken go: %v", err)
	}
	err := runDomainOptions(t, dir, specPath, nil)
	if err == nil || !strings.Contains(err.Error(), "inspect domain package") {
		t.Fatalf("unparsable domain package must fail inspection, got: %v", err)
	}
}

// TestGenDomain_PruneErrorPropagates --adopt 清理上次清单的 owned 旧产物失败
// （路径在树里是非空目录）必须上报。
func TestGenDomain_PruneErrorPropagates(t *testing.T) {
	dir, specPath := domainRunTree(t, guardFixture())
	if err := runDomainOptions(t, dir, specPath, func(o *domainOptions) { o.Adopt = true }); err != nil {
		t.Fatalf("first run: %v", err)
	}

	manifestPath := filepath.Join(dir, domainManifestName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m genManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	m.Files = append(m.Files, manifestEntry{Path: "stale", Ownership: "owned"})
	raw, err = json.Marshal(&m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "stale"), 0o755); err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale", "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stale child: %v", err)
	}

	err = runDomainOptions(t, dir, specPath, func(o *domainOptions) { o.Adopt = true })
	if err == nil || !strings.Contains(err.Error(), "remove stale generated file stale") {
		t.Fatalf("unremovable stale entry must fail loudly, got: %v", err)
	}
}

// TestGenDomain_ManifestWriteError 域清单路径被占成目录时写盘失败必须上报。
func TestGenDomain_ManifestWriteError(t *testing.T) {
	dir, specPath := domainRunTree(t, guardFixture())
	if err := os.MkdirAll(filepath.Join(dir, domainManifestName), 0o755); err != nil {
		t.Fatalf("precreate manifest dir: %v", err)
	}
	err := runDomainOptions(t, dir, specPath, nil)
	if err == nil || !strings.Contains(err.Error(), "write manifest:") {
		t.Fatalf("manifest path as directory must fail the write, got: %v", err)
	}
}

// TestEmitTree_NonGoWriteError 非 Go 生成物（owned）已存在且未 --adopt：
// writeGuarded 的拒绝必须从 emitTree 原样返回。
func TestEmitTree_NonGoWriteError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "taskfile.yml"), []byte("hand"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	streams, _ := testStreams()
	err := emitTree(root, []genFile{{Path: "taskfile.yml", Own: owned, Raw: []byte("gen")}}, false, streams)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing owned non-Go file must be refused, got: %v", err)
	}
}

// TestBindNames_ServiceSuffixFallback service 短名恰为 "Service" 时去后缀得到
// 空串，回退用短名本身（否则绑定函数名退化成裸 Bind）。
func TestBindNames_ServiceSuffixFallback(t *testing.T) {
	iface, binder := bindNames("v1.Service")
	if iface != "Service" || binder != "BindService" {
		t.Fatalf("bindNames(v1.Service) = %q, %q; want Service, BindService", iface, binder)
	}
}

// TestBindViewOf_GinPathErrorPropagates 注解路径无法无损转成 gin 路由时，
// 错误必须带域名与 RPC 名上抛。
func TestBindViewOf_GinPathErrorPropagates(t *testing.T) {
	svc := protoService{
		Package: "greet.v1", Name: "GreetService", GoPackage: "demo.example/app/api/gen/go/greet/v1;greetv1",
		Methods: []protoMethod{{Name: "Get", Verb: "get", Path: "/v1/{name=projects/*/locations/*}", Request: "GetRequest", Response: "GetResponse"}},
	}
	d := &appspecv1.DomainSpec{Name: "greet", Service: "greet.v1.GreetService"}
	_, err := bindViewOf("Greet", d, []protoService{svc})
	if err == nil || !strings.Contains(err.Error(), "multi-segment") {
		t.Fatalf("lossy gin path must propagate, got: %v", err)
	}
}

// TestBindViewOf_RejectsConflictingAlias 同一 proto Go 包被两个 service 用不同
// 别名 import 时无法合并成一条 import，生成期报错。
func TestBindViewOf_RejectsConflictingAlias(t *testing.T) {
	svcA := protoService{
		Package: "a.v1", Name: "AService", GoPackage: "demo.example/app/api/gen/go/shared/v1;sharedv1",
		Methods: []protoMethod{{Name: "Get", Verb: "get", Path: "/v1/as", Request: "GetRequest", Response: "GetResponse"}},
	}
	svcB := protoService{
		Package: "a.v1", Name: "BService", GoPackage: "demo.example/app/api/gen/go/shared/v1;otheralias",
		Methods: []protoMethod{{Name: "Get", Verb: "get", Path: "/v1/bs", Request: "GetRequest", Response: "GetResponse"}},
	}
	d := &appspecv1.DomainSpec{Name: "shared", Services: []string{"a.v1.AService", "a.v1.BService"}}
	_, err := bindViewOf("Shared", d, []protoService{svcA, svcB})
	if err == nil || !strings.Contains(err.Error(), "aliases") {
		t.Fatalf("conflicting aliases must be rejected, got: %v", err)
	}
}

// TestBindViewOf_RejectsDuplicateBinder 两个 service 去掉 Service 后缀后同名时
// 绑定函数撞名，生成期报错。
func TestBindViewOf_RejectsDuplicateBinder(t *testing.T) {
	svcA := protoService{
		Package: "a.v1", Name: "GreetService", GoPackage: "demo.example/app/api/gen/go/a/v1;av1",
		Methods: []protoMethod{{Name: "Get", Verb: "get", Path: "/v1/a", Request: "GetRequest", Response: "GetResponse"}},
	}
	svcB := protoService{
		Package: "b.v1", Name: "Greet", GoPackage: "demo.example/app/api/gen/go/b/v1;bv1",
		Methods: []protoMethod{{Name: "Get", Verb: "get", Path: "/v1/b", Request: "GetRequest", Response: "GetResponse"}},
	}
	d := &appspecv1.DomainSpec{Name: "both", Services: []string{"a.v1.GreetService", "b.v1.Greet"}}
	_, err := bindViewOf("Both", d, []protoService{svcA, svcB})
	if err == nil || !strings.Contains(err.Error(), "both derive the binding function") {
		t.Fatalf("duplicate binder must be rejected, got: %v", err)
	}
}

// TestEmitAPIServerSide_DomainServiceMissing 声明引用的 service 不在扫描结果里
// 时（Gate1 与扫描之间的第二道守卫）必须失败。
func TestEmitAPIServerSide_DomainServiceMissing(t *testing.T) {
	as := guardFixture()
	v := buildTreeView(as, contractBootstrap(as))
	streams, _ := testStreams()
	o := &domainOptions{ioStreams: streams}
	var files []genFile
	err := o.emitAPIServerSide(v, &protoScan{}, &files)
	if err == nil || !strings.Contains(err.Error(), "not found in scanned protos") {
		t.Fatalf("missing service must fail the fan-out, got: %v", err)
	}
}

// TestEmitAPIServerSide_AuthzAliasConflict 授权 object 别名歧义从扇出阶段上抛。
func TestEmitAPIServerSide_AuthzAliasConflict(t *testing.T) {
	as := &appspecv1.AppSpec{
		Meta: &appspecv1.AppMeta{Name: "demo", Module: "demo.example/app"},
		Domains: []*appspecv1.DomainSpec{
			{Name: "orders", Service: "p.v1.OrderService"},
			{Name: "items", Service: "q.v1.OrderService"},
		},
	}
	v := buildTreeView(as, contractBootstrap(as))
	scan := &protoScan{Services: []protoService{
		{Package: "p.v1", Name: "OrderService", GoPackage: "demo.example/app/api/gen/go/p/v1;pv1",
			Methods: []protoMethod{{Name: "Get", Verb: "get", Path: "/v1/orders", Request: "GetRequest", Response: "GetResponse"}}},
		{Package: "q.v1", Name: "OrderService", GoPackage: "demo.example/app/api/gen/go/q/v1;qv1",
			Methods: []protoMethod{{Name: "Get", Verb: "get", Path: "/v1/items", Request: "GetRequest", Response: "GetResponse"}}},
	}}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module other.example/m\n\nrequire github.com/kalandramo/bald/transport/web v0.0.0\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	streams, _ := testStreams()
	o := &domainOptions{OutDir: dir, ioStreams: streams}
	var files []genFile
	err := o.emitAPIServerSide(v, scan, &files)
	if err == nil || !strings.Contains(err.Error(), "must be unambiguous") {
		t.Fatalf("ambiguous authz alias must fail the fan-out, got: %v", err)
	}
}

// TestEmitAPIServerSide_SharedImportCheckedOnce 两个域引用同一 proto Go 包时，
// 依赖检查只做一次（第二次同路径直接跳过）。
func TestEmitAPIServerSide_SharedImportCheckedOnce(t *testing.T) {
	as := &appspecv1.AppSpec{
		Meta: &appspecv1.AppMeta{Name: "demo", Module: "demo.example/app"},
		Domains: []*appspecv1.DomainSpec{
			{Name: "greet", Service: "g.v1.GreetService"},
			{Name: "admin", Service: "g.v1.AdminService"},
		},
	}
	v := buildTreeView(as, contractBootstrap(as))
	scan := &protoScan{Services: []protoService{
		{Package: "g.v1", Name: "GreetService", GoPackage: "demo.example/app/api/gen/go/g/v1;gv1",
			Methods: []protoMethod{{Name: "Get", Verb: "get", Path: "/v1/greets", Request: "GetRequest", Response: "GetResponse"}}},
		{Package: "g.v1", Name: "AdminService", GoPackage: "demo.example/app/api/gen/go/g/v1;gv1",
			Methods: []protoMethod{{Name: "Get", Verb: "get", Path: "/v1/admins", Request: "GetRequest", Response: "GetResponse"}}},
	}}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module other.example/m\n\nrequire github.com/kalandramo/bald/transport/web v0.0.0\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	streams, _ := testStreams()
	o := &domainOptions{OutDir: dir, ioStreams: streams}
	var files []genFile
	if err := o.emitAPIServerSide(v, scan, &files); err != nil {
		t.Fatalf("shared import must be checked once: %v", err)
	}
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.Path] = true
	}
	for _, want := range []string{
		filepath.Join("internal", "apiserver", "handler", "gin", "greet.bind.gen.go"),
		filepath.Join("internal", "apiserver", "handler", "gin", "admin.bind.gen.go"),
	} {
		if !paths[want] {
			t.Errorf("missing generated file %s in %v", want, paths)
		}
	}
}

// TestEnsureBindDeps_ReadGoModError 产出根没有 go.mod 时无法判定依赖，报错
// 并给修法。
func TestEnsureBindDeps_ReadGoModError(t *testing.T) {
	streams, _ := testStreams()
	o := &domainOptions{OutDir: t.TempDir(), ioStreams: streams}
	err := o.ensureBindDeps("demo.example/app/api/gen/go/x/v1")
	if err == nil || !strings.Contains(err.Error(), "reading") {
		t.Fatalf("missing go.mod must fail dependency inspection, got: %v", err)
	}
}

// TestEnsureBindDeps_ProtoStageStillMissing proto 阶段执行成功却没有产出 Go
// 文件时仍是生成期硬错误（不给「生成成功、构建失败」留缝隙）。
func TestEnsureBindDeps_ProtoStageStillMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module demo.example/app\n\nrequire github.com/kalandramo/bald/transport/web v0.0.0\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	streams, _ := testStreams()
	o := &domainOptions{OutDir: dir, ProtoCmd: "true", ioStreams: streams}
	err := o.ensureBindDeps("demo.example/app/api/gen/go/greet/v1")
	if err == nil || !strings.Contains(err.Error(), "still has no generated Go files") {
		t.Fatalf("proto stage without output must fail, got: %v", err)
	}
}

// TestProtoPkgReady 包不在本 module 下归外部依赖自管（true）；module 内目录
// 没有 .go 文件则未就位（false）。
func TestProtoPkgReady(t *testing.T) {
	dir := t.TempDir()
	o := &domainOptions{OutDir: dir}
	if !o.protoPkgReady("module demo.example/app\n", "other.example/x") {
		t.Error("external package must be treated as ready")
	}
	pkgDir := filepath.Join(dir, "api", "gen", "go", "greet", "v1")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if o.protoPkgReady("module demo.example/app\n", "demo.example/app/api/gen/go/greet/v1") {
		t.Error("module-local package without .go files must be reported missing")
	}
}

// TestModuleLocalDir go_package → module 内相对目录的边界：注释行跳过、包等于
// module 根、包在 module 外、go.mod 无 module 行。
func TestModuleLocalDir(t *testing.T) {
	cases := []struct{ name, gomod, pkg, want string }{
		{"comment before module", "// demo\nmodule demo.example/app\n", "demo.example/app", "."},
		{"module root", "module demo.example/app\n", "demo.example/app", "."},
		{"subpackage", "module demo.example/app\n", "demo.example/app/x/v1", "x/v1"},
		{"outside module", "module demo.example/app\n", "other.example/x", ""},
		{"no module line", "go 1.27.1\n", "demo.example/app/x", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := moduleLocalDir(tc.gomod, tc.pkg); got != tc.want {
				t.Errorf("moduleLocalDir(%q, %q) = %q, want %q", tc.gomod, tc.pkg, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 结构性零覆盖（登记备查）：
//   - gen.go:219/292/297/330 —— 静态模板渲染与 gofmt 失败（模板是常量，
//     数据形状固定）；gen.go:240/310 Complete 恒返回 nil。
//   - gen_domain.go:156/168/181 —— 域骨架/清单模板渲染与 buildManifest 失败
//     （模板常量；spec 在 Run 开头已读过，owned 生成物均可 gofmt）。
//   - gen_domain_bind.go:170/195/220/224 —— emit 的模板渲染失败（模板常量）。
//   - gen_domain_bind.go:306/307 —— runtime.GOOS == windows 分支，darwin/linux
//     上不可达（跨平台保留的 shell 兼容）。
//   - gen_domain_proto.go:276/277 —— filepath.Rel 对「同一产出根下的绝对路径」
//     不会失败（WalkDir 的路径由根拼接而来）。
//   - gen_domain_cmd.go:48 —— domainOptions.Complete 只补默认值，恒返回 nil。
//   - appspec_validate.go:497/498 —— field-consumer 守卫的遍历根是零值
//     AppSpec，重复消息列表恒为空；非空实例下钻没有调用点。
//   - manifest.go:103 —— 清单只含字符串/切片，json.MarshalIndent 不会失败。
//   - presets.go:26 —— //go:embed 的 presets 目录是编译期常量，ReadDir 不会失败。
// ---------------------------------------------------------------------------
