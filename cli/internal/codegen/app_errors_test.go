package codegen

// app_errors_test.go 覆盖 gen app 命令编排层（app.go）的错误与边界分支：
// flag 互斥、--preset 播种路径（产出根缺省 / stat 失败 / mkdir 失败 / 写失败）、
// runGenAppSpec 的读 / 解析 / Gate1 失败，以及 emitTree / prune / 清单写的
// 错误传播。正向行为在 manifest_test.go、appspec_*_test.go 覆盖，这里只钉
// 「坏输入必须停在正确的阶段并点名原因」。
//
// 其余零覆盖语句在本文件末尾按结构性不可达登记。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// TestGenApp_ValidatePresetExclusive --spec 与 --preset 互斥：同时给出意味着
// 「读声明」和「写声明」两个相反动作，生成器无法择一，必须拒绝。
func TestGenApp_ValidatePresetExclusive(t *testing.T) {
	o := &appOptions{Spec: "appspec.json", Preset: "webserver"}
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("--spec + --preset must be rejected, got: %v", err)
	}
}

// TestGenApp_RunPresetBranch Run() 的 --preset 分支走 runPreset 而非读 spec，
// 且产出根缺省为当前目录：chdir 到空目录后裸跑 --preset 即落 appspec.json。
func TestGenApp_RunPresetBranch(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	streams, out := testStreams()
	o := &appOptions{Preset: "webserver", ioStreams: streams}
	if err := o.Validate(); err != nil {
		t.Fatalf("preset-only options must validate: %v", err)
	}
	if err := o.Run(); err != nil {
		t.Fatalf("Run --preset: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "appspec.json")); err != nil {
		t.Fatalf("preset must seed appspec.json in CWD: %v", err)
	}
	got := out.String()
	for _, want := range []string{"generated:", "preset webserver", "next:"} {
		if !strings.Contains(got, want) {
			t.Errorf("preset run output missing %q, got: %q", want, got)
		}
	}
}

// TestRunPreset_OutRootErrors 产出根的两种坏形状都必须在写盘前失败：
// 路径穿过普通文件（stat 返回 ENOTDIR，不属于「不存在」，不能当作可创建）；
// 产出根是悬空符号链接（stat 看似不存在，MkdirAll 却撞 EEXIST）。
func TestRunPreset_OutRootErrors(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	streams, _ := testStreams()
	err := runPreset("webserver", filepath.Join(blocker, "sub"), streams)
	if err == nil || !strings.Contains(err.Error(), "stat ") {
		t.Fatalf("path through a regular file must fail at stat, got: %v", err)
	}

	link := filepath.Join(base, "dangling")
	if err := os.Symlink("missing-target", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	err = runPreset("webserver", link, streams)
	if err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Fatalf("dangling symlink out root must fail at mkdir, got: %v", err)
	}
}

// TestRunPreset_WriteError 目标声明文件被占成「指向不存在目录的符号链接」：
// stat 判不存在、MkdirAll 成功（产出根真实存在），写盘才失败——错误必须
// 上报，不能静默留下半截状态。
func TestRunPreset_WriteError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join("nodir", "appspec.json"), filepath.Join(dir, "appspec.json")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	streams, _ := testStreams()
	if err := runPreset("webserver", dir, streams); err == nil {
		t.Fatal("write through a symlink to a missing directory must fail")
	}
}

// TestGenApp_SpecErrors 读声明、解析声明、Gate1 三道前置门各自短路，错误
// 必须指明阶段（read / parse / Gate1），坏声明不许走到树构建。
func TestGenApp_SpecErrors(t *testing.T) {
	dir := t.TempDir()
	badJSON := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badJSON, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write bad json: %v", err)
	}
	gate := guardFixture()
	gate.Meta.Name = "" // Gate1 必拦：应用名缺失
	gatePath := writeSpecJSON(t, dir, gate)

	cases := []struct{ name, spec, want string }{
		{"missing", filepath.Join(dir, "absent.json"), "read spec:"},
		{"bad json", badJSON, "parse AppSpec (protojson):"},
		{"gate1", gatePath, "Gate1 (spec schema):"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			streams, _ := testStreams()
			err := runGenAppSpec(tc.spec, t.TempDir(), false, false, streams)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got: %v", tc.want, err)
			}
		})
	}
}

// TestGenApp_RefusesOverwriteWithoutAdopt 双跑不带 --adopt：owned 文件已存在
// 即拒绝覆盖，emitTree 的错误原样回到调用方（--adopt 是唯一覆盖语义）。
func TestGenApp_RefusesOverwriteWithoutAdopt(t *testing.T) {
	dir := t.TempDir()
	specPath := writeSpecJSON(t, dir, guardFixture())
	streams, _ := testStreams()
	if err := runGenAppSpec(specPath, dir, false, false, streams); err != nil {
		t.Fatalf("first gen: %v", err)
	}
	streams, _ = testStreams()
	err := runGenAppSpec(specPath, dir, false, false, streams)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second gen without --adopt must refuse overwrite, got: %v", err)
	}
}

// TestGenApp_PruneErrorPropagates --adopt 清理上一次清单里的 owned 产物时，
// 目标在树里是非空目录（树被外部改动）⇒ 删除失败必须上报，不能静默留旧产物。
func TestGenApp_PruneErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	specPath := writeSpecJSON(t, dir, guardFixture())
	streams, _ := testStreams()
	if err := runGenAppSpec(specPath, dir, true, false, streams); err != nil {
		t.Fatalf("first gen: %v", err)
	}

	manifestPath := filepath.Join(dir, appManifestName)
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

	streams, _ = testStreams()
	err = runGenAppSpec(specPath, dir, true, false, streams)
	if err == nil || !strings.Contains(err.Error(), "remove stale generated file stale") {
		t.Fatalf("unremovable stale owned entry must fail loudly, got: %v", err)
	}
}

// TestGenApp_ManifestWriteError 清单路径被占成目录：生成树写入都成功，清单
// 写盘失败必须上报（否则树看似生成成功却没有清单，--check 会误报 missing）。
func TestGenApp_ManifestWriteError(t *testing.T) {
	dir := t.TempDir()
	specPath := writeSpecJSON(t, dir, guardFixture())
	if err := os.MkdirAll(filepath.Join(dir, appManifestName), 0o755); err != nil {
		t.Fatalf("precreate manifest dir: %v", err)
	}
	streams, _ := testStreams()
	err := runGenAppSpec(specPath, dir, false, false, streams)
	if err == nil || !strings.Contains(err.Error(), "write manifest:") {
		t.Fatalf("manifest path as directory must fail the write, got: %v", err)
	}
}

// TestRoleServer_MissingRole 角色名不在声明里时返回 nil（继承语义，不 panic）。
func TestRoleServer_MissingRole(t *testing.T) {
	if got := roleServer(guardFixture(), "ghost"); got != nil {
		t.Fatalf("unknown role must resolve to nil, got %v", got)
	}
}

// TestRoleContracts_RoleWithoutServerInherits 未声明 server 的角色继承
// bootstrap 契约（不另开面）：每份角色配置与 base 逐字节一致。
func TestRoleContracts_RoleWithoutServerInherits(t *testing.T) {
	as := rolesOn(
		&appspecv1.RoleSpec{Name: "api", Domains: []string{"greet"}},
		&appspecv1.RoleSpec{Name: "runner"},
	)
	if errs := validateAppSpec(as); len(errs) != 0 {
		t.Fatalf("fixture must pass Gate1:\n%s", joinErrs(errs))
	}
	bs := contractBootstrap(as)
	base, err := contractGate(as, bs)
	if err != nil {
		t.Fatalf("contractGate: %v", err)
	}
	view := buildTreeView(as, bs)
	cfgs, err := roleContracts(as, bs, base, view)
	if err != nil {
		t.Fatalf("roleContracts: %v", err)
	}
	for _, role := range []string{"api", "runner"} {
		if string(cfgs[role]) != string(base) {
			t.Errorf("role %q without server must inherit the base contract", role)
		}
	}
}

// TestGenApp_InlineTLSBytes bootstrap 的 bytes 字段（server.http.tls.config
// .cert_pem 等 inline PEM）必须能投影进契约 YAML：装载器对 bytes 只认
// base64 字符串（protojson 口径），生成器若把 bytes 当不可表示值拒绝，
// 声明 inline TLS 的 AppSpec 就永远生成不出一份能启动的配置（J9：配置取值
// 只有契约一条通道）。这里同时钉「生成成功」与「落盘形态是 base64 串」。
func TestGenApp_InlineTLSBytes(t *testing.T) {
	dir := t.TempDir()
	as := guardFixture()
	as.Bootstrap.Server.Http.Tls = &bootstrapv1.Server_TLS{
		Config: &bootstrapv1.Server_TLS_Config{
			CertPem: []byte("cert-pem"),
			KeyPem:  []byte("key-pem"),
		},
	}
	specPath := writeSpecJSON(t, dir, as)
	streams, _ := testStreams()
	if err := runGenAppSpec(specPath, dir, true, false, streams); err != nil {
		t.Fatalf("inline TLS PEM must be representable in config yaml: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "configs", "demoapp.yaml"))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	// base64("cert-pem") / base64("key-pem")——protojson 与 bconf 装载器的 bytes 口径。
	for _, want := range []string{"cert_pem: Y2VydC1wZW0=", "key_pem: a2V5LXBlbQ=="} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("config yaml missing %q:\n%s", want, cfg)
		}
	}
}

// ---------------------------------------------------------------------------
// 结构性零覆盖（无法经公开路径触达，登记在此备查）：
//   - app.go:152/157/216 roleContracts/contractGate 的契约门错误——配置树由
//     描述符派生，键错误在结构上不可能出现；bytes 已按 base64 投影，其余
//     messageToMap 失败分支只服务未来新增的不可表示 kind。
//   - app.go:165 buildTree 错误——静态模板全部可渲染，缺角色契约由
//     roleContracts 与 view.Roles 同源保证，结构上不可达。
//   - app.go:169 buildManifest 错误——spec 在 138 行已读过，owned 生成物
//     均为可 gofmt 的静态模板（prepareContent 失败不可达）。
//   - appspec_contract.go:30/34/55/92/99/110/124/130/138/190-204：
//     contractTree/contractGate 的错误传递链——bytes 已按 base64 投影后，
//     真实 proto 的每个字段 kind 都可表示，messageToMap 不再有可达的失败源；
//     checkContractKeys 的键由描述符派生，回读复检在结构上不可能报错。
//   - appspec_contract.go:157/163 Duration/Timestamp 的类型断言失败——fd 声明
//     为 well-known 消息时生成代码的值类型恒为 *durationpb.Duration /
//     *timestamppb.Timestamp。
// ---------------------------------------------------------------------------
