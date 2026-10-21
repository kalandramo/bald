package codegen

// gen_domain.go 是 gen domain 子命令：消费 AppSpec.domains[] 与
// 既有域 proto/域包，重写域驱动的生成物（domains.gen.go×2 +
// handler/{gin,grpc,convert}/<d>.gen.go + authz/alias.gen.go；装配改动 0）。
//
// 生成物全集（全部 owned，走 writeGuarded 三态通道）：
//   - <manifest>（默认 internal/bootstrap/domains.gen.go）：域清单聚合——
//     import 各域 go_package 并把 appkit.Domain 声明值变参直传 MustServe；
//     与 gen app 共用同一模板（bootstrapDomainsTmpl），单模板源不漂移。
//   - internal/<apiserver_dir>/domains.gen.go：逐域 handler 挂载扇出
//     （gen domain 把本文件升级为扇出）。
//   - handler/{grpc,gin,convert}/<d>.gen.go：逐域传输扇出——路由与
//     gRPC 注册的执行权在引擎（域声明值自持，appkit.Domain.Routes/Services），
//     生成文件把扇出落成可编译的委托 + 注解派生表（REST 路由表 / 授权动作
//     投影），使域清单与实际 proto 在编译层对账。
//   - authz/alias.gen.go：授权归一化别名表 + 默认动作聚合（proto service 派生，
//     D2 §2.4 #14）。
//   - <go_package>/domain.gen.go：域声明值骨架（生成一次，仅当域包尚无导出
//     Domain 时落盘；既有域包（如 _example/bald/greetv1）消费不覆盖）。
//
// Gate1 强化：domains[].service 必须与产出根下实际 proto 匹配
// （扫描 api/proto、api/protos、proto 三个候选根），校验失败的报错指明
// 哪个域的 service 在哪个 proto 里缺失、该 proto 实际声明了什么。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// protoRoots 是 gen domain 扫描域 proto 的候选根（相对产出根，存在即扫；
// 覆盖 api/proto 目录树与 _example 的早期布局 proto）。
var protoRoots = []string{"api/proto", "api/protos", "proto"}

// protoMethod 是从 proto 源码解析出的一个 rpc 方法（含 google.api.http 注解）。
type protoMethod struct {
	Name string
	Verb string // 注解动词（get/post/put/delete/patch）；无注解为空
	Path string // 注解路径（如 /v1/greet/{name}）
	Body string // 注解 body（"*" 或字段掩码）；GET/DELETE 缺省无 body
}

// protoService 是解析出的一个 service 声明。
type protoService struct {
	Package string
	Name    string
	Methods []protoMethod
}

// protoFile 是解析出的一个 proto 文件。
type protoFile struct {
	Path     string // 相对产出根
	Package  string
	Services []protoService
}

// protoScan 是一次全量扫描的结果（文件/包/service 均为声明序）。
type protoScan struct {
	Files    []protoFile
	Packages []string // 去重包名（声明序）
	Services []protoService
}

var (
	reProtoPackage  = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][A-Za-z0-9_.]*)\s*;`)
	reProtoService  = regexp.MustCompile(`(?m)^\s*service\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)
	reProtoRPC      = regexp.MustCompile(`\brpc\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	reProtoHTTPVerb = regexp.MustCompile(`\b(get|post|put|delete|patch)\s*:\s*"([^"]*)"`)
	reProtoHTTPBody = regexp.MustCompile(`\bbody\s*:\s*"([^"]*)"`)
	reProtoHTTPDot  = regexp.MustCompile(`\(google\.api\.http\)\.(get|post|put|delete|patch)\s*=\s*"([^"]*)"`)
)

// scanProto 正则扫描单个 proto 源码（gen domain 只需要 package / service /
// rpc / google.api.http 注解四个事实；正则口径对生成器与 buf format 风格的
// proto 都是确定性的——braced 注解与 (google.api.http).get 点式注解都识别）。
func scanProto(relPath string, src []byte) (*protoFile, error) {
	f := &protoFile{Path: relPath}
	if m := reProtoPackage.FindSubmatch(src); m != nil {
		f.Package = string(m[1])
	} else {
		return nil, fmt.Errorf("%s: no package declaration", relPath)
	}
	text := string(src)
	for _, loc := range reProtoService.FindAllStringSubmatchIndex(text, -1) {
		svc := protoService{Package: f.Package, Name: text[loc[2]:loc[3]]}
		block, _ := braceBlock(text, loc[1]-1) // loc[1] 指向 '{' 之后，回退一格
		for _, r := range reProtoRPC.FindAllStringSubmatchIndex(block, -1) {
			m := protoMethod{Name: block[r[2]:r[3]]}
			// 方法段 = 本 rpc 起点到下一 rpc（或 service 块尾）。
			end := len(block)
			if rest := reProtoRPC.FindStringSubmatchIndex(block[r[1]:]); rest != nil {
				end = r[1] + rest[0]
			}
			seg := block[r[1]:end]
			if i := strings.Index(seg, "(google.api.http)"); i >= 0 {
				if j := strings.Index(seg[i:], "{"); j >= 0 {
					if body, _ := braceBlock(seg, i+j); body != "" {
						m.Verb, m.Path, m.Body = parseHTTPRule(body)
					}
				}
			}
			if m.Verb == "" {
				// 点式注解回退：option (google.api.http).get = "/v1/x";
				if mm := reProtoHTTPDot.FindStringSubmatch(seg); mm != nil {
					m.Verb, m.Path = mm[2], mm[3]
				}
			}
			svc.Methods = append(svc.Methods, m)
		}
		f.Services = append(f.Services, svc)
	}
	return f, nil
}

// braceBlock 返回 text 中自 open（'{' 的下标）起的配对块内容与结束下标。
func braceBlock(text string, open int) (string, int) {
	depth := 0
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[open+1 : i], i
			}
		}
	}
	return "", -1
}

// parseHTTPRule 从 google.api.http 注解块提取动词/路径/body。
func parseHTTPRule(body string) (verb, path, b string) {
	if m := reProtoHTTPVerb.FindStringSubmatch(body); m != nil {
		verb, path = m[1], m[2]
	}
	if m := reProtoHTTPBody.FindStringSubmatch(body); m != nil {
		b = m[1]
	}
	return verb, path, b
}

// scanProtoRoots 扫描产出根下的全部候选 proto 根（存在即扫），返回聚合结果
// 与实际扫描的根清单。gen/ 生成目录跳过（pb 产物不是契约源）。
func scanProtoRoots(rootDir string) (*protoScan, []string, error) {
	scan := &protoScan{}
	var roots []string
	for _, r := range protoRoots {
		dir := filepath.Join(rootDir, filepath.FromSlash(r))
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		roots = append(roots, r)
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "gen" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".proto" {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(rootDir, path)
			if err != nil {
				rel = path
			}
			f, err := scanProto(filepath.ToSlash(rel), src)
			if err != nil {
				return err
			}
			scan.Files = append(scan.Files, *f)
			if !containsStr(scan.Packages, f.Package) {
				scan.Packages = append(scan.Packages, f.Package)
			}
			scan.Services = append(scan.Services, f.Services...)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return scan, roots, nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// validateDomainServices 是 Gate1 的 gen domain 强化：每个域的
// service 全名必须与实际 proto 匹配。报错指明哪个域的 service 在哪个 proto 里
// 缺失、该 proto 实际声明了什么（可行动：改声明或改 proto）。
func validateDomainServices(as *appspecv1.AppSpec, scan *protoScan, roots []string) []error {
	var errs []error
	for i, d := range as.GetDomains() {
		svcFull := d.GetService()
		if svcFull == "" {
			errs = append(errs, fmt.Errorf("domains[%d] (%s): service is required (field domains.service; gen domain derives handlers and authorization from it)", i, d.GetName()))
			continue
		}
		pkg := d.GetProtoPackage()
		if pkg == "" {
			pkg = svcFull
			if j := strings.LastIndexByte(pkg, '.'); j > 0 {
				pkg = pkg[:j]
			}
		}
		var files, svcs []string
		for _, f := range scan.Files {
			if f.Package != pkg {
				continue
			}
			files = append(files, f.Path)
			for _, s := range f.Services {
				svcs = append(svcs, s.Package+"."+s.Name)
			}
		}
		if len(files) == 0 {
			errs = append(errs, fmt.Errorf("domains[%d] (%s): proto package %q not found (scanned roots: %s; %d proto file(s) declaring packages: %s) -- "+
				"domains.service must be <package>.<Service> of a proto under the scanned roots", i, d.GetName(), pkg, strings.Join(roots, ", "), len(scan.Files), strings.Join(scan.Packages, ", ")))
			continue
		}
		if !containsStr(svcs, svcFull) {
			errs = append(errs, fmt.Errorf("domains[%d] (%s): service %q not found in proto package %q (%s declares: %s)",
				i, d.GetName(), svcFull, pkg, strings.Join(files, ", "), strings.Join(svcs, ", ")))
		}
	}
	return errs
}

// domainValuePresent 报告 dir 下非测试 .go 文件是否已声明导出值 Domain
// （go/parser 结构判定——域包已自带声明值时 gen domain 消费不覆盖）。
func domainValuePresent(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			return false, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				for _, name := range spec.(*ast.ValueSpec).Names {
					if name.Name == "Domain" {
						return true, nil
					}
				}
			}
		}
	}
	return false, nil
}

// domainDir 把域的 go_package import 路径折算为产出根下的包目录。
func domainDir(module, goPackage string) (string, error) {
	gp := goPackage
	if i := strings.IndexByte(gp, ';'); i >= 0 {
		gp = gp[:i]
	}
	if module != "" && strings.HasPrefix(gp, module+"/") {
		return filepath.FromSlash(strings.TrimPrefix(gp, module+"/")), nil
	}
	return "", fmt.Errorf("go_package %q is not under module %q (cannot locate the domain package directory)", goPackage, module)
}

// domainOptions 是 gen domain 叶子命令的领域配置。
type domainOptions struct {
	Spec         string // --spec：AppSpec JSON 路径（必填——域清单的唯一真相源）
	OutDir       string // --out：产出根（默认 .）
	Manifest     string // --manifest：域清单落盘路径（默认 internal/bootstrap/domains.gen.go）
	APIServerDir string // --apiserver-dir：apiserver 目录名（默认取 layout.apiserver_dir 否则 apiserver；传 - 跳过 apiserver 侧产物）
	Force        bool   // --force：覆盖已存在的 owned 文件

	IOStreams
}

func (o *domainOptions) Complete() error {
	if o.OutDir == "" {
		o.OutDir = "."
	}
	if o.Manifest == "" {
		o.Manifest = filepath.Join("internal", "bootstrap", "domains.gen.go")
	}
	return nil
}

func (o *domainOptions) Validate() error {
	if o.Spec == "" {
		return fmt.Errorf("the --spec <AppSpec.json> is required (domains[] is the declaration truth source)")
	}
	return nil
}

// Run 纯执行：Gate1 → proto 扫描与 service 校验 → 域包消费/骨架 seed → 落盘。
func (o *domainOptions) Run() error {
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
	scan, roots, err := scanProtoRoots(o.OutDir)
	if err != nil {
		return fmt.Errorf("scan domain protos: %w", err)
	}
	if err := guardErrs(validateDomainServices(as, scan, roots)); err != nil {
		return fmt.Errorf("Gate1 (domains.service vs proto): %w", err)
	}

	v := buildTreeView(as, normalizeBootstrap(as))
	v.Gen = `"bald gen domain"`
	if o.APIServerDir != "" && o.APIServerDir != "-" {
		v.APIDir = o.APIServerDir
		v.APIPkg = as.GetMeta().GetModule() + "/internal/" + o.APIServerDir
	}

	// 域包消费：已导出 Domain 的包跳过骨架（既有域包）；缺失则 seed 声明值骨架。
	for _, d := range as.GetDomains() {
		dir, err := domainDir(as.GetMeta().GetModule(), d.GetGoPackage())
		if err != nil {
			return err
		}
		pkgDir := filepath.Join(o.OutDir, dir)
		present, err := domainValuePresent(pkgDir)
		if err != nil {
			return fmt.Errorf("inspect domain package %s: %w", dir, err)
		}
		if present {
			fmt.Fprintln(o.Out, "skip:", pkgDir, "(domain value already declared)")
			continue
		}
		data, err := renderTreeTemplate(filepath.Base(dir)+"-domain", domainValueTmpl, map[string]string{
			"Name": d.GetName(), "Pkg": filepath.Base(dir),
		})
		if err != nil {
			return err
		}
		if err := writeFileFormatted(filepath.Join(pkgDir, "domain.gen.go"), data, Seeded, o.Force, o.IOStreams); err != nil {
			return err
		}
	}

	// 域清单（与 gen app 共用 bootstrapDomainsTmpl——单模板源不漂移）。
	v.Package = manifestPackage(o.Manifest)
	manifestRaw, err := renderTreeTemplate("bootstrap-domains", bootstrapDomainsTmpl, v)
	if err != nil {
		return err
	}
	if err := writeFileFormatted(filepath.Join(o.OutDir, o.Manifest), manifestRaw, Owned, o.Force, o.IOStreams); err != nil {
		return err
	}

	// apiserver 侧扇出（"-" 跳过——非标准目录树的宿主，如 _example）。
	if o.APIServerDir != "-" {
		if err := o.emitAPIServerSide(v, scan); err != nil {
			return err
		}
	}
	fmt.Fprintln(o.Out, "next: appspec.json is the declaration truth source; add a domain = hand-write its proto + domain package, declare it in domains[], rerun with --force (assembly files stay untouched)")
	return nil
}

// emitAPIServerSide 落盘 internal/<apiserver_dir>/ 的扇出与逐域 handler 产物。
func (o *domainOptions) emitAPIServerSide(v *treeView, scan *protoScan) error {
	emit := func(path, tmpl string, data any) error {
		raw, err := renderTreeTemplate(filepath.Base(path), tmpl, data)
		if err != nil {
			return err
		}
		return writeFileFormatted(filepath.Join(o.OutDir, path), raw, Owned, o.Force, o.IOStreams)
	}
	// 逐域 handler（委托域声明值自持实现 + 注解派生表）。
	byService := serviceIndex(scan)
	for _, d := range v.Spec.GetDomains() {
		name := title(d.GetName())
		goPkg := d.GetGoPackage()
		if err := emit(filepath.Join("internal", v.APIDir, "handler", "grpc", d.GetName()+".gen.go"), handlerGRPCTmpl,
			map[string]string{"Name": name, "GoPackage": goPkg, "Domain": d.GetName()}); err != nil {
			return err
		}
		svc := byService[d.GetService()]
		if err := emit(filepath.Join("internal", v.APIDir, "handler", "gin", d.GetName()+".gen.go"), handlerGinTmpl,
			map[string]any{"Name": name, "GoPackage": goPkg, "Domain": d.GetName(), "Routes": derivedRoutes(svc)}); err != nil {
			return err
		}
		if err := emit(filepath.Join("internal", v.APIDir, "handler", "convert", d.GetName()+".gen.go"), handlerConvertTmpl,
			map[string]any{"Name": name, "Service": d.GetService(), "Actions": derivedActions(svc)}); err != nil {
			return err
		}
	}
	// 扇出清单。
	if err := emit(filepath.Join("internal", v.APIDir, "domains.gen.go"), apiDomainFanoutTmpl, v); err != nil {
		return err
	}
	// 授权别名表 + 默认动作聚合（proto service 派生）。
	aliases, merged := derivedAuthz(v.Spec, byService)
	return emit(filepath.Join("internal", v.APIDir, "authz", "alias.gen.go"), authzAliasTmpl,
		map[string]any{"APIDir": v.APIDir, "Module": v.Module, "Aliases": aliases, "Actions": merged,
			"Domains": v.Spec.GetDomains(), "HasDomains": len(v.Spec.GetDomains()) > 0})
}

// serviceIndex 把扫描结果按 service 全名建索引。
func serviceIndex(scan *protoScan) map[string]protoService {
	idx := map[string]protoService{}
	for _, s := range scan.Services {
		idx[s.Package+"."+s.Name] = s
	}
	return idx
}

// derivedRoutes 把域 service 的 google.api.http 注解折算为 REST 路由登记
// （"VERB path[ (body: …)] -> RPC"；注解驱动，D2 §4.2 机制 1）。
func derivedRoutes(svc protoService) []string {
	var out []string
	for _, m := range svc.Methods {
		if m.Verb == "" {
			continue // 无 REST 映射的方法不进表
		}
		line := strings.ToUpper(m.Verb) + " " + m.Path
		if m.Body != "" {
			line += " (body: " + m.Body + ")"
		}
		out = append(out, line+" -> "+m.Name)
	}
	return out
}

// derivedActions 把域 service 的逐 RPC 授权动作折算为 map（key = FullMethod
// 去首斜杠；规则与 pkg/authz.DefaultGRPCAction 同源）。
func derivedActions(svc protoService) map[string]string {
	out := map[string]string{}
	for _, m := range svc.Methods {
		out[svc.Package+"."+svc.Name+"/"+m.Name] = grpcActionOf(m.Name)
	}
	return out
}

// grpcActionOf 与 pkg/authz.DefaultGRPCAction 同规则（生成期同源派生）。
func grpcActionOf(method string) string {
	switch {
	case strings.HasPrefix(method, "Delete"):
		return "delete"
	case strings.HasPrefix(method, "List"):
		return "list"
	case strings.HasPrefix(method, "Create"), strings.HasPrefix(method, "Update"),
		strings.HasPrefix(method, "Set"), strings.HasPrefix(method, "Put"):
		return "write"
	default:
		return "get"
	}
}

// grpcObjectOf 与 pkg/authz.DefaultGRPCObject 同规则：service 全名 → 资源名。
func grpcObjectOf(serviceFull string) string {
	svc := serviceFull
	if i := strings.LastIndexByte(svc, '.'); i >= 0 {
		svc = svc[i+1:]
	}
	return strings.ToLower(strings.TrimSuffix(svc, "Service"))
}

// httpObjectOf 与 pkg/authz.DefaultHTTPObject 同规则：注解路径 → 首个资源段。
func httpObjectOf(path string) string {
	for _, p := range strings.Split(strings.Trim(path, "/"), "/") {
		if p == "" || p == "v1" {
			continue
		}
		return p
	}
	return ""
}

// derivedAuthz 派生授权别名表（仅列 gRPC/HTTP 两侧归一化不一致的域——
// bald-admin 教训：冗余映射会掩盖真实命名问题）与逐 RPC 动作聚合。
func derivedAuthz(as *appspecv1.AppSpec, byService map[string]protoService) (map[string]string, map[string]string) {
	aliases := map[string]string{}
	merged := map[string]string{}
	for _, d := range as.GetDomains() {
		svc := byService[d.GetService()]
		grpcObj := grpcObjectOf(d.GetService())
		httpObj := ""
		for k, v := range derivedActions(svc) {
			merged[k] = v
		}
		for _, m := range svc.Methods {
			if httpObj == "" && m.Path != "" {
				httpObj = httpObjectOf(m.Path)
			}
		}
		if httpObj != "" && grpcObj != "" && grpcObj != httpObj {
			aliases[grpcObj] = httpObj
		}
	}
	return aliases, merged
}

// manifestPackage 由清单路径折算 package 子句（internal/bootstrap → bootstrap；
// 根级 domains.gen.go → main——_example 形态）。
func manifestPackage(manifest string) string {
	base := filepath.Base(filepath.Dir(filepath.FromSlash(manifest)))
	if base == "." || base == "" {
		return "main"
	}
	return base
}

// ---------------------------------------------------------------------------
// gen domain 产物模板（清单模板 bootstrapDomainsTmpl 与 gen app 共用，见
// appspec_gen.go——单模板源不漂移）。
// ---------------------------------------------------------------------------

// domainValueTmpl 是 <go_package>/domain.gen.go：域声明值骨架（生成一次；
// 域包尚无导出 Domain 时落盘，此后归用户维护——接线/Routes/Services 由用户
// 在本包的手写文件里实现后挂进此值，对照 _example/bald/greetv1/domain.go）。
var domainValueTmpl = `// Code generated once by bald gen domain; now maintained by you.
//
// {{ .Name }} 域的声明值骨架（W2 形态——包导出 appkit.Domain 值）。本文件
// 生成一次，此后归你维护：业务接线（Wire）、REST 路由（Routes）、gRPC 注册
// （Services）在本包的手写文件里实现后挂进此值；重新生成跳过本文件。
package {{ .Pkg }}

import "github.com/kalandramo/bald/pkg/appkit"

// Domain 是 {{ .Name }} 域的装配声明值（三个钩子均由引擎在既定时点消费，
// 见 pkg/appkit/domain.go：Wire 时点 = 业务 BeforeStart 之后、server 构建之前）。
var Domain = appkit.Domain{
	Name: {{ q .Name }},
}
`

// handlerGRPCTmpl 是 handler/grpc/<d>.gen.go：gRPC 注册委托（域自持实现）。
var handlerGRPCTmpl = `// Code generated by "bald gen domain". DO NOT EDIT.
//
// {{ .Domain }} 域的 gRPC 面扇出：注册委托域声明值自持实现
// （appkit.Domain.Services——引擎在 server 构建期经 composeDomainServices
// 调用；本函数供业务把注册收拢进自己的回调时取用）。
package grpc

import (
	"google.golang.org/grpc"

	{{ alias .GoPackage }} "{{ .GoPackage }}"
)

// Register{{ .Name }} 把 {{ .Domain }} 域的 gRPC service 注册进 server。
func Register{{ .Name }}(s *grpc.Server) {
	if {{ alias .GoPackage }}.Domain.Services == nil {
		return
	}
	{{ alias .GoPackage }}.Domain.Services(s)
}
`

// handlerGinTmpl 是 handler/gin/<d>.gen.go：REST 挂载委托 + 注解派生路由表。
var handlerGinTmpl = `// Code generated by "bald gen domain". DO NOT EDIT.
//
// {{ .Domain }} 域的 REST 面扇出：挂载委托域声明值自持实现
// （appkit.Domain.Routes——引擎在 Wire 之后、server 构建之前调用，gateway
// 转码面占用 server.http 端口时整体让位）；路由表从域 proto 的
// google.api.http 注解派生，供授权/审计/网关面对账。
package gin

import (
	"net/http"

	{{ alias .GoPackage }} "{{ .GoPackage }}"
)

// Mount{{ .Name }} 把 {{ .Domain }} 域的 REST 路由挂上应用 HTTP 面。
func Mount{{ .Name }}(h http.Handler) error {
	if {{ alias .GoPackage }}.Domain.Routes == nil {
		return nil
	}
	return {{ alias .GoPackage }}.Domain.Routes(h)
}

// {{ .Name }}Routes 是 {{ .Domain }} 域的 REST 路由登记（google.api.http 注解
// 派生；形态 "VERB path[ (body: …)] -> RPC"）。
var {{ .Name }}Routes = []string{
{{- range .Routes }}
	{{ q . }},
{{- end }}
}
`

// handlerConvertTmpl 是 handler/convert/<d>.gen.go：授权动作投影。
var handlerConvertTmpl = `// Code generated by "bald gen domain". DO NOT EDIT.
//
// {{ .Service }} 的授权动作投影（gen domain 产物；规则与
// pkg/authz.DefaultGRPCAction 同源——Delete→delete、List→list、
// Create/Update/Set/Put→write、其余→get）。授权策略默认行
// （internal/bootstrap/seed/policies.go）与 authz/alias.gen.go 以此对账。
package convert

// {{ .Name }}Actions 是逐 RPC 的授权动作（key = FullMethod 去首斜杠）。
var {{ .Name }}Actions = map[string]string{
{{- range $k, $v := .Actions }}
	{{ q $k }}: {{ q $v }},
{{- end }}
}
`

// apiDomainFanoutTmpl 是 internal/<apiserver_dir>/domains.gen.go 的扇出形态：
// 逐域 handler 挂载扇出。
var apiDomainFanoutTmpl = `// Code generated by {{ .Gen }}. DO NOT EDIT.
//
// 域声明的 apiserver 侧清单 + 逐域 handler 挂载扇出（AppSpec.domains 驱动）。
// W2 形态下路由与 gRPC 注册的执行权在引擎（域声明值自持：appkit.Domain.Routes
// / Services，经 internal/bootstrap/domains.gen.go 直传 MustServe）；本扇出是
// apiserver 侧的声明序镜像——每域恰好 1 行，挂载/注册收拢点（业务要把域路由
// 并进自己的 gin 链或 gRPC 回调时从这里取）。
package {{ .APIDir }}
{{- if .Spec.Domains }}

import (
	"net/http"

	"google.golang.org/grpc"

	ginhandler "{{ .Module }}/internal/{{ .APIDir }}/handler/gin"
	grpchandler "{{ .Module }}/internal/{{ .APIDir }}/handler/grpc"
)
{{- end }}
// DomainHandler 一个域在 apiserver 侧的 handler 扇出（挂载 + 注册）。
type DomainHandler struct {
	Name         string
	ProtoPackage string
	Service      string
	Model        string
	Mount        func(http.Handler) error // REST 面（域自持实现——appkit.Domain.Routes）
	Register     func(*grpc.Server)       // gRPC 面（域自持实现——appkit.Domain.Services）
}

// DomainHandlers 是 AppSpec.domains 声明序的逐域扇出（清单序 = 域装配次序）。
var DomainHandlers = []DomainHandler{
{{- range .Spec.Domains }}
	{
		Name: {{ q .Name }}, ProtoPackage: {{ q .ProtoPackage }}, Service: {{ q .Service }}, Model: {{ q .Model }},
		Mount:    ginhandler.Mount{{ title .Name }},
		Register: grpchandler.Register{{ title .Name }},
	},
{{- else }}
	// （AppSpec.domains 为空：暂无业务域。）
{{- end }}
}
`

// authzAliasTmpl 是 internal/<apiserver_dir>/authz/alias.gen.go：授权归一化
// 别名表 + 默认动作聚合（proto service 派生——D2 §2.4 #14）。
var authzAliasTmpl = `// Code generated by "bald gen domain". DO NOT EDIT.
//
// 授权归一化别名表 + 默认动作聚合（proto service/method 派生）。派生规则与
// pkg/authz 同源：
//   - object：DefaultGRPCObject（Service 段去 Service 后缀转小写）↔ HTTP 面
//     注解路径首段（DefaultHTTPObject）；两侧一致时**不入表**（bald-admin
//     教训：冗余映射会掩盖真实命名问题——只列差异）；
//   - action：DefaultGRPCAction（handler/convert 逐域表聚合，编译期对账）。
package authz
{{- if .HasDomains }}

import convert "{{ .Module }}/internal/{{ .APIDir }}/handler/convert"
{{- end }}
// ObjectAliases 是 object 归一化别名表（gRPC 面命名 → HTTP 面命名；仅列两侧
// 归一化结果不一致的域）。消费入口（行为不在生成物里）：contrib/authz-casbin
// 的 NewAlias——双向等价解析，桥接请求面命名与策略行命名空间（见该包文档）。
var ObjectAliases = map[string]string{
{{- range $k, $v := .Aliases }}
	{{ q $k }}: {{ q $v }},
{{- end }}
}

// DefaultActions 是全部域逐 RPC 的默认授权动作聚合（handler/convert 派生；
// internal/bootstrap/seed/policies.go 的默认策略行以它对账——
// object = pkg/authz.DefaultGRPCObject(fullMethod)）。
{{- if .HasDomains }}
var DefaultActions = mergeActions({{ range $i, $d := .Domains }}{{ if $i }}, {{ end }}convert.{{ title $d.Name }}Actions{{ end }})
{{- else }}
var DefaultActions = map[string]string{}
{{- end }}

// mergeActions 聚合多张动作表（FullMethod 键全局唯一；覆盖路径不可达，保留为防御）。
func mergeActions(ms ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
`

// genDomainCmd 组装 gen domain 叶子命令。
func genDomainCmd(streams IOStreams) *cobra.Command {
	o := &domainOptions{IOStreams: streams}
	cmd := &cobra.Command{
		Use:   "domain --spec <AppSpec.json>",
		Short: "按 AppSpec.domains 重写域驱动生成物（清单×2 + 逐域 handler + 授权别名）",
		Long: `消费 appspec.json 的 domains[] 与既有域 proto/域包，确定性重写域驱动生成物
（装配层评估 §4.6 新增域触碰清单——装配改动 0）：

  - internal/bootstrap/domains.gen.go            域清单聚合（appkit.Domain 变参直传）
  - internal/<apiserver_dir>/domains.gen.go      逐域 handler 挂载扇出
  - internal/<apiserver_dir>/handler/grpc/<d>.gen.go    gRPC 注册委托
  - internal/<apiserver_dir>/handler/gin/<d>.gen.go     REST 挂载委托 + 注解路由表
  - internal/<apiserver_dir>/handler/convert/<d>.gen.go 授权动作投影
  - internal/<apiserver_dir>/authz/alias.gen.go  授权别名表 + 默认动作聚合
  - <go_package>/domain.gen.go                   域声明值骨架（生成一次；域包
                                                 已有 Domain 声明时消费跳过）

Gate1 强化：domains[].service 必须与产出根下实际 proto 匹配（扫描 api/proto、
api/protos、proto），失配报错指明域、proto 文件与实际声明。全部产物 owned
（--force 覆盖）；域包骨架 seeded（生成一次）。`,
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
	cmd.Flags().StringVar(&o.Spec, "spec", "", "AppSpec JSON 路径（protojson；必填）")
	cmd.Flags().StringVar(&o.OutDir, "out", "", "产出根（默认 .）")
	cmd.Flags().StringVar(&o.Manifest, "manifest", "", "域清单落盘路径（默认 internal/bootstrap/domains.gen.go；_example 根级形态传 domains.gen.go）")
	cmd.Flags().StringVar(&o.APIServerDir, "apiserver-dir", "", "apiserver 目录名（默认 layout.apiserver_dir 否则 apiserver；传 - 跳过 apiserver 侧产物）")
	cmd.Flags().BoolVar(&o.Force, "force", false, "覆盖已存在的 owned 文件（默认拒绝）")
	_ = cmd.MarkFlagFilename("spec", ".json")
	cmd.SetErr(streams.ErrOut)
	return cmd
}
