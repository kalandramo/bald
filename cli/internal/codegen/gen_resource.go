package codegen

// gen_resource.go 是 gen resource 子命令：给一个域**加一条资源链路**——契约
// （proto）+ 实体 + 域声明，然后立刻重派生绑定/路由/授权。
//
// 与 onexai `create api --kinds post` 的对应关系（也说明为什么不照抄它的 9 个
// 文件）：
//
//	onexai 一条命令落 9 个新文件 + 2 个改注册表（handler/biz/store/model/
//	errno/conversion 各一份，注册靠改 IStore/IBiz）；
//	bald 的架构里 handler 绑定、REST 路由、gRPC 注册、授权动作是从 proto 的
//	google.api.http 注解与 service 声明**派生**的（gen domain），不是写死的
//	胶水——所以这里只需落「声明与数据形状」两件事，派生物由 gen domain 重算。
//	剩下的业务逻辑（字段语义、状态流转、查询组合）两边的生成器都不做，
//	那是人的活。
//
// 为什么 --apply-decl 是显式 opt-in 而不是默认：appspec.json 是 user 文件，
// 生成器「永不读写」是 directory-r03 的硬约束，也是这套生成器的信任基础。
// 追加一条 domains[] 声明确实是机械动作，但默认改写用户声明会让「生成器碰过
// 我的文件」变成常态——那正是生成树不可审计的开始。给了 --apply-decl 才写，
// 且写入走 protojson 回写（覆盖的是契约字段，不是文本替换）。

import (
	"bytes"
	"fmt"
	"github.com/kalandramo/bald/cli/internal/fsatomic"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// resourceOptions 是 gen resource 叶子命令的领域配置。
type resourceOptions struct {
	Spec      string // --spec：AppSpec JSON 路径（域名的真相源）
	Domain    string // --domain：目标域（未声明时按约定派生包名）
	Kinds     string // --kind：逗号分隔的资源名（post,comment）
	OutRoot   string // --out：产出根（默认 .）
	ProtoRoot string // --proto-root：proto 根（缺省探测 api/proto|api/protos|proto）
	ApplyDecl bool   // --apply-decl：把 domains[] 声明回写进 AppSpec（默认只打印片段）
	Role      string // --role：新域归哪个角色服务（缺省：唯一角色；多角色时必须显式给）
	Force     bool   // --force：覆盖已存在的 proto/实体（默认拒绝）
	Check     bool   // --check：只校验（零写入，有差异即失败）

	ioStreams
}

// kindSpec 是一条资源的派生命名。
type kindSpec struct {
	Kind   string // post
	Title  string // Post
	Plural string // posts
	File   string // post.proto
}

func (o *resourceOptions) Complete() error {
	if o.OutRoot == "" {
		o.OutRoot = "."
	}
	return nil
}

func (o *resourceOptions) Validate() error {
	if !discoverSpec(&o.Spec, o.Out) {
		return fmt.Errorf("the --spec <AppSpec.json> is required (domain naming derives from it)")
	}
	if o.Domain == "" {
		return fmt.Errorf("the --domain <name> is required (the resource lands in a declared or new domain)")
	}
	if strings.TrimSpace(o.Kinds) == "" {
		return fmt.Errorf("the --kind <post[,comment]> is required")
	}
	if o.Force && o.Check {
		return fmt.Errorf("--force and --check are mutually exclusive")
	}
	return nil
}

// kinds 解析 --kind 列表（保序去重，非法名直接拒绝：资源名会变成 proto 消息
// 名与文件名，容忍非法输入只会在 buf generate 时炸得更晚、更难读）。
func (o *resourceOptions) kinds() ([]kindSpec, error) {
	seen := map[string]bool{}
	var out []kindSpec
	for _, raw := range strings.Split(o.Kinds, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if !isLowerSnake(name) {
			return nil, fmt.Errorf("--kind %q must be lower_snake_case (it becomes a proto message and file name)", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, kindSpec{Kind: name, Title: title(name), Plural: name + "s", File: name + ".proto"})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the --kind <post[,comment]> is required")
	}
	return out, nil
}

// isLowerSnake 判定 lower_snake_case（首字符字母）。
func isLowerSnake(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

// Run 纯执行：读声明 → 定域命名 → 落 proto + 实体 →（可选）回写声明 →
// 重派生域驱动生成物。
func (o *resourceOptions) Run() error {
	raw, err := os.ReadFile(o.Spec)
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	as := &appspecv1.AppSpec{}
	if err := protojson.Unmarshal(raw, as); err != nil {
		return fmt.Errorf("parse AppSpec (protojson): %w", err)
	}
	if err := guardErrs(validateAppSpec(as)); err != nil {
		return fmt.Errorf("Gate1 (spec schema): %w", err)
	}
	kinds, err := o.kinds()
	if err != nil {
		return err
	}

	protoRoot, err := o.protoRoot()
	if err != nil {
		return err
	}
	protoPkg, goPkg, err := resolveDomain(as, o.Domain)
	if err != nil {
		return err
	}
	goDir, err := domainDir(as.GetMeta().GetModule(), goPkg)
	if err != nil {
		return err
	}
	pkgBase := filepath.Base(goDir)
	layout, err := o.detectLayout(as.GetMeta().GetModule(), protoPkg, protoRoot)
	if err != nil {
		return err
	}

	var files []genFile
	var services []string
	for _, k := range kinds {
		protoRaw, err := renderTmpl("resource-proto-"+k.Kind, protoTmpl, map[string]string{
			"Name": k.Kind, "Plural": k.Plural, "GoPackage": layout.GoPackage, "ProtoPackage": protoPkg,
		})
		if err != nil {
			return err
		}
		files = append(files, genFile{
			Path: filepath.Join(layout.Dir, k.File),
			Own:  owned,
			Raw:  protoRaw,
		})
		entityRaw, err := renderTmpl("resource-entity-"+k.Kind, entityTmpl, map[string]string{"Name": k.Kind, "Pkg": pkgBase})
		if err != nil {
			return err
		}
		files = append(files, genFile{
			Path:  filepath.Join(goDir, k.Kind+".go"),
			Own:   owned,
			Raw:   entityRaw,
			GoSrc: true,
		})
		services = append(services, protoPkg+"."+k.Title+"Service")
	}

	// 声明回写（可选）：先算出新声明并让它过一道 Gate1，再落文件——声明写歪时
	// 不该留下半套资源。Gate1 在这一步特别必要：AppSpec 有一条「roles 的
	// domains 并集 == domains 全集」的约束，只追加 domains[] 会让新域无人服务，
	// 错误要到 gen domain 才冒出来（那时 proto 与实体已经落盘了）。
	nextSpec, err := declWithServices(as, o.Domain, protoPkg, goPkg, services, o.Role)
	if err != nil {
		return err
	}
	if err := validateDeclaredSpec(nextSpec); err != nil {
		return fmt.Errorf("declaration after this resource would be invalid: %w", err)
	}

	if o.Check {
		return o.checkFiles(files)
	}
	for _, f := range files {
		if err := writeGuarded(filepath.Join(o.OutRoot, f.Path), f.Raw, f.Own, o.Force, o.ioStreams); err != nil {
			return err
		}
	}
	if !o.ApplyDecl {
		fmt.Fprintln(o.Out, "declaration snippet (paste into appspec.json domains[], or rerun with --apply-decl):")
		fmt.Fprintln(o.Out, domainSnippetJSON(o.Domain, protoPkg, goPkg, services))
		fmt.Fprintln(o.Out, "next: add the snippet, then run `bald gen domain --spec "+o.Spec+"` to derive bindings/routes/authz")
		return nil
	}
	if err := fsatomic.WriteFile(o.Spec, nextSpec, 0o644); err != nil {
		return fmt.Errorf("write spec: %w", err)
	}
	fmt.Fprintln(o.Out, "updated:", o.Spec, "(domains[] extended with "+o.Domain+")")

	// 声明变了 → 应用清单的 spec 摘要也变了：先刷新 app 树，否则紧接着的
	// `gen app --check`（CI 的漂移门）会立刻报 appspec.manifest.json 过期。
	if err := runGenAppSpec(o.Spec, o.OutRoot, true, false, o.ioStreams); err != nil {
		return fmt.Errorf("refresh app tree after declaration change: %w", err)
	}

	// 重派生：声明已就位，绑定/路由/授权/域清单都由 gen domain 重算。
	// Adopt 是必需的——既有域已有 owned 生成物（清单/convert/authz），不覆盖
	// 就只剩「拒绝重生成」；本命令的语义就是「改了声明，重新派生一遍」。
	dom := &domainOptions{Spec: o.Spec, OutDir: o.OutRoot, ioStreams: o.ioStreams, Adopt: true}
	if err := dom.Complete(); err != nil {
		return err
	}
	if err := dom.Validate(); err != nil {
		return err
	}
	if err := dom.Run(); err != nil {
		// 契约与声明已经落盘（它们本身是对的），失败的只是「派生」这一段。
		// 最常见的原因是树的 proto 依赖没配全（模板 import 了 googleapis /
		// protovalidate / bald store 三个模块的注解与消息），那属于树的属性、
		// 不归生成器猜——所以给修法而不是回滚。
		fmt.Fprintln(o.Out, "note: contracts and declaration are already written; if buf failed on a missing import, "+
			"add the proto dep modules to api/buf.yaml (buf.build/googleapis/googleapis, buf.build/bufbuild/protovalidate, "+
			"buf.build/kalandramo/bald-bconf), then rerun `bald gen domain --spec "+o.Spec+" --adopt`")
		return fmt.Errorf("derive domain artifacts: %w", err)
	}
	fmt.Fprintln(o.Out, "next: implement the new RPCs in the domain package (contracts, routes, authz and bindings are already derived)")
	return nil
}

// protoLayout 是一份域契约的落盘形状：新 proto 放哪、go_package 写什么。
//
// 两个值必须分开：proto 的 go_package 指向 **buf 产出**的 Go 包（api/gen/go/…），
// DomainSpec.go_package 指向**手写**域包（internal/<apiserver>/…v1）。写成同一个
// 值，要么 buf 往手写包倾倒生成物，要么绑定 import 一个不存在的包。
type protoLayout struct {
	Dir       string // 相对 --out 的目录
	GoPackage string // option go_package（"path;alias"）
}

// detectLayout 探测本域既有契约的形状：同包已有 proto 时沿用它的目录与
// go_package（扁平 api/proto/greet.proto 与分目录 api/proto/greet/v1/ 并存
// 的两种现实布局都不硬编码）；没有则读树的 buf managed 前缀派生，再退回
// gen proto 的模板约定（见 buf_layout.go：两代布局都真实存在，猜错必然编译失败）。
func (o *resourceOptions) detectLayout(module, protoPkg, protoRoot string) (protoLayout, error) {
	scan, _, err := scanProtoRoots(o.OutRoot)
	if err != nil {
		return protoLayout{}, err
	}
	for _, f := range scan.Files {
		if f.Package != protoPkg || f.GoPackage == "" {
			continue
		}
		dir := filepath.Dir(filepath.FromSlash(f.Path))
		if dir == "." {
			dir = protoRoot
		}
		return protoLayout{Dir: dir, GoPackage: f.GoPackage}, nil
	}
	goPkg, err := protoGoPackage(o.OutRoot, protoRoot, module, o.Domain)
	if err != nil {
		return protoLayout{}, err
	}
	return protoLayout{
		Dir:       filepath.Join(protoRoot, o.Domain, "v1"),
		GoPackage: goPkg,
	}, nil
}

// protoRoot 返回 proto 根：优先沿用树上已有的（api/proto|api/protos|proto），
// 都没有时落 api/proto（与 gen proto 的 --out 缺省同源）。
func (o *resourceOptions) protoRoot() (string, error) {
	if o.ProtoRoot != "" {
		return o.ProtoRoot, nil
	}
	for _, cand := range []string{"api/proto", "api/protos", "proto"} {
		if st, err := os.Stat(filepath.Join(o.OutRoot, cand)); err == nil && st.IsDir() {
			return cand, nil
		}
	}
	return filepath.Join("api", "proto"), nil
}

// resolveDomain 返回 proto 包名与 go_package（手写域包）。
//
// 已声明：沿用声明里的包名（改名会让既有生成物全部失联）；未声明：按
// <domain>.v1 / <module>/internal/<apiserver_dir>/<domain>v1 派生——与
// _example/bald 的 greet → greetv1 同形，避免两套命名约定并存。
func resolveDomain(as *appspecv1.AppSpec, name string) (string, string, error) {
	for _, d := range as.GetDomains() {
		if d.GetName() != name {
			continue
		}
		if d.GetHttpOnly() {
			return "", "", fmt.Errorf("domain %q is http_only (no proto service); add RPCs by hand or declare a service form", name)
		}
		pkg := d.GetProtoPackage()
		if pkg == "" {
			return "", "", fmt.Errorf("domain %q declares no proto_package", name)
		}
		return pkg, d.GetGoPackage(), nil
	}
	apiDir := as.GetLayout().GetApiserverDir()
	if apiDir == "" {
		apiDir = "apiserver"
	}
	mod := as.GetMeta().GetModule()
	if mod == "" {
		return "", "", fmt.Errorf("meta.module is required to derive the domain go_package")
	}
	return name + ".v1", mod + "/internal/" + apiDir + "/" + name + "v1", nil
}

// declWithServices 返回追加了本域/本服务后的 AppSpec 字节（protojson）。
// 已是该形状时返回原字节（幂等：重跑不改文件）。
//
// 同时把新域归到角色：AppSpec 要求 meta.roles[].domains 的并集等于 domains 全集
// （缺域 = 该域无人服务）。单角色时自动挂上；多角色时靠 --role 指定——猜「哪个
// 角色该服务这个域」是业务判断，生成器不该代劳。
func declWithServices(as *appspecv1.AppSpec, name, protoPkg, goPkg string, services []string, role string) ([]byte, error) {
	clone, ok := proto.Clone(as).(*appspecv1.AppSpec)
	if !ok {
		return nil, fmt.Errorf("clone AppSpec failed")
	}
	var target *appspecv1.DomainSpec
	for _, d := range clone.GetDomains() {
		if d.GetName() == name {
			target = d
			break
		}
	}
	if target == nil {
		target = &appspecv1.DomainSpec{Name: name, ProtoPackage: protoPkg, GoPackage: goPkg}
		clone.Domains = append(clone.Domains, target)
	}
	// 已有单数 service 时先折成复数形态（两者互斥，Gate1 会判）。
	if s := target.GetService(); s != "" {
		target.Services = append(target.Services, s)
		target.Service = ""
	}
	for _, want := range services {
		dup := false
		for _, have := range target.GetServices() {
			if have == want {
				dup = true
				break
			}
		}
		if !dup {
			target.Services = append(target.Services, want)
		}
	}
	if err := assignDomainToRole(clone, name, role); err != nil {
		return nil, err
	}
	out, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(clone)
	if err != nil {
		return nil, fmt.Errorf("marshal spec: %w", err)
	}
	return append(out, '\n'), nil
}

// assignDomainToRole 把域挂到角色的 domains 列表（幂等；已挂则不动）。
func assignDomainToRole(as *appspecv1.AppSpec, domain, role string) error {
	roles := as.GetMeta().GetRoles()
	if len(roles) == 0 {
		// 空 roles = 单角色形态，Gate1 不校验归属——但**显式点名**的角色仍然
		// 要校验：静默接受一个不存在的角色（adv-codegen 观察 C）与多角色树上
		// 报 unknown role 的口径自相矛盾，且让调用方误以为挂载生效了。
		if role != "" {
			return fmt.Errorf("unknown role %q (meta.roles is empty)", role)
		}
		return nil
	}
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.GetName())
		for _, d := range r.GetDomains() {
			if d == domain {
				return nil // 已有人服务，保持原样
			}
		}
	}
	pick := role
	switch {
	case pick == "" && len(roles) == 1:
		pick = roles[0].GetName()
	case pick == "":
		return fmt.Errorf("--role <name> is required: meta.roles has %d roles (%s) and domain %q is not served by any; "+
			"pass the role that should serve it", len(roles), strings.Join(names, ", "), domain)
	}
	for _, r := range roles {
		if r.GetName() == pick {
			r.Domains = append(r.Domains, domain)
			return nil
		}
	}
	return fmt.Errorf("unknown role %q (meta.roles: %s)", pick, strings.Join(names, ", "))
}

// validateDeclaredSpec 让待写回的声明过一遍 Gate1——生成器写出的声明必须自己
// 就是合法的，否则错误会推迟到下一个命令，且那时树已经改了。
func validateDeclaredSpec(raw []byte) error {
	as := &appspecv1.AppSpec{}
	if err := protojson.Unmarshal(raw, as); err != nil {
		return fmt.Errorf("parse declaration: %w", err)
	}
	return guardErrs(validateAppSpec(as))
}

// domainSnippetJSON 是给人粘贴的声明片段（--apply-decl 未开时的出口）。
func domainSnippetJSON(name, protoPkg, goPkg string, services []string) string {
	var b strings.Builder
	b.WriteString("  {\n")
	fmt.Fprintf(&b, "    \"name\": %q,\n", name)
	fmt.Fprintf(&b, "    \"protoPackage\": %q,\n", protoPkg)
	fmt.Fprintf(&b, "    \"goPackage\": %q,\n", goPkg)
	b.WriteString("    \"services\": [")
	for i, s := range services {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", s)
	}
	b.WriteString("]\n  }")
	return b.String()
}

// checkFiles 是 --check 的零写入校验：目标文件必须已存在且逐字节一致。
func (o *resourceOptions) checkFiles(files []genFile) error {
	var problems []string
	for _, f := range files {
		path := filepath.Join(o.OutRoot, f.Path)
		have, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, path+": missing ("+err.Error()+")")
			continue
		}
		if !bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(f.Raw)) {
			problems = append(problems, path+": content differs from declaration")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("gen resource --check: %d problem(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
	fmt.Fprintln(o.Out, "ok:", len(files), "resource file(s) match the declaration")
	return nil
}

// resourceShort 是资源命令在 gen 命令组里的短描述（保持组内一览的对称）。
const resourceShort = "给域加一条资源链路（proto 契约 + 实体 + 域声明，随后重派生绑定）"

// genResourceCmd 组装 gen resource 叶子命令。
//
// 它把「加一条资源」的四步收敛成一条命令：契约（proto CRUD 六件套）→ 实体
// 骨架 → 域声明（--apply-decl 才回写）→ 重派生（gen domain 的绑定/路由/授权）。
// 默认不碰 appspec.json，是 directory-r03「生成器不写 user 文件」的直接推论。
func genResourceCmd(streams ioStreams) *cobra.Command {
	o := &resourceOptions{ioStreams: streams}
	cmd := &cobra.Command{
		Use:   "resource --spec <AppSpec.json> --domain <name> --kind <post[,comment]>",
		Short: resourceShort,
		Long: `给一个域加一条资源链路，并立刻重派生域驱动生成物：

  1. 契约    api/proto/<domain>/v1/<kind>.proto    CRUD 六件套 + google.api.http
                                                  注解（复用 gen proto 的模板）
  2. 实体    <go_package>/<kind>.go                GORM 模型 + KeyOf（复用 gen store
                                                  的模板）
  3. 声明    appspec.json domains[]                只在 --apply-decl 时回写
  4. 重派生  gen domain 的全部产物                  domains.gen.go / convert /
                                                  authz alias / bind / routes

与 onexai ` + "`create api --kinds post`" + ` 的差别：它落 9 个新文件再改两张注册表，
bald 只落「声明 + 数据形状」——handler 绑定、REST 路由、gRPC 注册、授权动作都是
从 proto 注解**派生**的，不写死胶水。业务逻辑（字段语义、状态流转）两边都不生成。

目录与 go_package 探测：同包已有 proto 时沿用它的目录与 option go_package（扁平
api/proto/greet.proto 与分目录 api/proto/greet/v1/ 都支持）；否则按 gen proto 的
缺省约定派生 <module>/api/gen/go/<domain>/v1;<domain>v1。proto 的 go_package 指向
buf 产出的包，DomainSpec.go_package 指向手写域包——两者不同值。

不传 --apply-decl 时打印可直接粘贴的 domains[] 片段；传了则回写（protojson 覆
盖契约字段，不是文本替换）并接着跑 gen domain。

角色归属：AppSpec 要求 meta.roles[].domains 的并集等于 domains 全集（缺域=该域
无人服务），所以回写时会把新域挂到一个角色上——单角色自动挂，多角色用 --role 指定
（「哪个角色服务这个域」是业务判断，不猜）。写回前声明会再过一遍 Gate1：生成器
写出的声明必须自己就合法。

派生一条不漏：重派生走完整的 gen domain（含注解绑定）。缺 proto 的 Go 类型时会
自动执行 --proto-cmd（默认 buf generate）——生成器不提供「跳过绑定」的开关，因为
--no-bind 在 gen domain 里的含义是「这棵树不该有绑定」，拿它救急会把别的域已经生成
好的绑定文件当陈旧产物删掉。`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(*cobra.Command, []string) error {
			if err := o.Complete(); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			return o.Run()
		},
	}
	cmd.Flags().StringVar(&o.Spec, "spec", "", "AppSpec JSON 路径（protojson；必填——域命名的真相源）")
	cmd.Flags().StringVar(&o.Domain, "domain", "", "目标域（已声明则沿用其包名，未声明则按约定派生；必填）")
	cmd.Flags().StringVar(&o.Kinds, "kind", "", "资源名，逗号分隔的 lower_snake_case（post,comment；必填）")
	cmd.Flags().StringVar(&o.OutRoot, "out", "", "产出根（默认 .）")
	cmd.Flags().StringVar(&o.ProtoRoot, "proto-root", "", "proto 根（缺省探测 api/proto、api/protos、proto）")
	cmd.Flags().BoolVar(&o.ApplyDecl, "apply-decl", false, "把 domains[] 声明回写进 AppSpec 并立刻重派生（默认只打印片段）")
	cmd.Flags().StringVar(&o.Role, "role", "", "新域归哪个角色服务（缺省：唯一角色；多角色时必须指定——roles 的 domains 并集必须等于 domains 全集）")
	cmd.Flags().BoolVar(&o.Force, "force", false, "覆盖已存在的 proto/实体（默认拒绝）")
	cmd.Flags().BoolVar(&o.Check, "check", false, "只校验 proto/实体是否与声明一致（零写入，有差异即失败）")
	_ = cmd.MarkFlagFilename("spec", ".json")
	cmd.SetErr(streams.ErrOut)
	return cmd
}
