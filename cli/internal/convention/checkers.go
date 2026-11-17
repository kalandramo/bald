package convention

// 本文件是确定性检查器族（check.go 的 registry 在这里实现）。
//
// 三条设计约束：
//
//  1. **只收语法即可判定的规则**：不引 go/types、不做跨文件数据流——检查器要能在
//     任意消费者项目上跑，而消费者未必能 resolve 依赖；解析失败的文件直接跳过。
//  2. **误报比漏报贵**：`bald convention check` 在自家仓库（1200+ Go 文件）每次 CI
//     都跑，任何一条误报都会让门禁变成噪声。因此每条 checker 都写明「职责豁免」
//     与「形态豁免」，解释某个看似违规的写法为什么不报。
//  3. **一条 checker 绑一张卡的一条规则**（checkerRule 是显式绑定，docscheck 侧有
//     双向对账闸门），finding 必须带 file:line 与规则 ID——AI 拿到 ID 才能回查卡。

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// goSource 是一个已解析的源文件（rel 相对被检根，用于 finding 的 file 字段）。
// raw 是原文（文本类 checker 用）；file 为 nil 表示解析失败（非法 Go 归
// go build 管，AST 类不判、文本类按 raw 继续判）。
type goSource struct {
	rel  string
	raw  string
	file *ast.File
	fset *token.FileSet
}

// line 把 token 位置翻成行号。
func (s *goSource) line(p token.Pos) int { return s.fset.Position(p).Line }

// astWalk 是遍历形态的参数（消费方口径，见 goTree.selectFiles）。
type astWalk struct {
	includeTests bool     // 是否纳入 _test.go（testing-* 规则要，其余不要）
	exempt       []string // 相对路径前缀豁免：命中即不交回调（仍计入 scanned）
}

// astRule 是一条 checker 的输出版式（ruleID 来自 checkerRule 的绑定）。
type astRule struct {
	ruleID  string
	suggest string
}

// checkAST 是 AST 检查器的共用外壳：go.mod 缺失（含 dir 本身是文件）与服务
// 里没有 Go 源文件这两种情况都视为「环境不可判」——与 gen-app-drift 的 skip
// 语义同款：不是违规，也不是通过。
// 解析走 treeFor 共享缓存：全部 AST 类 checker 共享一次全树 walk+parse
// （此前每个 checker 独立遍历，一次检查 ≈ O(checkers × files) 次解析）。
func checkAST(dir string, w astWalk, r astRule, fn func(*goSource, func(token.Pos, string))) ([]Finding, string, error) {
	tree, err := treeFor(dir)
	if err != nil {
		return nil, "", err
	}
	if !tree.hasGoMod {
		return nil, "no go.mod in " + dir, nil
	}
	files := tree.selectFiles(w.includeTests, w.exempt)
	var findings []Finding
	for _, s := range files {
		fn(s, func(pos token.Pos, msg string) {
			findings = append(findings, Finding{
				RuleID:     r.ruleID,
				Level:      LevelMandatory,
				File:       s.rel,
				Line:       s.line(pos),
				Message:    msg,
				Suggestion: r.suggest,
			})
		})
	}
	if len(files) == 0 {
		return nil, "no go source files in " + dir, nil
	}
	return findings, "", nil
}

// importHit 返回首个满足 pred 的 import 路径及其位置。
func importHit(f *ast.File, pred func(string) bool) (token.Pos, string, bool) {
	for _, spec := range f.Imports {
		p := strings.Trim(spec.Path.Value, `"`)
		if pred(p) {
			return spec.Path.Pos(), p, true
		}
	}
	return token.NoPos, "", false
}

// isBaldPkg 报告 import 路径是否落在 <域>/bald/<sub> 或其子包。
// 例：sub = "pkg/appkit" 命中 ".../bald/pkg/appkit" 与 ".../bald/pkg/appkit/mech"。
func isBaldPkg(p, sub string) bool {
	return strings.HasSuffix(p, "/"+sub) || strings.Contains(p, "/"+sub+"/")
}

// layerNames 是生成树的内部层名集合。
var layerNames = map[string]bool{"biz": true, "store": true, "model": true, "handler": true}

// isInternalLayer 报告相对路径是否落在生成树的内部分层目录
// internal/<app>/<layer>（layer 取 biz|store|model|handler）。
//
// 只认 internal/ 之下：库本体 pkg/store、contrib/store-gorm 不是「业务层」，
// 它们的 import 由各自模块治理——这是本判定的职责豁免。
//
// 层名匹配只认**第一个**层名段：internal/handler/store/x.go 是 handler 的投影
// 子包（主题子包），不是业务层——曾经继续往后扫把深层 "store" 也判成业务层，
// 让投影边界 import httperr 被误报（adv-checkers FP2）。第一个层名段若是
// internal 的直接子段（缺应用名，如 internal/biz/x.go）则整个文件不判，避免
// 误伤非生成树的自由布局。
func isInternalLayer(rel, layer string) bool {
	segs := strings.Split(rel, "/")
	inInternal := false
	for i, s := range segs {
		if !inInternal {
			if s == "internal" {
				inInternal = true
			}
			continue
		}
		if layerNames[s] {
			return i > 1 && s == layer
		}
	}
	return false
}

// isBusinessLayer 报告文件是否在业务层（biz/store/model）。
func isBusinessLayer(rel string) bool {
	return isInternalLayer(rel, "biz") || isInternalLayer(rel, "store") || isInternalLayer(rel, "model")
}

// checkBerrorsWithDiscard 钉 errors-r03：With* 是不可变 builder，必须接住返回值
// 链式派生；就地调用丢弃返回值 = sentinel 污染（跨请求串数据的真实 bug 源）。
//
// 形态判定：**语句级**（ExprStmt）调用且最外层选择子以 With 开头，且接收链的
// 根确实是 berrors 包（直接 `berrors.New(...).With...` 或根变量由 berrors 链赋值）。
// 曾按「文件 import 了 berrors」对全文件任何 .With* 开火——自有可变 builder
// （就地改字段返回 self）只因同文件恰好 import berrors 就被误报（adv-checkers FP3）。
// 链根追踪是单文件内的语法判定，不做跨文件数据流。
func checkBerrorsWithDiscard(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["berrors-with-discard"],
		suggest: "接住返回值（e = e.WithDetails(...)）或直接链式传给调用方；丢弃返回值会让已构造的错误实例被后续请求复用",
	}, func(s *goSource, add func(token.Pos, string)) {
		alias := ""
		for _, spec := range s.file.Imports {
			p := strings.Trim(spec.Path.Value, `"`)
			if strings.HasSuffix(p, "/bald/berrors") || p == "github.com/kalandramo/bald/berrors" {
				// 默认包名 = 路径末段（go import 语义）；显式别名优先。
				alias = p[strings.LastIndexByte(p, '/')+1:]
				if spec.Name != nil {
					alias = spec.Name.Name
				}
				break
			}
		}
		if alias == "" {
			return // 只认 berrors 根包本身；httperr/grpcerr 桥接子包归 errors-r01 管
		}
		// 第一遍：收集「根是 berrors 链」的变量（e := berrors.New(...)）。
		berrorsVars := map[string]bool{}
		// 链根是 berrors 包名本身（berrors.New(...) 的链根是 Ident(berrors)）。
		rootIsBerrors := func(expr ast.Expr) bool {
			root := chainRoot(expr)
			id, ok := root.(*ast.Ident)
			return ok && id.Name == alias
		}
		collect := func(lhs []ast.Expr, rhs ast.Expr) {
			if len(lhs) != 1 {
				return
			}
			id, ok := lhs[0].(*ast.Ident)
			if !ok || id.Name == "_" {
				return
			}
			if rootIsBerrors(rhs) {
				berrorsVars[id.Name] = true
			}
		}
		ast.Inspect(s.file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.AssignStmt:
				if len(node.Rhs) == 1 {
					collect(node.Lhs, node.Rhs[0])
				}
			case *ast.GenDecl:
				if node.Tok != token.VAR {
					return true
				}
				for _, spec := range node.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Values) != 1 || len(vs.Names) != 1 {
						continue
					}
					if rootIsBerrors(vs.Values[0]) {
						berrorsVars[vs.Names[0].Name] = true
					}
				}
			}
			return true
		})
		// 第二遍：语句级 With* 调用且链根是 berrors（直接或经收集的变量）。
		ast.Inspect(s.file, func(n ast.Node) bool {
			es, ok := n.(*ast.ExprStmt)
			if !ok {
				return true
			}
			call, ok := es.X.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !strings.HasPrefix(sel.Sel.Name, "With") || sel.Sel.Name == "With" {
				return true
			}
			root := chainRoot(call.Fun)
			berrors := false
			if id, ok := root.(*ast.Ident); ok {
				berrors = id.Name == alias || berrorsVars[id.Name]
			}
			if berrors {
				add(call.Pos(), sel.Sel.Name+" 的返回值被丢弃（errors-r03：With* 是不可变 builder）")
			}
			return true
		})
	})
}

// chainRoot 沿调用/选择子链上溯到根表达式（berrors.New(...).WithX(...) 的根是
// berrors）。
func chainRoot(expr ast.Expr) ast.Expr {
	for {
		switch e := expr.(type) {
		case *ast.CallExpr:
			expr = e.Fun
		case *ast.SelectorExpr:
			expr = e.X
		default:
			return expr
		}
	}
}

// checkErrorsBridgeImport 钉 errors-r01 的 import 面：产生（biz 构造）与匹配
// （Is 分支）只 import 根包 berrors；grpcerr/httperr 只出现在传输投影边界。
// 判定按**目录**而非类型：业务层（biz/store/model）出现桥接子包 import 即违规。
// handler/gateway 是投影边界，不判。
func checkErrorsBridgeImport(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["errors-bridge-import"],
		suggest: "业务层只 import 根包 berrors；HTTP/gRPC 投影放到 handler 或 gateway 边界（berrors/httperr、berrors/grpcerr）",
	}, func(s *goSource, add func(token.Pos, string)) {
		if !isBusinessLayer(s.rel) {
			return
		}
		pos, p, ok := importHit(s.file, func(p string) bool {
			return isBaldPkg(p, "bald/berrors/grpcerr") || isBaldPkg(p, "bald/berrors/httperr")
		})
		if !ok {
			return
		}
		add(pos, "业务层 import 传输桥接包 "+p+"（errors-r01）")
	})
}

// checkAssemblyImportInBusiness 钉 reuse-r03：装配知识不进业务目录。
// 业务层 import pkg/appkit（含 mech）或 bootstrap 即违规——那意味着业务包在
// 自己接线，装配判断出现了第二份（判据 J2 的重复税）。
func checkAssemblyImportInBusiness(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["assembly-import-in-biz"],
		suggest: "业务包不 import 装配层：接线写在域声明的 Wire 槽位或 Provider，业务只声明业务（判据 J2）",
	}, func(s *goSource, add func(token.Pos, string)) {
		if !isBusinessLayer(s.rel) {
			return
		}
		pos, p, ok := importHit(s.file, func(p string) bool {
			return isBaldPkg(p, "bald/pkg/appkit") || isBaldPkg(p, "bald/bootstrap")
		})
		if !ok {
			return
		}
		add(pos, "业务层 import 装配层 "+p+"（reuse-r03：装配知识不进业务目录）")
	})
}

// checkCtxStringKey 钉 context-r01 最硬的一档：context.WithValue 用**字符串**
// 做键——跨包撞键的经典来源。类型键（ctxKey{}）是 Go 官方推荐写法，但 bald 的
// 统一面是 pkg/contextx 的五个标准键，类型键这一档交评审（无法从语法判断它是否
// 绕开了 contextx）；测试文件不判（用例要构造私有键模拟外部上下文）。
//
// 字符串形态收两档（adv-checkers M1 补漏）：字面量，以及**未类型化**的字符串
// 常量键（const reqIDKey = "acme/request-id" 后用作键——撞键风险与字面量同等，
// 曾只判字面量形态让它整条漏掉）。带类型声明的常量（const k ctxKey = "..."）
// 是类型键写法，不判。
func checkCtxStringKey(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["ctx-string-key"],
		suggest: "用 pkg/contextx 的 With/From 对（user/username/trace_id/request_id/tenant_id）；自有数据不要塞 context",
	}, func(s *goSource, add func(token.Pos, string)) {
		// 收集未类型化的字符串常量名：const x = "..."（无类型声明）。
		strConsts := map[string]bool{}
		ast.Inspect(s.file, func(n ast.Node) bool {
			gd, ok := n.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || vs.Type != nil || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				if lit, ok := vs.Values[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					strConsts[vs.Names[0].Name] = true
				}
			}
			return true
		})
		ast.Inspect(s.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WithValue" {
				return true
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok || x.Name != "context" {
				return true
			}
			switch key := call.Args[1].(type) {
			case *ast.BasicLit:
				if key.Kind != token.STRING {
					return true
				}
				add(call.Pos(), "context.WithValue 用字符串字面量 "+key.Value+" 做键（context-r01：禁止私有 context 键）")
			case *ast.Ident:
				if strConsts[key.Name] {
					add(call.Pos(), "context.WithValue 用未类型化字符串常量 "+key.Name+" 做键（context-r01：禁止私有 context 键）")
				}
			}
			return true
		})
	})
}

// checkValidateSignature 钉 validation-r02 的方法名约定：Validate* 必须是
// func(recv T, ctx context.Context, req *XxxRequest) error。
//
// 豁免（adv-checkers FP5）：
//   - 生成文件（*.pb.go / *.pb.validate.go / *.gen.go / *.pb.gw.go）——
//     protoc-gen-validate 的 ValidateAll 是每个生成文件都有的零参方法，判它
//     等于逼人生成器打补丁；
//   - 没有任何 *XxxRequest 形态参数的方法——领域校验器（ValidateEmail(string)
//     这类常见命名）与「请求校验」是两回事，名字撞了但约定辖不到它。
//     裸 Validate() 同理不判。
//
// 报告条件因此收窄为：ValidateXxx + 方法参数里存在 *XxxRequest + 签名不符约定。
func checkValidateSignature(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["validate-signature"],
		suggest: "改成 func (r T) ValidateXxx(ctx context.Context, req *XxxRequest) error；签名不符必须显式报错，不许静默跳过",
	}, func(s *goSource, add func(token.Pos, string)) {
		base := filepath.Base(s.rel)
		for _, suffix := range []string{".pb.go", ".pb.validate.go", ".pb.gw.go", ".gen.go"} {
			if strings.HasSuffix(base, suffix) {
				return
			}
		}
		ast.Inspect(s.file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Type == nil {
				return true
			}
			name := fn.Name.Name
			if !strings.HasPrefix(name, "Validate") || name == "Validate" {
				return true
			}
			if !hasRequestPointerParam(fn.Type) {
				return true
			}
			if validateSignatureOK(fn.Type) {
				return true
			}
			add(fn.Name.Pos(), name+" 的签名不符 validation-r02 约定（应为 (ctx context.Context, req *XxxRequest) error）")
			return true
		})
	})
}

// hasRequestPointerParam 报告参数表里是否有 *XxxRequest 形态的参数（识别
// 「请求校验器」的语法特征）。
func hasRequestPointerParam(t *ast.FuncType) bool {
	if t.Params == nil {
		return false
	}
	for _, fld := range t.Params.List {
		star, ok := fld.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if id, ok := star.X.(*ast.Ident); ok && strings.HasSuffix(id.Name, "Request") {
			return true
		}
	}
	return false
}

// validateSignatureOK 判定 (ctx context.Context, req *T) error 形态。
func validateSignatureOK(t *ast.FuncType) bool {
	if t.Params == nil || t.Results == nil {
		return false
	}
	if t.Params.NumFields() != 2 || t.Results.NumFields() != 1 {
		return false
	}
	// 第一个参数必须是 context.Context。
	first := t.Params.List[0].Type
	sel, ok := first.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "context" || sel.Sel.Name != "Context" {
		return false
	}
	// 第二个参数必须是指针（*XxxRequest）。
	if _, ok := t.Params.List[1].Type.(*ast.StarExpr); !ok {
		return false
	}
	// 返回值必须是 error。
	res := t.Results.List[0].Type
	id, ok := res.(*ast.Ident)
	return ok && id.Name == "error"
}

// checkStoreTenantInit 钉 store-r02：多租户隔离必须显式注册，禁止 init()
// 隐式注册——init 里的注册顺序不可见、不可测、无法在装配计划里表达（判据 J5
// 「显式声明不推断」）。
//
// 职责豁免（adv-checkers FP6）：只判 import 了 bald store 的文件——
// init() 里调自家 SaaS 配置表的 cfg.RegisterTenant(...) 与 pkg/store 无关，
// 「方法恰好叫 RegisterTenant」不构成管辖依据。
func checkStoreTenantInit(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["store-tenant-init"],
		suggest: "移到装配层显式调用 store.RegisterTenant(\"tenant_id\", store.DefaultTenantFunc)；注册时机要能被 plan/doctor 看见",
	}, func(s *goSource, add func(token.Pos, string)) {
		if _, _, ok := importHit(s.file, func(p string) bool {
			return isBaldPkg(p, "bald/pkg/store") || isBaldPkg(p, "bald/contrib/store")
		}); !ok {
			return
		}
		ast.Inspect(s.file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "init" || fn.Body == nil {
				return true
			}
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "RegisterTenant" {
					add(call.Pos(), "init() 里隐式注册多租户（store-r02：禁止 init 隐式注册）")
				}
				return true
			})
			return true
		})
	})
}

// checkAssemblyPlanImpure 钉 assembly-r01：Plan 必须是纯函数（不写文件、不发
// 网络、不改全局、不读环境），Build 是唯一允许副作用的入口。
//
// 只判五类动静：os 的环境读取与文件写入、net.Dial*、http 请求、time 的时间读取、
// rand 的随机数——后两类是 doctor-r07 点名的确定性破坏源（plan hash 每次不同，
// doctor-r06 把 plan hash 当 CI 稳定判据；adv-checkers M2）。日志调用不判——
// Plan 里 Debug 一行不改变装配拓扑，判它会把可观测性逼成违禁品。
//
// 职责豁免（adv-checkers FP8）：只判 import 了装配层的文件——Provider.Plan 的
// 实现方必然 import appkit/bootstrap；自家 RoutePlanner 的 Plan 方法与装配无关，
// 「方法恰好叫 Plan」不构成管辖依据。
func checkAssemblyPlanImpure(dir string) ([]Finding, string, error) {
	osEnv := map[string]bool{"Getenv": true, "LookupEnv": true, "Setenv": true, "Unsetenv": true, "Environ": true}
	osWrite := map[string]bool{"WriteFile": true, "Create": true, "OpenFile": true, "Mkdir": true, "MkdirAll": true, "Remove": true, "RemoveAll": true, "Rename": true, "Chmod": true}
	netCalls := map[string]bool{"Dial": true, "DialTimeout": true, "DialContext": true}
	httpCalls := map[string]bool{"Get": true, "Post": true, "PostForm": true, "Head": true, "Do": true}
	timeCalls := map[string]bool{"Now": true, "Since": true, "Until": true, "After": true, "NewTicker": true, "NewTimer": true, "Tick": true, "Date": true}
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["assembly-plan-impure"],
		suggest: "把副作用搬进同一节点的 Build（唯一的副作用入口）；Plan 只算拓扑，返回值必须与运行环境无关",
	}, func(s *goSource, add func(token.Pos, string)) {
		if _, _, ok := importHit(s.file, func(p string) bool {
			return isBaldPkg(p, "bald/pkg/appkit") || isBaldPkg(p, "bald/bootstrap")
		}); !ok {
			return
		}
		ast.Inspect(s.file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != "Plan" || fn.Body == nil {
				return true
			}
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				var kind string
				switch {
				case pkg.Name == "os" && osEnv[sel.Sel.Name]:
					kind = "读环境"
				case pkg.Name == "os" && osWrite[sel.Sel.Name]:
					kind = "写文件"
				case pkg.Name == "net" && netCalls[sel.Sel.Name]:
					kind = "发网络"
				case pkg.Name == "http" && httpCalls[sel.Sel.Name]:
					kind = "发网络"
				case pkg.Name == "time" && timeCalls[sel.Sel.Name]:
					kind = "读时间"
				case pkg.Name == "rand":
					kind = "读随机数"
				}
				if kind != "" {
					add(call.Pos(), "Plan 里 "+pkg.Name+"."+sel.Sel.Name+"（assembly-r01：Plan "+kind+"，不是纯函数）")
				}
				return true
			})
			return true
		})
	})
}

// checkHTTPEnvelopeShape 钉 errors-r08 的可判定面：同时带 code/data/message 三个
// json 标签的结构体 = {code,data,message} 全 200 信封。
//
// 正确的 StatusBody 是 code/message/details（google.rpc.Status 形状），键集不同，
// 天然不命中——这条规则因此可以机械判，不会误伤正确实现。
//
// 豁免（adv-checkers FP4）：同文件里作为 json.Unmarshal 解码目标出现的同名类型
// ——那是**外部契约的镜像**（第三方支付/开放平台的响应 wire 形状就是
// {code,data,message}，解析它必须同形），不是自家响应面。按用法区分 producer/
// consumer 是这条规则唯一能落地的口径：定义处本身无法区分。
func checkHTTPEnvelopeShape(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["http-envelope-shape"],
		suggest: "错误响应体用 transport/web 的 StatusBody/StatusOf（google.rpc.Status JSON，三面一份契约），HTTP 状态码承载成败而非 body",
	}, func(s *goSource, add func(token.Pos, string)) {
		// 先收集同文件变量的声明类型（var r thirdPartyResp / r := &T{...}）。
		varTypes := map[string]string{}
		ast.Inspect(s.file, func(n ast.Node) bool {
			gd, ok := n.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if vs.Type != nil && i == 0 {
						varTypes[name.Name] = exprTypeName(vs.Type)
					}
				}
			}
			return true
		})
		// 收集同文件 json.Unmarshal 调用实参里出现的标识符名及其声明类型
		// （解码目标：&r 的类型是 thirdPartyResp）。
		decoded := map[string]bool{}
		ast.Inspect(s.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Unmarshal" {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "json" {
				return true
			}
			for _, arg := range call.Args {
				ast.Inspect(arg, func(m ast.Node) bool {
					id, ok := m.(*ast.Ident)
					if !ok {
						return true
					}
					decoded[id.Name] = true
					if t := varTypes[id.Name]; t != "" {
						decoded[t] = true
					}
					return true
				})
			}
			return true
		})
		ast.Inspect(s.file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			tags := map[string]bool{}
			for _, fld := range st.Fields.List {
				if fld.Tag == nil {
					continue
				}
				if name, ok := jsonTagName(fld.Tag.Value); ok {
					tags[name] = true
				}
			}
			if tags["code"] && tags["data"] && tags["message"] && !decoded[ts.Name.Name] {
				add(ts.Name.Pos(), ts.Name.Name+" 是 {code,data,message} 全 200 信封（errors-r08）")
			}
			return true
		})
	})
}

var jsonTagRe = regexp.MustCompile(`json:"([^",]+)`)

// exprTypeName 取类型表达式的名字（Ident 直取，SelectorExpr 取 Sel）。
func exprTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.StarExpr:
		return exprTypeName(t.X)
	}
	return ""
}

// jsonTagName 取结构体字段 tag 里的 json 名。
func jsonTagName(tag string) (string, bool) {
	m := jsonTagRe.FindStringSubmatch(tag)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// checkTransportStopBackground 钉 transport-r06：Stop(ctx) 必须使用调用方传入的
// ctx（含超时），禁止用 context.Background() 调 Shutdown 丢弃超时。
//
// 判定收窄到**实参**：只报「传给 Shutdown 的实参子树里出现 context.Background()/
// TODO()」的形态——包括直接传与 WithTimeout(Background) 包一层（后者同样丢弃了
// 调用方超时）。Stop 里用 Background 做与 Shutdown 无关的兜底清理（标准最佳实践）
// 不再命中：曾按「同一函数体同时出现 Shutdown 与 Background」判，正好把注释承诺
// 豁免的形态判了（adv-checkers FP1）。变量中转（b := context.Background()）判不到，
// 属漏报——可接受（误报比漏报贵）。
func checkTransportStopBackground(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["transport-stop-background"],
		suggest: "把调用方 ctx 透传给 Shutdown；需要兜底超时由调用方设 deadline，不在 Stop 里换 Background",
	}, func(s *goSource, add func(token.Pos, string)) {
		ast.Inspect(s.file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != "Stop" || fn.Body == nil {
				return true
			}
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Shutdown" {
					return true
				}
				for _, arg := range call.Args {
					if ctxIdentArg(arg) {
						add(arg.Pos(), "Stop 用 context.Background/TODO 调 Shutdown（transport-r06：丢弃调用方超时）")
						break
					}
				}
				return true
			})
			return true
		})
	})
}

// ctxIdentArg 报告表达式子树里是否出现 context.Background()/context.TODO()。
func ctxIdentArg(expr ast.Expr) bool {
	hit := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if hit {
			return false
		}
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "context" &&
			(sel.Sel.Name == "Background" || sel.Sel.Name == "TODO") {
			hit = true
			return false
		}
		return true
	})
	return hit
}

// bareAddrRe 匹配裸端口地址字面量（":8080"），127.0.0.1:8080 不命中。
var bareAddrRe = regexp.MustCompile(`^:[0-9]+$`)

// listenCallees 是「真在监听/拨号」的函数名（ListenAndServe、net.Listen、
// grpc.Dial 家族）。testing-r03 的坑是**客户端把 ":port" 解析成 IPv6**，
// 所以只有这些调用位上的裸端口才算违规——配置样例里的 addr: ":8080" 是声明
// 值，没人拿它当客户端地址，判它就是误报（本仓 16 条全是这种形态）。
var listenCallees = map[string]bool{
	"Listen": true, "ListenPacket": true, "ListenAndServe": true, "ListenAndServeTLS": true,
	"Dial": true, "DialContext": true, "DialTimeout": true, "DialTCP": true, "DialUDP": true,
}

// checkTestingBareAddr 钉 testing-r03：测试监听/拨号地址必须带 127.0.0.1 前缀。
// 裸 :port 会被 gRPC 客户端解析成 IPv6 导致 connection refused（本仓踩过的坑）。
//
// 判定只认一类位置：监听/拨号调用的实参。曾经还判 *Server 字面量的 Addr 字段，
// 但「配置样例里的 addr: \":8080\" 是声明值……判它就是误报」正是注释承诺的豁免，
// 字面量判定位恰好判了它（adv-checkers FP9）——自有 Server 配置结构的形状断言
// 从不监听不拨号。按注释承诺删掉该判定位。
// 豁免 ":0"：端口由内核分配，客户端必然回读实际地址，不存在解析成 IPv6 的坑
// ——这一档判它会把「先 Listen 再取 Addr」的标准写法整体判红。
func checkTestingBareAddr(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{includeTests: true}, astRule{
		ruleID:  checkerRule["testing-bare-addr"],
		suggest: "用 pkg/testkit 的 FreeAddr(t) 取地址；需要写死时用 127.0.0.1:port，不要裸 :port",
	}, func(s *goSource, add func(token.Pos, string)) {
		if !strings.HasSuffix(s.rel, "_test.go") {
			return
		}
		report := func(n ast.Node) {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return
			}
			v := strings.Trim(lit.Value, "`\"")
			if !bareAddrRe.MatchString(v) || v == ":0" {
				return
			}
			add(lit.Pos(), "测试监听地址用裸端口 "+lit.Value+"（testing-r03：缺 127.0.0.1 前缀会被解析成 IPv6）")
		}
		ast.Inspect(s.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isListenCallee(call.Fun) {
				return true
			}
			for _, arg := range call.Args {
				report(arg)
			}
			return true
		})
	})
}

// isListenCallee 报告调用目标是否是监听/拨号函数（包级或方法形态都认）。
func isListenCallee(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return listenCallees[f.Name]
	case *ast.SelectorExpr:
		return listenCallees[f.Sel.Name]
	}
	return false
}

// compositeTypeName 取复合字面量的类型名（http.Server → Server）。
func compositeTypeName(cl *ast.CompositeLit) string {
	switch t := cl.Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.StarExpr:
		return compositeTypeName(&ast.CompositeLit{Type: t.X})
	}
	return ""
}

// checkStoreHandwrittenTenant 钉 store-r10：写路径租户归属由 injectWriteTenant
// 无条件覆写，业务禁止手工写 tenant_id。
//
// 只判 biz/handler（业务与投影层）：store 与 model 层的 tenant 赋值是持久化实现
// 的分内事，判它们等于禁掉 injectWriteTenant 本身。
//
// 读路径豁免（adv-checkers FP7）：对**同文件声明为 *Query/*Filter/*List/*Params**
// 类型变量的 .TenantID 赋值是查询条件构造（平台管理员按租户查询是合法读路径），
// 不是写路径租户归属——injectWriteTenant 覆写的是写路径实体，规则辖不到读路径。
// 类型来源限于同文件的 :=/var 声明（单文件语法判定）；接收者/参数上的赋值类型
// 不可知，仍报告（宁漏勿误的边界在这里倾向少报）。
func checkStoreHandwrittenTenant(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["store-handwritten-tenant"],
		suggest: "删掉手写 tenant_id：写路径由 store 的 injectWriteTenant 从 contextx 取租户覆写，业务传了也会被冲掉",
	}, func(s *goSource, add func(token.Pos, string)) {
		if !isInternalLayer(s.rel, "biz") && !isInternalLayer(s.rel, "handler") {
			return
		}
		// 第一遍：收集同文件变量的声明类型（x := &pkg.Type{...} / var x pkg.Type）。
		varTypes := map[string]string{}
		collect := func(id *ast.Ident, rhs ast.Expr) {
			var typeName string
			switch v := rhs.(type) {
			case *ast.UnaryExpr:
				if cl, ok := v.X.(*ast.CompositeLit); ok {
					typeName = compositeTypeName(cl)
				}
			case *ast.CompositeLit:
				typeName = compositeTypeName(v)
			}
			if typeName != "" {
				varTypes[id.Name] = typeName
			}
		}
		ast.Inspect(s.file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.AssignStmt:
				if node.Tok == token.DEFINE && len(node.Lhs) == 1 && len(node.Rhs) == 1 {
					if id, ok := node.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
						collect(id, node.Rhs[0])
					}
				}
			case *ast.GenDecl:
				if node.Tok != token.VAR {
					return true
				}
				for _, spec := range node.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Values) != 1 || len(vs.Names) != 1 {
						continue
					}
					collect(vs.Names[0], vs.Values[0])
				}
			}
			return true
		})
		// 第二遍：判 .TenantID 赋值，读路径豁免。
		ast.Inspect(s.file, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "TenantID" {
					continue
				}
				// 沿链找根变量，查它的声明类型是否是查询 DTO。
				root := chainRoot(lhs)
				if id, ok := root.(*ast.Ident); ok && isQueryDTOType(varTypes[id.Name]) {
					continue
				}
				add(as.Pos(), "业务层手工写 TenantID（store-r10：租户归属由 injectWriteTenant 覆写）")
			}
			return true
		})
	})
}

// isQueryDTOType 报告类型名是否查询 DTO 的命名形态（*Query/*Filter/*List/*Params）。
func isQueryDTOType(name string) bool {
	if name == "" {
		return false
	}
	for _, suffix := range []string{"Query", "Filter", "List", "Params", "Selector"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// modulePath 读 go.mod 的 module 行。
func modulePath(gomod string) (string, error) {
	raw, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", fmt.Errorf("%s: no module line", gomod)
}

// checkReuseVendoredBald 钉 reuse-r02：不复制、不 vendor bald 库代码——复制让
// 升级与安全修复变得非常困难。
//
// 判定：项目内出现 module 行为 github.com/kalandramo/bald* 的**嵌套** go.mod。
// 自身就是 bald 仓库时整条 skip（96 个 module 都在该路径下，那是本仓结构而非
// 复制），这种 skip 与 gen-app-drift「没有 appspec 就不判」同源：规则管辖的是
// 消费者项目。
//
// 已知局限（adv-checkers M3，语法判定的边界，声明不修复）：真实 vendor 手法
// 会改写或删除 module 行（module acme-vendor/pkg/store）——这类副本本判定探测
// 不到。守这道门的主力是 code review 与「代码溯源」的评审习惯，本 checker 只拦
// 最粗的形态（module 行原样保留的整仓复制）。
func checkReuseVendoredBald(dir string) ([]Finding, string, error) {
	root := filepath.Join(dir, "go.mod")
	mod, err := modulePath(root)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return nil, "no go.mod in " + dir, nil
		}
		return nil, "no module line in " + dir, nil
	}
	if strings.HasPrefix(mod, "github.com/kalandramo/bald") {
		return nil, "scanned repo is bald itself (" + mod + ")", nil
	}
	var findings []Finding
	var scanned int
	walkErr := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "go.mod" || path == root {
			return nil
		}
		scanned++
		nested, err := modulePath(path)
		if err != nil || !strings.HasPrefix(nested, "github.com/kalandramo/bald") {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		findings = append(findings, Finding{
			RuleID:     checkerRule["reuse-vendored-bald"],
			Level:      LevelMandatory,
			File:       filepath.ToSlash(rel),
			Line:       1,
			Message:    "项目内出现 bald 库源码副本（module " + nested + "，reuse-r02）",
			Suggestion: "删掉副本，改以 Go module 依赖引入（go get github.com/kalandramo/bald/pkg@vX.Y.Z）；复制会让升级与安全修复无法传导",
		})
		return nil
	})
	if walkErr != nil {
		return nil, "", fmt.Errorf("walk %s: %w", dir, walkErr)
	}
	return findings, "", nil
}
