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
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// goSource 是一个已解析的源文件（rel 相对被检根，用于 finding 的 file 字段）。
type goSource struct {
	rel  string
	file *ast.File
	fset *token.FileSet
}

// line 把 token 位置翻成行号。
func (s *goSource) line(p token.Pos) int { return s.fset.Position(p).Line }

// astWalk 是遍历形态的参数。
type astWalk struct {
	includeTests bool     // 是否纳入 _test.go（testing-* 规则要，其余不要）
	exempt       []string // 相对路径前缀豁免：命中即不交回调（仍计入 scanned）
}

// walkAST 遍历 dir 下可判的 Go 文件。跳过目录与既有 checkLoggingStdlib /
// checkNoPanic 一致（vendor/.git/node_modules/_example/testdata）；解析失败的文件
// 不判（非法 Go 归 go build 管，不归规范检查器）。
func walkAST(dir string, w astWalk, fn func(*goSource)) (int, error) {
	fset := token.NewFileSet()
	scanned := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case "vendor", ".git", "node_modules", "_example", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		if !w.includeTests && strings.HasSuffix(name, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, raw, 0)
		if perr != nil {
			return nil
		}
		scanned++
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		for _, p := range w.exempt {
			if strings.HasPrefix(rel, p) {
				return nil
			}
		}
		fn(&goSource{rel: rel, file: f, fset: fset})
		return nil
	})
	if err != nil {
		return scanned, fmt.Errorf("walk %s: %w", dir, err)
	}
	return scanned, nil
}

// astRule 是一条 checker 的输出版式（ruleID 来自 checkerRule 的绑定）。
type astRule struct {
	ruleID  string
	suggest string
}

// checkAST 是 AST 检查器的共用外壳：go.mod 缺失（含 dir 本身是文件）与服务
// 里没有 Go 源文件这两种情况都视为「环境不可判」——与 gen-app-drift 的 skip
// 语义同款：不是违规，也不是通过。
func checkAST(dir string, w astWalk, r astRule, fn func(*goSource, func(token.Pos, string))) ([]Finding, string, error) {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return nil, "no go.mod in " + dir, nil
		}
		return nil, "", fmt.Errorf("stat %s: %w", dir, err)
	}
	var findings []Finding
	scanned, err := walkAST(dir, w, func(s *goSource) {
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
	})
	if err != nil {
		return nil, "", err
	}
	if scanned == 0 {
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

// isInternalLayer 报告相对路径是否落在生成树的内部分层目录
// internal/<app>/<layer>（layer 取 biz|store|model|handler）。
//
// 只认 internal/ 之下：库本体 pkg/store、contrib/store-gorm 不是「业务层」，
// 它们的 import 由各自模块治理——这是本判定的职责豁免。
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
		// i>1 保证 internal/ 后面确实有一层应用名（internal/biz/x.go 这种
		// 缺应用名的形态不判，避免误伤非生成树的自由布局）。
		if i > 1 && s == layer {
			return true
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
// 形态判定：**语句级**（ExprStmt）调用且最外层选择子以 With 开头。链式
// `x.WithLabelValues(...).Add(...)` 的最外层是 Add、`f(WithX(...))` 是实参——
// 都不报（prometheus/protobuf 的 builder 链天然长这样）。
func checkBerrorsWithDiscard(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["berrors-with-discard"],
		suggest: "接住返回值（e = e.WithDetails(...)）或直接链式传给调用方；丢弃返回值会让已构造的错误实例被后续请求复用",
	}, func(s *goSource, add func(token.Pos, string)) {
		if _, _, ok := importHit(s.file, func(p string) bool { return isBaldPkg(p, "bald/berrors") }); !ok {
			return
		}
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
			add(call.Pos(), sel.Sel.Name+" 的返回值被丢弃（errors-r03：With* 是不可变 builder）")
			return true
		})
	})
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

// checkCtxStringKey 钉 context-r01 最硬的一档：context.WithValue 用**字符串
// 字面量**做键——跨包撞键的经典来源。类型键（ctxKey{}）是 Go 官方推荐写法，
// 但 bald 的统一面是 pkg/contextx 的五个标准键，类型键这一档交评审（无法从语法
// 判断它是否绕开了 contextx）；测试文件不判（用例要构造私有键模拟外部上下文）。
func checkCtxStringKey(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["ctx-string-key"],
		suggest: "用 pkg/contextx 的 With/From 对（user/username/trace_id/request_id/tenant_id）；自有数据不要塞 context",
	}, func(s *goSource, add func(token.Pos, string)) {
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
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			add(call.Pos(), "context.WithValue 用字符串字面量 "+lit.Value+" 做键（context-r01：禁止私有 context 键）")
			return true
		})
	})
}

// checkValidateSignature 钉 validation-r02 的方法名约定：Validate* 必须是
// func(recv T, ctx context.Context, req *XxxRequest) error。
//
// 豁免：裸 Validate()（protoc-gen-validate / protovalidate 的生成形态）不属于
// 本约定的管辖范围——名字约定要求「Validate + 请求类型名」，裸 Validate 无类型名
// 可校验，且是第三方生成器的固定形态，判它只会逼人生成器打补丁。
func checkValidateSignature(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["validate-signature"],
		suggest: "改成 func (r T) ValidateXxx(ctx context.Context, req *XxxRequest) error；签名不符必须显式报错，不许静默跳过",
	}, func(s *goSource, add func(token.Pos, string)) {
		ast.Inspect(s.file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Type == nil {
				return true
			}
			name := fn.Name.Name
			if !strings.HasPrefix(name, "Validate") || name == "Validate" {
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
func checkStoreTenantInit(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["store-tenant-init"],
		suggest: "移到装配层显式调用 store.RegisterTenant(\"tenant_id\", store.DefaultTenantFunc)；注册时机要能被 plan/doctor 看见",
	}, func(s *goSource, add func(token.Pos, string)) {
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
// 只判四类动静：os 的环境读取与文件写入、net.Dial*、http 请求。日志调用不判——
// Plan 里 Debug 一行不改变装配拓扑，判它会把可观测性逼成违禁品。
func checkAssemblyPlanImpure(dir string) ([]Finding, string, error) {
	osEnv := map[string]bool{"Getenv": true, "LookupEnv": true, "Setenv": true, "Unsetenv": true, "Environ": true}
	osWrite := map[string]bool{"WriteFile": true, "Create": true, "OpenFile": true, "Mkdir": true, "MkdirAll": true, "Remove": true, "RemoveAll": true, "Rename": true, "Chmod": true}
	netCalls := map[string]bool{"Dial": true, "DialTimeout": true, "DialContext": true}
	httpCalls := map[string]bool{"Get": true, "Post": true, "PostForm": true, "Head": true, "Do": true}
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["assembly-plan-impure"],
		suggest: "把副作用搬进同一节点的 Build（唯一的副作用入口）；Plan 只算拓扑，返回值必须与运行环境无关",
	}, func(s *goSource, add func(token.Pos, string)) {
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
func checkHTTPEnvelopeShape(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["http-envelope-shape"],
		suggest: "错误响应体用 transport/web 的 StatusBody/StatusOf（google.rpc.Status JSON，三面一份契约），HTTP 状态码承载成败而非 body",
	}, func(s *goSource, add func(token.Pos, string)) {
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
			if tags["code"] && tags["data"] && tags["message"] {
				add(ts.Name.Pos(), ts.Name.Name+" 是 {code,data,message} 全 200 信封（errors-r08）")
			}
			return true
		})
	})
}

var jsonTagRe = regexp.MustCompile(`json:"([^",]+)`)

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
// 只在同一个 Stop 函数体里**同时**出现 Shutdown 与 Background/TODO 时报：Stop
// 里用 Background 打一行退出日志不违规，那种写法不该被这条规则拦住。
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
			var shutdownPos, bgPos token.Pos
			bgKind := ""
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == "Shutdown" && !shutdownPos.IsValid() {
					shutdownPos = call.Pos()
				}
				if (sel.Sel.Name == "Background" || sel.Sel.Name == "TODO") && !bgPos.IsValid() {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "context" {
						bgPos = call.Pos()
						bgKind = sel.Sel.Name
					}
				}
				return true
			})
			if shutdownPos.IsValid() && bgPos.IsValid() {
				add(bgPos, "Stop 用 context."+bgKind+"() 调 Shutdown（transport-r06：丢弃调用方超时）")
			}
			return true
		})
	})
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
// 判定只认两类位置：监听/拨号调用的实参，以及 *Server 字面量的 Addr 字段。
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
			switch node := n.(type) {
			case *ast.CallExpr:
				if !isListenCallee(node.Fun) {
					return true
				}
				for _, arg := range node.Args {
					report(arg)
				}
			case *ast.CompositeLit:
				if !strings.HasSuffix(compositeTypeName(node), "Server") {
					return true
				}
				for _, elt := range node.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Addr" {
						report(kv.Value)
					}
				}
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
func checkStoreHandwrittenTenant(dir string) ([]Finding, string, error) {
	return checkAST(dir, astWalk{}, astRule{
		ruleID:  checkerRule["store-handwritten-tenant"],
		suggest: "删掉手写 tenant_id：写路径由 store 的 injectWriteTenant 从 contextx 取租户覆写，业务传了也会被冲掉",
	}, func(s *goSource, add func(token.Pos, string)) {
		if !isInternalLayer(s.rel, "biz") && !isInternalLayer(s.rel, "handler") {
			return
		}
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
				add(as.Pos(), "业务层手工写 TenantID（store-r10：租户归属由 injectWriteTenant 覆写）")
			}
			return true
		})
	})
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
