package codegen

// gen_resource_test.go 是 gen resource 的验收测试：一条命令把「契约 + 实体 +
// 域声明 + 重派生」串起来，且四件事各自的边界都守住——
//
//   - 默认不碰 appspec.json（directory-r03：生成器不写 user 文件）
//   - owned 三态：重跑拒绝、--force 覆盖且字节一致、--check 零写入
//   - go_package 的两个值不混用（proto 指向 buf 产出包，DomainSpec 指向手写域包）
//   - 既有契约布局优先（同包已有 proto 时沿用它的目录与 go_package）

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// resourceFixture 是「尚无域声明」的最小 AppSpec：gen resource 要能在空
// domains[] 上把新域从零立起来（域名的真相源是 --domain + 约定）。
func resourceFixture() *appspecv1.AppSpec {
	return &appspecv1.AppSpec{
		Meta: &appspecv1.AppMeta{Name: "demoapp", Module: "demo.example/app", Desc: "demo desc"},
	}
}

// writeResourceStub 落一份新资源的 proto Go 桩（buf generate 的等价物）。
//
// gen resource 的派生一条不漏：它调 gen domain 生成注解绑定，而绑定要 import
// proto 的 Go 类型。真实项目由 --proto-cmd（默认 buf generate）产出；测试里给一组
// emptypb.Empty 的类型别名即可满足生成期「包存在且消息类型可用」的前置。
// domain 是域 proto 目录名（= 包前缀），如 "post" → api/gen/go/post/v1。
func writeResourceStub(t *testing.T, dir, domain string, kinds ...string) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "package %sv1\n\nimport \"google.golang.org/protobuf/types/known/emptypb\"\n\n// 测试桩：真实产物由 protoc-gen-go 生成。\n", domain)
	for _, k := range kinds {
		title := strings.ToUpper(k[:1]) + k[1:]
		for _, suffix := range []string{"", "ListRequest", "ListResponse", "GetRequest", "CreateRequest", "UpdateRequest", "DeleteRequest"} {
			fmt.Fprintf(&b, "\ntype %s%s = emptypb.Empty\n", title, suffix)
		}
	}
	pkgDir := filepath.Join(dir, "api", "gen", "go", domain, "v1")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir proto go package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "stub.pb.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write proto go stub: %v", err)
	}
}

// runGenResource 在 dir 执行 gen resource（cwd 切到 dir，保持 --out 缺省语义）。
func runGenResource(t *testing.T, dir, specPath string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(dir)
	streams, out := testStreams()
	cmd := genResourceCmd(streams)
	cmd.SetArgs(append([]string{"--spec", specPath}, args...))
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return out.String(), err
}

// TestGenResource_ScaffoldAndSnippet 主路径（不回写声明）：契约 + 实体落盘，
// 输出可粘贴的 domains[] 片段，appspec.json 分毫不动。
func TestGenResource_ScaffoldAndSnippet(t *testing.T) {
	dir := t.TempDir()
	scaffoldModule(t, dir, "demo.example/app")
	specPath := writeSpecJSON(t, dir, resourceFixture())
	before, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}

	out, err := runGenResource(t, dir, specPath, "--domain", "post", "--kind", "post")
	if err != nil {
		t.Fatalf("gen resource: %v\n%s", err, out)
	}

	// 契约：复用 gen proto 的 CRUD 六件套模板，go_package 指向 buf 产出包
	// （不是手写域包——混用会让 buf 往 internal/ 倾倒生成物）。
	protoSrc, err := os.ReadFile(filepath.Join(dir, "api", "proto", "post", "v1", "post.proto"))
	if err != nil {
		t.Fatalf("read proto: %v", err)
	}
	for _, want := range []string{
		"package post.v1;",
		`option go_package = "demo.example/app/api/gen/go/post/v1;postv1";`,
		"service PostService {",
		`get: "/v1/posts"`,
		"message Post {",
	} {
		if !strings.Contains(string(protoSrc), want) {
			t.Errorf("proto missing %q:\n%s", want, protoSrc)
		}
	}

	// 实体：复用 gen store 的实体模板，落在 DomainSpec.go_package 指向的域包。
	entitySrc, err := os.ReadFile(filepath.Join(dir, "internal", "apiserver", "postv1", "post.go"))
	if err != nil {
		t.Fatalf("read entity: %v", err)
	}
	for _, want := range []string{"package postv1", "type Post struct", "func KeyOfPost"} {
		if !strings.Contains(string(entitySrc), want) {
			t.Errorf("entity missing %q:\n%s", want, entitySrc)
		}
	}

	// 片段：字段名与 AppSpec 的 protojson 形状一致（可直接粘进 domains[]）。
	for _, want := range []string{
		`"name": "post"`,
		`"protoPackage": "post.v1"`,
		`"goPackage": "demo.example/app/internal/apiserver/postv1"`,
		`"services": ["post.v1.PostService"]`,
		"next: add the snippet",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// 默认不写 user 文件。
	after, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec after: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("appspec.json must be untouched without --apply-decl:\nbefore=%s\nafter=%s", before, after)
	}

	// owned 三态：重跑拒绝，--force 覆盖且逐字节一致（确定性）。
	if _, err := runGenResource(t, dir, specPath, "--domain", "post", "--kind", "post"); err == nil {
		t.Fatal("regeneration without --force must be refused (owned)")
	} else if !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal must mention --force, got: %v", err)
	}
	first := string(protoSrc)
	if _, err := runGenResource(t, dir, specPath, "--domain", "post", "--kind", "post", "--force"); err != nil {
		t.Fatalf("regen --force: %v", err)
	}
	regen, err := os.ReadFile(filepath.Join(dir, "api", "proto", "post", "v1", "post.proto"))
	if err != nil {
		t.Fatalf("read regenerated proto: %v", err)
	}
	if string(regen) != first {
		t.Error("regeneration must be byte-identical (deterministic)")
	}
}

// TestGenResource_ApplyDeclDerives 回写声明并立刻重派生：domains[] 得到
// services[]，convert/authz/manifest 由 gen domain 重算——一条命令走完
// 「加资源」，不给用户留「记得再跑一次」的隐性步骤。
func TestGenResource_ApplyDeclDerives(t *testing.T) {
	dir := t.TempDir()
	scaffoldModule(t, dir, "demo.example/app")
	specPath := writeSpecJSON(t, dir, resourceFixture())
	writeResourceStub(t, dir, "post", "post") // buf generate 的等价物

	out, err := runGenResource(t, dir, specPath, "--domain", "post", "--kind", "post", "--apply-decl")
	if err != nil {
		t.Fatalf("gen resource --apply-decl: %v\n%s", err, out)
	}

	// 声明：services[] 形态（DomainSpec 三形态互斥，单数 service 会被折成复数）。
	specRaw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	post := findDomain(t, specPath, "post")
	if post == nil {
		t.Fatalf("declaration must gain domain post, got:\n%s", specRaw)
	}
	if got, want := post.GetProtoPackage(), "post.v1"; got != want {
		t.Errorf("proto_package = %q, want %q", got, want)
	}
	if got, want := post.GetGoPackage(), "demo.example/app/internal/apiserver/postv1"; got != want {
		t.Errorf("go_package = %q, want %q", got, want)
	}
	if got := post.GetServices(); len(got) != 1 || got[0] != "post.v1.PostService" {
		t.Errorf("services = %v, want [post.v1.PostService]", got)
	}
	if s := post.GetService(); s != "" {
		t.Errorf("singular service must stay empty once services[] is used, got %q", s)
	}

	// 重派生：清单 + 授权投影来自 gen domain，与新域同名同址。
	manifest, err := os.ReadFile(filepath.Join(dir, "internal", "bootstrap", "domains.gen.go"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	for _, want := range []string{"postv1.Domain,", "greet"} {
		if want == "greet" { // 空域夹具不应凭空造域
			if strings.Contains(string(manifest), "greetv1.Domain") {
				t.Errorf("manifest must not invent a greet domain:\n%s", manifest)
			}
			continue
		}
		if !strings.Contains(string(manifest), want) {
			t.Errorf("manifest missing %q:\n%s", want, manifest)
		}
	}
	conv, err := os.ReadFile(filepath.Join(dir, "internal", "apiserver", "handler", "convert", "post.gen.go"))
	if err != nil {
		t.Fatalf("read convert projection: %v", err)
	}
	if !strings.Contains(string(conv), `"post.v1.PostService/List"`) {
		t.Errorf("convert projection missing List action:\n%s", conv)
	}
	// 绑定/路由：注解派生的产物必须一起到位（否则 REST 面缺一条资源）。
	bind, err := os.ReadFile(filepath.Join(dir, "internal", "apiserver", "handler", "gin", "post.bind.gen.go"))
	if err != nil {
		t.Fatalf("read binding: %v", err)
	}
	if !strings.Contains(string(bind), "PostService") {
		t.Errorf("binding missing PostService:\n%s", bind)
	}
	// Delete 返回 google.protobuf.Empty：必须折成 emptypb.Empty（带自己的 import），
	// 不能拼成域包前缀下的假标识符（postv1.google_protobuf_Empty）——那会让
	// 「生成一个域再派生绑定」这条主路径必然编译失败。见 well_known_types.go。
	if !strings.Contains(string(bind), "emptypb.Empty") {
		t.Errorf("binding must map google.protobuf.Empty to emptypb.Empty:\n%s", bind)
	}
	if strings.Contains(string(bind), "google_protobuf_Empty") {
		t.Errorf("binding must not qualify well-known types with the domain package:\n%s", bind)
	}
	if !strings.Contains(string(bind), `"google.golang.org/protobuf/types/known/emptypb"`) {
		t.Errorf("binding must import emptypb:\n%s", bind)
	}
	routes, err := os.ReadFile(filepath.Join(dir, "internal", "apiserver", "routes", "post.gen.go"))
	if err != nil {
		t.Fatalf("read routes: %v", err)
	}
	if !strings.Contains(string(routes), "/v1/posts") {
		t.Errorf("routes missing /v1/posts:\n%s", routes)
	}
	// 整棵树自证：app 清单（spec 摘要已刷新）+ 域产物都与声明一致。
	if vout, verr := runGenVerify(t, dir, specPath); verr != nil {
		t.Fatalf("tree must verify after gen resource: %v\n%s", verr, vout)
	}

	// 幂等：声明已是该形状时重跑不改文件（declWithServices 去重）。
	second, err := runGenResource(t, dir, specPath,
		"--domain", "post", "--kind", "post", "--apply-decl", "--force")
	if err != nil {
		t.Fatalf("rerun: %v\n%s", err, second)
	}
	specAgain, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec again: %v", err)
	}
	if string(specAgain) != string(specRaw) {
		t.Errorf("declaration must be idempotent:\nfirst=%s\nsecond=%s", specRaw, specAgain)
	}
}

// TestGenResource_ExistingDomainReusesPackage 已声明域：沿用声明里的包名与
// go_package（改名会让既有生成物全部失联），且不重复追加域条目。
func TestGenResource_ExistingDomainReusesPackage(t *testing.T) {
	dir := t.TempDir()
	scaffoldModule(t, dir, "demo.example/app")
	as := resourceFixture()
	as.Domains = []*appspecv1.DomainSpec{{
		Name: "greet", ProtoPackage: "greet.v1", Service: "greet.v1.GreetService",
		GoPackage: "demo.example/app/internal/apiserver/greetv1",
	}}
	specPath := writeSpecJSON(t, dir, as)
	writeFixtureProto(t, dir)                      // 既有契约：package greet.v1 + 分目录布局
	writeResourceStub(t, dir, "greet", "greeting") // 新资源的 proto Go 桩

	out, err := runGenResource(t, dir, specPath, "--domain", "greet", "--kind", "greeting", "--apply-decl")
	if err != nil {
		t.Fatalf("gen resource on declared domain: %v\n%s", err, out)
	}

	// 新契约落在既有布局里（沿用 api/proto/greet/v1），go_package 沿用既有
	// option（demo.example/app/api/gen/go/greet/v1;greetv1），不是另派生一套。
	src, err := os.ReadFile(filepath.Join(dir, "api", "proto", "greet", "v1", "greeting.proto"))
	if err != nil {
		t.Fatalf("read new proto: %v", err)
	}
	for _, want := range []string{
		"package greet.v1;",
		`option go_package = "demo.example/app/api/gen/go/greet/v1;greetv1";`,
		"service GreetingService {",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("proto missing %q:\n%s", want, src)
		}
	}

	// 声明：单数 service 折成 services[] 复数形态，且既有的 GreetService 不丢。
	greet := findDomain(t, specPath, "greet")
	if greet == nil {
		t.Fatal("domain greet must stay declared")
	}
	if s := greet.GetService(); s != "" {
		t.Errorf("singular service must be folded into services[], still %q", s)
	}
	if got := greet.GetServices(); len(got) != 2 ||
		got[0] != "greet.v1.GreetService" || got[1] != "greet.v1.GreetingService" {
		t.Errorf("services = %v, want [greet.v1.GreetService greet.v1.GreetingService] (declaration order)", got)
	}
}

// findDomain 按 protojson 反序列化取域条目（protojson 的多行缩进会让文本断言
// 依赖空格数，声明结构用解析结果判更稳）。
func findDomain(t *testing.T, specPath, name string) *appspecv1.DomainSpec {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	as := &appspecv1.AppSpec{}
	if err := protojson.Unmarshal(raw, as); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	var hits []*appspecv1.DomainSpec
	for _, d := range as.GetDomains() {
		if d.GetName() == name {
			hits = append(hits, d)
		}
	}
	if len(hits) > 1 {
		t.Errorf("domain %q declared %d times (must be at most once)", name, len(hits))
	}
	if len(hits) == 0 {
		return nil
	}
	return hits[0]
}

// TestGenResource_Check --check 零写入：一致时通过，漂移与缺件各自报出行。
func TestGenResource_Check(t *testing.T) {
	dir := t.TempDir()
	scaffoldModule(t, dir, "demo.example/app")
	specPath := writeSpecJSON(t, dir, resourceFixture())
	if _, err := runGenResource(t, dir, specPath, "--domain", "post", "--kind", "post"); err != nil {
		t.Fatalf("scaffold: %v", err)
	}

	out, err := runGenResource(t, dir, specPath, "--domain", "post", "--kind", "post", "--check")
	if err != nil {
		t.Fatalf("--check on matching tree: %v\n%s", err, out)
	}
	if !strings.Contains(out, "match the declaration") {
		t.Errorf("--check must report a match, got:\n%s", out)
	}

	protoPath := filepath.Join(dir, "api", "proto", "post", "v1", "post.proto")
	// 保持 proto 可解析（扫描要读 package/go_package），只改内容判漂移。
	if err := os.WriteFile(protoPath, []byte("syntax = \"proto3\";\n\npackage post.v1;\n"), 0o644); err != nil {
		t.Fatalf("tamper proto: %v", err)
	}
	_, err = runGenResource(t, dir, specPath, "--domain", "post", "--kind", "post", "--check")
	if err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("--check must flag drift, got: %v", err)
	}

	entityPath := filepath.Join(dir, "internal", "apiserver", "postv1", "post.go")
	if err := os.Remove(entityPath); err != nil {
		t.Fatalf("remove entity: %v", err)
	}
	_, err = runGenResource(t, dir, specPath, "--domain", "post", "--kind", "post", "--check")
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("--check must flag missing files, got: %v", err)
	}
	// 零写入：缺件不会被 --check 补出来。
	if _, statErr := os.Stat(entityPath); statErr == nil {
		t.Error("--check must not write files")
	}
}

// TestGenResource_MultipleKinds --kind 逗号分隔：每条资源一套契约 + 实体，
// services[] 按声明序聚合进同一个域。
func TestGenResource_MultipleKinds(t *testing.T) {
	dir := t.TempDir()
	scaffoldModule(t, dir, "demo.example/app")
	specPath := writeSpecJSON(t, dir, resourceFixture())
	writeResourceStub(t, dir, "blog", "post", "comment")

	out, err := runGenResource(t, dir, specPath, "--domain", "blog", "--kind", "post,comment", "--apply-decl")
	if err != nil {
		t.Fatalf("gen resource multi-kind: %v\n%s", err, out)
	}
	for _, name := range []string{"post", "comment"} {
		if _, err := os.Stat(filepath.Join(dir, "api", "proto", "blog", "v1", name+".proto")); err != nil {
			t.Errorf("missing proto for %s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "internal", "apiserver", "blogv1", name+".go")); err != nil {
			t.Errorf("missing entity for %s: %v", name, err)
		}
	}
	specRaw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	for _, want := range []string{"blog.v1.PostService", "blog.v1.CommentService"} {
		if !strings.Contains(string(specRaw), want) {
			t.Errorf("declaration missing %q:\n%s", want, specRaw)
		}
	}
}

// TestGenResource_Validation 负例：必填项、非法资源名、互斥开关、http_only 域
// 都在写盘前拒绝（错误信息点名可执行的修法）。
func TestGenResource_Validation(t *testing.T) {
	dir := t.TempDir()
	scaffoldModule(t, dir, "demo.example/app")
	as := resourceFixture()
	as.Domains = []*appspecv1.DomainSpec{{
		Name: "webonly", HttpOnly: true,
		GoPackage: "demo.example/app/internal/apiserver/webonlyv1",
	}}
	specPath := writeSpecJSON(t, dir, as)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no domain", []string{"--spec", specPath, "--kind", "post"}, "--domain"},
		{"no kind", []string{"--spec", specPath, "--domain", "post"}, "--kind"},
		{"bad kind", []string{"--spec", specPath, "--domain", "post", "--kind", "Post"}, "lower_snake_case"},
		{"force vs check", []string{"--spec", specPath, "--domain", "post", "--kind", "post", "--force", "--check"}, "mutually exclusive"},
		{"http_only domain", []string{"--spec", specPath, "--domain", "webonly", "--kind", "post"}, "http_only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runGenResource(t, dir, specPath, tc.args...)
			_ = out
			if err == nil {
				t.Fatalf("expected error mentioning %q, got none", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error must mention %q, got: %v", tc.want, err)
			}
		})
	}
}

// TestGenResource_RequiresSpec 没有 --spec 且 cwd 无 appspec.json 时报必填。
func TestGenResource_RequiresSpec(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	streams, _ := testStreams()
	cmd := genResourceCmd(streams)
	cmd.SetArgs([]string{"--domain", "post", "--kind", "post"})
	cmd.SilenceErrors = true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("gen resource without --spec and without appspec.json must fail")
	}
	if !strings.Contains(err.Error(), "AppSpec.json") {
		t.Errorf("error must point at the AppSpec file, got: %v", err)
	}
}

// TestGenResource_DiscoversSpec 应用目录里跑可以省掉 --spec（与 gen app 同一条
// 自动发现），并打印出用了哪一份。
func TestGenResource_DiscoversSpec(t *testing.T) {
	dir := t.TempDir()
	scaffoldModule(t, dir, "demo.example/app")
	writeSpecJSON(t, dir, resourceFixture())

	// 先用相对路径触发发现分支（--spec 留空 = 让命令自己找）。
	t.Chdir(dir)
	streams, out := testStreams()
	cmd := genResourceCmd(streams)
	cmd.SetArgs([]string{"--domain", "post", "--kind", "post"})
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gen resource with in-tree spec: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "using appspec.json from current directory") {
		t.Errorf("spec discovery must be announced, got:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "api", "proto", "post", "v1", "post.proto")); err != nil {
		t.Errorf("discovery path must generate the same tree: %v", err)
	}
}

// TestGenResource_UnknownRoleOnEmptyRoles 空 roles 树（单角色形态）上显式点名
// 一个不存在的角色必须报错——静默接受与多角色树的 unknown role 口径自相矛盾，
// 且让调用方误以为挂载生效（adv-codegen 观察 C）。
func TestGenResource_UnknownRoleOnEmptyRoles(t *testing.T) {
	dir := t.TempDir()
	scaffoldModule(t, dir, "demo.example/app")
	specPath := writeSpecJSON(t, dir, resourceFixture()) // meta.roles 为空

	_, err := runGenResource(t, dir, specPath, "--domain", "post", "--kind", "post", "--apply-decl", "--role", "ghost")
	if err == nil {
		t.Fatal("explicit --role ghost on an empty-roles tree must be refused")
	}
	if !strings.Contains(err.Error(), "unknown role") {
		t.Errorf("error must say unknown role, got: %v", err)
	}
}
