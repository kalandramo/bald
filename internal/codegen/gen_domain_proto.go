package codegen

// gen_domain_proto.go 扫描并校验域 proto：从 proto 源码提取 package /
// go_package / service / rpc / google.api.http 注解，供 gen domain 的 Gate1
// 校验与绑定生成共用。正则口径对生成器与 buf format 风格的 proto 都是
// 确定性的。

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

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// protoRoots 是 gen domain 扫描域 proto 的候选根（相对产出根，存在即扫；
// 覆盖 api/proto 目录树与 _example 的早期布局 proto）。
var protoRoots = []string{"api/proto", "api/protos", "proto"}

// protoMethod 是从 proto 源码解析出的一个 rpc 方法（含 google.api.http 注解
// 与请求/响应消息类型——绑定生成需要后两者）。
type protoMethod struct {
	Name      string
	Verb      string     // 主注解动词（get/post/put/delete/patch）；无注解为空
	Path      string     // 主注解路径（如 /v1/greet/{name}）
	Body      string     // 主注解 body（"*" 或字段掩码）；GET/DELETE 缺省无 body
	Request   string     // 请求消息类型（去 proto package 前缀；嵌套类型以 "_" 连接）
	Response  string     // 响应消息类型（同上）
	Streaming bool       // 任一方向流式（proto 的 stream 关键字）
	Rules     []httpRule // 全部 google.api.http 规则：主规则在前，additional_bindings 依次追加
	// Unsupported 是注解里出现、但绑定生成未实现的字段（custom 动词、
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
	// reProtoHTTPUnsupported 匹配绑定生成未实现的注解字段（custom 动词、
	// response_body）。静默忽略会让生成的 REST 面与注解契约不一致，故扫描时
	// 记录、生成期报错。
	reProtoHTTPUnsupported = regexp.MustCompile(`\b(custom|response_body)\s*[:=]`)
	// reProtoHTTPDotField 匹配点式注解的字段名：(google.api.http).<field>。
	// 只用于补采未实现字段（custom / response_body），动词由 reProtoHTTPDot 处理。
	reProtoHTTPDotField = regexp.MustCompile(`\(google\.api\.http\)\.([A-Za-z_][A-Za-z0-9_]*)`)
)

// unsupportedAnnotationFields 返回注解块里绑定生成未实现的字段名（去重
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
