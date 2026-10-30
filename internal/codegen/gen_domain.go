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
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// protoRoots 是 gen domain 扫描域 proto 的候选根（相对产出根，存在即扫；
// 覆盖 api/proto 目录树与 _example 的早期布局 proto）。
var protoRoots = []string{"api/proto", "api/protos", "proto"}

// protoMethod 是从 proto 源码解析出的一个 rpc 方法（含 google.api.http 注解
// 与请求/响应消息类型——--rest-bind 的绑定生成需要后两者）。
type protoMethod struct {
	Name      string
	Verb      string     // 主注解动词（get/post/put/delete/patch）；无注解为空
	Path      string     // 主注解路径（如 /v1/greet/{name}）
	Body      string     // 主注解 body（"*" 或字段掩码）；GET/DELETE 缺省无 body
	Request   string     // 请求消息类型（去 proto package 前缀；嵌套类型以 "_" 连接）
	Response  string     // 响应消息类型（同上）
	Streaming bool       // 任一方向流式（proto 的 stream 关键字）
	Rules     []httpRule // 全部 google.api.http 规则：主规则在前，additional_bindings 依次追加
	// Unsupported 是注解里出现、但 --rest-bind 未实现的字段（custom 动词、
	// response_body），去重后按字母序。生成期据此报错，避免生成的 REST 面
	// 与注解契约静默不一致。
	Unsupported []string
}

// httpRule 是一条 google.api.http 映射（主规则或 additional_bindings 条目）。
type httpRule struct {
	Verb string
	Path string
	Body string
}

// protoService 是解析出的一个 service 声明。
type protoService struct {
	Package   string
	Name      string
	Methods   []protoMethod
	GoPackage string // proto 文件的 option go_package（"path;alias"）原样
}

// protoFile 是解析出的一个 proto 文件。
type protoFile struct {
	Path      string // 相对产出根
	Package   string
	GoPackage string
	Services  []protoService
}

// protoScan 是一次全量扫描的结果（文件/包/service 均为声明序）。
type protoScan struct {
	Files    []protoFile
	Packages []string // 去重包名（声明序）
	Services []protoService
}

var (
	reProtoPackage = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][A-Za-z0-9_.]*)\s*;`)
	reProtoService = regexp.MustCompile(`(?m)^\s*service\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)
	reProtoRPC     = regexp.MustCompile(`\brpc\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(\s*(?:stream\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*\)\s*returns\s*\(\s*(?:stream\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*\)`)
	// reProtoStream 识别 rpc 签名里的 stream 关键字（任一方向）。
	reProtoStream    = regexp.MustCompile(`\bstream\s+[A-Za-z_]`)
	reProtoHTTPVerb  = regexp.MustCompile(`\b(get|post|put|delete|patch)\s*:\s*"([^"]*)"`)
	reProtoHTTPBody  = regexp.MustCompile(`\bbody\s*:\s*"([^"]*)"`)
	reProtoHTTPDot   = regexp.MustCompile(`\(google\.api\.http\)\.(get|post|put|delete|patch)\s*=\s*"([^"]*)"`)
	reProtoGoPackage = regexp.MustCompile(`(?m)^\s*option\s+go_package\s*=\s*"([^"]+)"\s*;`)
	// reProtoPathVar 匹配 google.api.http 路径变量（字段路径可含 "."，可带
	// "=pattern" 段）。
	reProtoPathVar = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_.]*)(=[^}]*)?\}`)
	// reGinParam 归一化 gin 路由里的变量名（:name → :），用于冲突 shape 判定。
	reGinParam = regexp.MustCompile(`:[A-Za-z0-9_]+`)
	// reProtoHTTPUnsupported 匹配 --rest-bind 未实现的注解字段（custom 动词、
	// response_body）。静默忽略会让生成的 REST 面与注解契约不一致，故扫描时
	// 记录、生成期报错。
	reProtoHTTPUnsupported = regexp.MustCompile(`\b(custom|response_body)\s*[:=]`)
	// reProtoHTTPDotField 匹配点式注解的字段名：(google.api.http).<field>。
	// 只用于补采未实现字段（custom / response_body），动词由 reProtoHTTPDot 处理。
	reProtoHTTPDotField = regexp.MustCompile(`\(google\.api\.http\)\.([A-Za-z_][A-Za-z0-9_]*)`)
)

// unsupportedAnnotationFields 返回注解块里 --rest-bind 未实现的字段名（去重
// 按字母序），供 bindViewOf 生成期报错。匹配须带 ":" 或 "="，因此
// `body: "custom"` 这类字符串取值不会误判。
func unsupportedAnnotationFields(text string) []string {
	var out []string
	for _, m := range reProtoHTTPUnsupported.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return dedupeSorted(out)
}

// dedupeSorted 原地去重并排序；空输入返回 nil（生成视图里不出现空列表）。
func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sort.Strings(in)
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// scanProto 正则扫描单个 proto 源码（gen domain 需要 package / go_package /
// service / rpc（含请求响应类型）/ google.api.http 注解；正则口径对生成器与
// buf format 风格的 proto 都是确定性的——braced 注解与
// (google.api.http).get 点式注解都识别）。
func scanProto(relPath string, src []byte) (*protoFile, error) {
	f := &protoFile{Path: relPath}
	if m := reProtoPackage.FindSubmatch(src); m != nil {
		f.Package = string(m[1])
	} else {
		return nil, fmt.Errorf("%s: no package declaration", relPath)
	}
	if m := reProtoGoPackage.FindSubmatch(src); m != nil {
		f.GoPackage = string(m[1])
	}
	text := string(src)
	for _, loc := range reProtoService.FindAllStringSubmatchIndex(text, -1) {
		svc := protoService{Package: f.Package, Name: text[loc[2]:loc[3]], GoPackage: f.GoPackage}
		block, _ := braceBlock(text, loc[1]-1) // loc[1] 指向 '{' 之后，回退一格
		for _, r := range reProtoRPC.FindAllStringSubmatchIndex(block, -1) {
			m := protoMethod{
				Name:      block[r[2]:r[3]],
				Request:   normalizeProtoType(f.Package, block[r[4]:r[5]]),
				Response:  normalizeProtoType(f.Package, block[r[6]:r[7]]),
				Streaming: reProtoStream.MatchString(block[r[0]:r[1]]),
			}
			// 方法段 = 本 rpc 起点到下一 rpc（或 service 块尾）。
			end := len(block)
			if rest := reProtoRPC.FindStringSubmatchIndex(block[r[1]:]); rest != nil {
				end = r[1] + rest[0]
			}
			seg := block[r[1]:end]
			if i := strings.Index(seg, "(google.api.http)"); i >= 0 {
				if j := strings.Index(seg[i:], "{"); j >= 0 {
					if body, _ := braceBlock(seg, i+j); body != "" {
						if rules := parseHTTPRules(body); len(rules) > 0 {
							m.Rules = rules
							m.Verb, m.Path, m.Body = rules[0].Verb, rules[0].Path, rules[0].Body
						}
						m.Unsupported = unsupportedAnnotationFields(body)
					}
				}
			}
			if m.Verb == "" {
				// 点式注解回退：option (google.api.http).get = "/v1/x";
				if mm := reProtoHTTPDot.FindStringSubmatch(seg); mm != nil {
					m.Verb, m.Path = mm[2], mm[3]
					m.Rules = []httpRule{{Verb: mm[2], Path: mm[3]}}
				}
			}
			// 点式注解未实现字段：option (google.api.http).custom… /
			// .response_body = "…"。与 braced 形态合并去重（两态不并存，
			// 合并只为让口径与顺序无关）。
			for _, mm := range reProtoHTTPDotField.FindAllStringSubmatch(seg, -1) {
				if mm[1] == "custom" || mm[1] == "response_body" {
					m.Unsupported = append(m.Unsupported, mm[1])
				}
			}
			m.Unsupported = dedupeSorted(m.Unsupported)
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

// parseHTTPRules 提取一条注解的全部规则：主规则 + additional_bindings 条目
// （同一 RPC 的别名路径，如 GET 与 POST 两个入口）。additional_bindings 不
// 允许再嵌套，单层扫描即可。
func parseHTTPRules(block string) []httpRule {
	var rules []httpRule
	if v, p, b := parseHTTPRule(block); v != "" {
		rules = append(rules, httpRule{Verb: v, Path: p, Body: b})
	}
	rest := block
	for {
		i := strings.Index(rest, "additional_bindings")
		if i < 0 {
			break
		}
		j := strings.Index(rest[i:], "{")
		if j < 0 {
			break
		}
		sub, end := braceBlock(rest, i+j)
		if end < 0 {
			break
		}
		if v, p, b := parseHTTPRule(sub); v != "" {
			rules = append(rules, httpRule{Verb: v, Path: p, Body: b})
		}
		rest = rest[end+1:]
	}
	return rules
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

// normalizeProtoType 把 proto 里的消息类型名折算为 Go 类型名：去 proto package
// 前缀，嵌套类型的分隔点换 "_"（protoc-gen-go 的命名规则）。
func normalizeProtoType(pkg, t string) string {
	t = strings.TrimPrefix(t, pkg+".")
	return strings.ReplaceAll(t, ".", "_")
}

// parseProtoGoPackage 拆 proto 的 option go_package（"path;alias" 或 "path"）：
// 未写别名时按 protoc-gen-go 口径取路径末段（非法标识符字符换成 "_"）。
func parseProtoGoPackage(gp string) (path, alias string) {
	path, alias = gp, ""
	if i := strings.IndexByte(gp, ';'); i >= 0 {
		path, alias = gp[:i], gp[i+1:]
	}
	if alias == "" {
		alias = path
		if i := strings.LastIndexByte(alias, '/'); i >= 0 {
			alias = alias[i+1:]
		}
	}
	alias = sanitizeGoIdent(alias)
	if alias == "" {
		alias = "pb"
	}
	return path, alias
}

// sanitizeGoIdent 把 import 别名收敛成合法 Go 标识符。
func sanitizeGoIdent(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ginPathOf 把 google.api.http 路径转成 gin 路由，并返回路径变量名（注解原名，
// 可含 "." 表示嵌套字段）。{name} 与单段 {name=pattern} 映射为 :name；跨段
// pattern（含 "/"，如 {name=projects/*/locations/*}）gin 无法表达，返回错误
// 而不是静默退化成单段匹配——那会让生成的 REST 面与注解契约不一致。
func ginPathOf(path string) (string, []string, error) {
	var vars []string
	var patternErr error
	out := reProtoPathVar.ReplaceAllStringFunc(path, func(m string) string {
		sub := reProtoPathVar.FindStringSubmatch(m)
		name := sub[1]
		if rule := strings.TrimPrefix(sub[2], "="); strings.Contains(rule, "/") {
			patternErr = fmt.Errorf("google.api.http variable %q has a multi-segment pattern %q; gin routes cannot express it", name, rule)
			return m
		}
		vars = append(vars, name)
		return ":" + ginParamName(name)
	})
	if patternErr != nil {
		return "", nil, patternErr
	}
	// gin 在同一路由里遇到重名参数会 panic（含嵌套字段归一化后的碰撞）。
	seen := map[string]string{}
	for _, v := range vars {
		g := ginParamName(v)
		if prev, ok := seen[g]; ok {
			return "", nil, fmt.Errorf("google.api.http variables %q and %q both map to gin parameter :%s", prev, v, g)
		}
		seen[g] = v
	}
	return out, vars, nil
}

// ginParamName 是 gin 的变量名（":" 后不允许 "."，嵌套字段改用 "_"）。
func ginParamName(protoVar string) string {
	return strings.ReplaceAll(protoVar, ".", "_")
}

// domainOptions 是 gen domain 叶子命令的领域配置。
type domainOptions struct {
	Spec         string // --spec：AppSpec JSON 路径（必填——域清单的唯一真相源）
	OutDir       string // --out：产出根（默认 .）
	Manifest     string // --manifest：域清单落盘路径（默认 internal/bootstrap/domains.gen.go）
	APIServerDir string // --apiserver-dir：apiserver 目录名（默认取 layout.apiserver_dir 否则 apiserver；传 - 跳过 apiserver 侧产物）
	Adopt        bool   // --adopt：覆盖已存在的 owned 文件（--force 的替代，B5）
	Check        bool   // --check：只校验不落盘（I8；有差异即失败）
	RESTBind     bool   // --rest-bind：从 google.api.http 注解生成 gin 绑定（需要 proto 已 buf generate）
	// NoRoutes 与 --rest-bind 连用：只产 handler 绑定，跳过 appkit.Route
	// 清单——域保留手写 RouteTable（Q5 的逃逸面：生成执行、清单自持）。
	NoRoutes bool

	ioStreams
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
	if o.Adopt && o.Check {
		return fmt.Errorf("--adopt and --check are mutually exclusive")
	}
	if o.NoRoutes && !o.RESTBind {
		// 清单只在 --rest-bind 下生成；单独给 --no-routes 是空操作，
		// 静默接受会让作者误以为关闭了某个默认行为。
		return fmt.Errorf("--no-routes only applies together with --rest-bind (route manifests are generated by --rest-bind)")
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

	var files []genFile

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
		skeleton := filepath.Join(dir, "domain.gen.go")
		if present {
			fmt.Fprintln(o.Out, "skip:", pkgDir, "(domain value already declared)")
			// 已生成的骨架仍入清单（seeded 只判存在性）：若首次生成后不再列出，
			// 清单字节会随「骨架已存在」而漂移，--check 永远不干净。消费者域包
			// 自带 Domain 且无 domain.gen.go 时则完全不入清单。
			if _, statErr := os.Stat(filepath.Join(pkgDir, "domain.gen.go")); statErr == nil {
				files = append(files, genFile{Path: skeleton, Own: Seeded})
			}
			continue
		}
		data, err := renderTreeTemplate(filepath.Base(dir)+"-domain", domainValueTmpl, map[string]string{
			"Name": d.GetName(), "Pkg": filepath.Base(dir),
		})
		if err != nil {
			return err
		}
		files = append(files, genFile{Path: skeleton, Own: Seeded, Raw: data, GoSrc: true})
	}

	// 域清单（与 gen app 共用 bootstrapDomainsTmpl——单模板源不漂移）。
	// 生成器标识用共享常量：同一路径在两命令下必须逐字节一致（见 domainsGenTag）。
	v.Package = manifestPackage(o.Manifest)
	mv := *v
	mv.Gen = domainsGenTag
	manifestRaw, err := renderTreeTemplate("bootstrap-domains", bootstrapDomainsTmpl, &mv)
	if err != nil {
		return err
	}
	files = append(files, genFile{Path: o.Manifest, Own: owned, Raw: manifestRaw, GoSrc: true})

	// apiserver 侧扇出（"-" 跳过——非标准目录树的宿主，如 _example）。
	if o.APIServerDir != "-" {
		if err := o.emitAPIServerSide(v, scan, &files); err != nil {
			return err
		}
	}

	_, genManifestRaw, err := buildManifest("bald gen domain", o.Spec, files)
	if err != nil {
		return err
	}
	if o.Check {
		return checkTree(o.OutDir, domainManifestName, files, genManifestRaw, o.ioStreams)
	}
	if err := emitTree(o.OutDir, files, o.Adopt, o.ioStreams); err != nil {
		return err
	}
	if o.Adopt {
		// 域被删除、或 --rest-bind/--no-routes 改变生成面时，上次清单里的
		// owned 旧产物必须退场（否则陈旧绑定继续编译、--check 又看不见）。
		if err := pruneStaleOwned(o.OutDir, domainManifestName, files, o.ioStreams); err != nil {
			return err
		}
	}
	if err := writeManifest(o.OutDir, domainManifestName, genManifestRaw, o.ioStreams); err != nil {
		return err
	}
	fmt.Fprintln(o.Out, "next: appspec.json is the declaration truth source; add a domain = hand-write its proto + domain package, declare it in domains[], rerun with --check to verify or --adopt to regenerate (assembly files stay untouched)")
	return nil
}

// bindView 是 --rest-bind 逐域绑定文件的模板数据。
type bindView struct {
	Name          string // 域名的 Go 形态（用于 Bind<Name>/<Name>Service）
	Domain        string
	Service       string // proto service 全名
	ProtoPkgAlias string // proto Go 包 import 别名
	ProtoPkgPath  string // proto Go 包 import 路径
	Methods       []bindMethodView
	Routes        []bindRouteView
	// RoutesPkgPath 是路由清单包（internal/<apidir>/routes）的 import 路径，
	// 供绑定文件头注释给出 Domain.RouteTable 的接线示例。
	RoutesPkgPath string
}

// bindMethodView 是服务接口的一个方法（按 RPC 名去重）。
type bindMethodView struct {
	RPC, Request, Response string
}

// bindRouteView 是 gin 路由登记的一项（一条 google.api.http 注解）。
type bindRouteView struct {
	RPC, Request, Verb, GinPath, Body, PathVarsExpr string
}

// bindViewOf 从域 service 的注解派生绑定视图；无 REST 注解时返回 nil（不产
// 绑定文件）。--rest-bind 需要 proto 的 Go 类型：go_package 缺失即报错——
// 生成器不猜 import 路径。
func bindViewOf(name string, d *appspecv1.DomainSpec, svc protoService) (*bindView, error) {
	var routes []bindRouteView
	var methods []bindMethodView
	seenRPC := map[string]bool{}
	seenRoute := map[string]string{}
	for _, m := range svc.Methods {
		if len(m.Unsupported) > 0 {
			// 未实现的注解字段若被静默跳过，REST 面就与注解契约不一致
			// （custom 动词整条丢弃、response_body 仍回整个消息）。宁可生成期
			// 失败，让作者显式手写这条路由或改用已实现形态。
			return nil, fmt.Errorf("domain %q: rpc %s carries google.api.http field(s) %s that --rest-bind does not implement (custom verbs and response_body would silently diverge from the annotation; hand-write this route instead)", d.GetName(), m.Name, strings.Join(m.Unsupported, ", "))
		}
		rules := methodRules(m)
		if len(rules) == 0 {
			continue
		}
		if m.Streaming {
			// grpc-gateway 不支持流式转码，gin 绑定同样无法承载——注解加在
			// 流式 RPC 上是 proto 侧的错配，生成期直接报。
			return nil, fmt.Errorf("domain %q: rpc %s is streaming but carries google.api.http annotations; streaming cannot be transcoded to REST", d.GetName(), m.Name)
		}
		for _, r := range rules {
			ginPath, vars, err := ginPathOf(r.Path)
			if err != nil {
				return nil, fmt.Errorf("domain %q: rpc %s: %w", d.GetName(), m.Name, err)
			}
			// gin 的同 method+shape 路由此处就会 panic（:a 与 :b 是同一 shape），
			// 生成期先报错——把运行期 panic 换成可定位的注解冲突。
			key := strings.ToUpper(r.Verb) + " " + reGinParam.ReplaceAllString(ginPath, ":")
			if prev, ok := seenRoute[key]; ok {
				return nil, fmt.Errorf("domain %q: %s is mapped by both %s and %s after converting google.api.http patterns to gin routes (gin cannot register two identical shapes)", d.GetName(), key, prev, m.Name)
			}
			seenRoute[key] = m.Name
			routes = append(routes, bindRouteView{
				RPC: m.Name, Request: m.Request, Verb: strings.ToUpper(r.Verb),
				GinPath: ginPath, Body: r.Body, PathVarsExpr: pathVarsExpr(vars),
			})
		}
		if !seenRPC[m.Name] {
			seenRPC[m.Name] = true
			methods = append(methods, bindMethodView{RPC: m.Name, Request: m.Request, Response: m.Response})
		}
	}
	if len(routes) == 0 {
		return nil, nil
	}
	if svc.GoPackage == "" {
		return nil, fmt.Errorf("domain %q: proto service %q has no option go_package; --rest-bind needs it to import the generated Go message types", d.GetName(), d.GetService())
	}
	protoPath, alias := parseProtoGoPackage(svc.GoPackage)
	return &bindView{
		Name: name, Domain: d.GetName(), Service: d.GetService(),
		ProtoPkgAlias: alias, ProtoPkgPath: protoPath,
		Methods: methods, Routes: routes,
	}, nil
}

// pathVarsExpr 生成路径变量 map 字面量（变量名是 proto 字段路径，取值来自
// gin 的路径参数）。
func pathVarsExpr(vars []string) string {
	if len(vars) == 0 {
		return "nil"
	}
	parts := make([]string, 0, len(vars))
	for _, v := range vars {
		parts = append(parts, fmt.Sprintf("%q: c.Param(%q)", v, ginParamName(v)))
	}
	return "map[string]string{" + strings.Join(parts, ", ") + "}"
}

// emitAPIServerSide 收集 internal/<apiserver_dir>/ 的扇出与逐域 handler 产物
// （落盘统一由 Run 的 emitTree 完成，manifest 与写入共享同一批字节）。
func (o *domainOptions) emitAPIServerSide(v *treeView, scan *protoScan, files *[]genFile) error {
	emit := func(path, tmpl string, data any) error {
		raw, err := renderTreeTemplate(filepath.Base(path), tmpl, data)
		if err != nil {
			return err
		}
		*files = append(*files, genFile{Path: path, Own: owned, Raw: raw, GoSrc: true})
		return nil
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
		if o.RESTBind {
			bind, err := bindViewOf(name, d, svc)
			if err != nil {
				return err
			}
			if bind != nil {
				if !o.NoRoutes {
					bind.RoutesPkgPath = v.Module + "/internal/" + v.APIDir + "/routes"
					// 计划可见性：把同一批注解派生为 appkit.Route 清单。域包直接
					// 引用它（RouteTable: routes.<Name>）即可让 --plan/doctor 列出
					// 路由、让跨域冲突在计划期报错——注解仍是唯一真相源，不重复
					// 维护。放在独立叶子包（只 import appkit）：域包不能 import
					// handler/gin（那里为了 Mount 反向 import 域包，会成环）。
					if err := emit(filepath.Join("internal", v.APIDir, "routes", d.GetName()+".gen.go"), routesTableTmpl, bind); err != nil {
						return err
					}
				}
				if err := emit(filepath.Join("internal", v.APIDir, "handler", "gin", d.GetName()+".bind.gen.go"), handlerBindTmpl, bind); err != nil {
					return err
				}
			}
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
// （"VERB path[ (body: …)] -> RPC"；注解驱动，D2 §4.2 机制 1）。同一 RPC 的
// additional_bindings 逐条登记。
func derivedRoutes(svc protoService) []string {
	var out []string
	for _, m := range svc.Methods {
		for _, r := range methodRules(m) {
			line := strings.ToUpper(r.Verb) + " " + r.Path
			if r.Body != "" {
				line += " (body: " + r.Body + ")"
			}
			out = append(out, line+" -> "+m.Name)
		}
	}
	return out
}

// methodRules 返回方法的注解规则；兼容只填了主规则的构造（测试/旧调用点）。
func methodRules(m protoMethod) []httpRule {
	if len(m.Rules) > 0 {
		return m.Rules
	}
	if m.Verb == "" {
		return nil
	}
	return []httpRule{{Verb: m.Verb, Path: m.Path, Body: m.Body}}
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
// 根级 domains.gen.go → main——早期 _example 形态）。
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
// 域包尚无导出 Domain 时落盘，此后归用户维护——声明与执行由用户在本包的手写
// 文件里实现后挂进此值，对照 _example/bald/greetv1/domain.go）。
var domainValueTmpl = `// Code generated once by bald gen domain; now maintained by you.
//
// {{ .Name }} 域的声明值骨架（包导出 appkit.Domain 值）。本文件
// 生成一次，此后归你维护：目标形态是 Declare（声明节点拓扑）+ ApplyNode
// （按计划序执行），业务配置段用 biz.<域名> 并以 ReloadReconcile +
// Reconcile 参与热更（设计 §4.6 / Q8）；迁移期仍可用兼容槽位
// Wire/Routes/Services。重新生成跳过本文件。
package {{ .Pkg }}

import "github.com/kalandramo/bald/pkg/appkit"

// Domain 是 {{ .Name }} 域的装配声明值（钩子由引擎在既定时点消费，见
// pkg/appkit/domain.go：域窗口 = 业务 BeforeStart 之后、server 构建之前）。
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

// handlerBindTmpl 是 handler/gin/<d>.bind.gen.go（--rest-bind）：从 google.api.http
// 注解与 rpc 签名生成 gin 路由 + 请求绑定 + 响应编码，替换手写 REST handler。
var handlerBindTmpl = `// Code generated by "bald gen domain --rest-bind". DO NOT EDIT.
//
// {{ .Domain }} 域的 REST 绑定（Q5）：路由、请求编解码、错误映射都从 proto 的
// google.api.http 注解与 rpc 签名生成——注解是唯一真相源，改 proto 后重新
// 生成，手写 REST handler 归零。前置条件（--rest-bind 显式开启的原因）：
//   - proto 的 Go 类型已由 buf generate 产出（本文件 import 其 go_package）；
//   - 消费者 module 依赖 github.com/kalandramo/bald/transport/web。
//
// 本文件是 appkit.Domain.Routes 的生成替代：把 Bind{{ .Name }}(engine, impl)
// 挂进装配即可；同时保留手写 Domain.Routes 会把同一路由注册两次。
{{- if .RoutesPkgPath }}
// 计划可见的路由清单在同批生成的 {{ .RoutesPkgPath }}（RouteTable: routes.{{ .Name }}）。
{{- else }}
// 路由清单按 --no-routes 跳过：计划可见性由手写 Domain.RouteTable 承担。
{{- end }}
package gin

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/transport/web"

	{{ .ProtoPkgAlias }} "{{ .ProtoPkgPath }}"
)

// {{ .Name }}Service 是从 {{ .Service }} 的 rpc 签名生成的 Go 接口，业务实现
// 后传入 Bind{{ .Name }}。
type {{ .Name }}Service interface {
{{- range .Methods }}
	{{ .RPC }}(ctx context.Context, req *{{ $.ProtoPkgAlias }}.{{ .Request }}) (*{{ $.ProtoPkgAlias }}.{{ .Response }}, error)
{{- end }}
}

// Bind{{ .Name }} 把 {{ .Service }} 的 REST 面挂到 r 上：路径/查询/body 按
// google.api.http 注解绑定（web.BindProtoRequest），响应按 protojson 编码
// （web.WriteProtoResponse，与 grpc-gateway 同形）。
//
// 计划可见性：{{ if .RoutesPkgPath }}同批注解已派生为 {{ $.RoutesPkgPath }} 的清单，域声明值
// 用 RouteTable: routes.{{ .Name }} 引用（见该文件头注释）——挂载与清单
// 同源，--plan/doctor 列出的就是这里注册的。
{{- else }}--no-routes 跳过了路由清单：Plan.Routes 由手写 Domain.RouteTable 承担，
// 本文件只负责实际注册（两边需人工保持一致）。
{{- end }}
func Bind{{ .Name }}(r gin.IRouter, svc {{ .Name }}Service) {
{{- range .Routes }}
	r.{{ .Verb }}("{{ .GinPath }}", func(c *gin.Context) {
		req := &{{ $.ProtoPkgAlias }}.{{ .Request }}{}
		if err := web.BindProtoRequest(c, req, {{ q .Body }}, {{ .PathVarsExpr }}); err != nil {
			web.ErrorResponse(c, err)
			return
		}
		resp, err := svc.{{ .RPC }}(c.Request.Context(), req)
		web.WriteProtoResponse(c, resp, err)
	})
{{- end }}
}
`

// routesTableTmpl 是 internal/<apidir>/routes/<d>.gen.go（--rest-bind）：同一批
// google.api.http 注解的 appkit.Route 形态。域声明值引用它即获得计划可见性
// （Domain.RouteTable → Plan.Routes、--plan/doctor、跨域冲突计划期报错）。
var routesTableTmpl = `// Code generated by "bald gen domain --rest-bind". DO NOT EDIT.
//
// {{ .Domain }} 域的 REST 路由清单（google.api.http 注解派生，与
// handler/gin/{{ .Domain }}.bind.gen.go 的实际注册同源）。域声明值引用它，
// 计划与 doctor 才能列出注解派生的路由：
//
//	import routes "{{ .RoutesPkgPath }}"
//
//	var Domain = appkit.Domain{
//		Name:       {{ q .Domain }},
//		RouteTable: routes.{{ .Name }},
//	}
//
// 本文件是清单不是实现（执行在 Bind{{ .Name }}）；同一域再手写 RouteTable
// 会得到「同一域重复声明同一路由」的计划期错误——二选一。
package routes

import "github.com/kalandramo/bald/pkg/appkit"

// {{ .Name }} 是 {{ .Domain }} 域的路由清单：Path 是实际注册的 gin 形态
// （路径变量写作 :name），Method 已归一化为大写。
var {{ .Name }} = []appkit.Route{
{{- range .Routes }}
	{Method: {{ q .Verb }}, Path: {{ q .GinPath }}, RPC: {{ q .RPC }}},
{{- end }}
}
`

// apiDomainFanoutTmpl 是 internal/<apiserver_dir>/domains.gen.go 的扇出形态：
// 逐域 handler 挂载扇出。
var apiDomainFanoutTmpl = `// Code generated by {{ .Gen }}. DO NOT EDIT.
//
// 域声明的 apiserver 侧清单 + 逐域 handler 挂载扇出（AppSpec.domains 驱动）。
// 路由与 gRPC 注册的执行权在引擎（域声明值自持：appkit.Domain.Routes /
// Services，经 internal/bootstrap/domains.gen.go 直传 MustServe）；本扇出是
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
func genDomainCmd(streams ioStreams) *cobra.Command {
	o := &domainOptions{ioStreams: streams}
	cmd := &cobra.Command{
		Use:   "domain --spec <AppSpec.json>",
		Short: "按 AppSpec.domains 重写域驱动生成物（清单×2 + 逐域 handler + 授权别名）",
		Long: `消费 appspec.json 的 domains[] 与既有域 proto/域包，确定性重写域驱动生成物
（新增域触碰清单——装配改动 0）：

  - internal/bootstrap/domains.gen.go            域清单聚合（appkit.Domain 变参直传）
  - internal/<apiserver_dir>/domains.gen.go      逐域 handler 挂载扇出
  - internal/<apiserver_dir>/handler/grpc/<d>.gen.go    gRPC 注册委托
  - internal/<apiserver_dir>/handler/gin/<d>.gen.go     REST 挂载委托 + 注解路由表
  - internal/<apiserver_dir>/handler/convert/<d>.gen.go 授权动作投影
  - internal/<apiserver_dir>/authz/alias.gen.go  授权别名表 + 默认动作聚合
  - <go_package>/domain.gen.go                   域声明值骨架（生成一次；域包
                                                 已有 Domain 声明时消费跳过）
  - internal/<apiserver_dir>/handler/gin/<d>.bind.gen.go  仅 --rest-bind：从
                                                 google.api.http 注解生成 gin 路由
                                                 与服务接口（替代手写 REST handler）
  - internal/<apiserver_dir>/routes/<d>.gen.go   仅 --rest-bind：同批注解的
                                                 appkit.Route 清单（域引用
                                                 RouteTable: routes.<Name> 即获
                                                 计划可见性）；--no-routes 跳过
                                                 该清单，保留手写 RouteTable

Gate1 强化：domains[].service 必须与产出根下实际 proto 匹配（扫描 api/proto、
api/protos、proto），失配报错指明域、proto 文件与实际声明。全部产物 owned
（--adopt 覆盖）；域包骨架 seeded（生成一次）。--check 零写校验生成物与
清单（domains.manifest.json）是否同声明一致。

--rest-bind 的前置条件：proto 的 Go 类型已由 buf generate 产出（bind 文件
import 其 go_package），消费者 module 依赖 bald/transport/web。默认关闭——
未跑 buf generate 的树保持可编译；开启后 bind 文件与其余产物一同纳入
--check 对账。--no-routes 与 --rest-bind 连用：只产 bind 文件，不产路由
清单（域保留手写 RouteTable）。`,
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
	cmd.Flags().StringVar(&o.Manifest, "manifest", "", "域清单落盘路径（默认 internal/bootstrap/domains.gen.go；早期根级形态传 domains.gen.go）")
	cmd.Flags().StringVar(&o.APIServerDir, "apiserver-dir", "", "apiserver 目录名（默认 layout.apiserver_dir 否则 apiserver；传 - 跳过 apiserver 侧产物）")
	cmd.Flags().BoolVar(&o.Adopt, "adopt", false, "覆盖已存在的 owned 文件并更新清单（默认拒绝）")
	cmd.Flags().BoolVar(&o.Check, "check", false, "只校验生成物与清单是否与声明一致（零写入，有差异即失败）")
	cmd.Flags().BoolVar(&o.RESTBind, "rest-bind", false, "从 google.api.http 注解生成 gin 路由与服务接口（需 proto Go 类型已 buf generate；custom 动词与 response_body 未实现，生成期报错）")
	cmd.Flags().BoolVar(&o.NoRoutes, "no-routes", false, "与 --rest-bind 连用：跳过 appkit.Route 清单，域保留手写 RouteTable（生成执行、清单自持）")
	_ = cmd.MarkFlagFilename("spec", ".json")
	cmd.SetErr(streams.ErrOut)
	return cmd
}
