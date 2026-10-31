package codegen

// gen_domain_bind.go 把 google.api.http 注解派生为 gin 绑定与路由清单，
// 并保证绑定产物的两条流水线前置（transport/web 依赖与 proto Go 类型）。

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// bindView 是逐域绑定文件的模板数据。
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
// 绑定文件）。绑定默认生成：需要 proto 的 Go 类型，go_package 缺失即报错——
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
			return nil, fmt.Errorf("domain %q: rpc %s carries google.api.http field(s) %s that binding generation does not implement (custom verbs and response_body would silently diverge from the annotation; hand-write this route instead)", d.GetName(), m.Name, strings.Join(m.Unsupported, ", "))
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
		return nil, fmt.Errorf("domain %q: proto service %q has no option go_package; bindings need it to import the generated Go message types", d.GetName(), d.GetService())
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

// emitAPIServerSide 收集 internal/<apiserver_dir>/ 的授权派生产物
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
	byService := serviceIndex(scan)
	bindDepsChecked := map[string]bool{}
	for _, d := range v.Spec.GetDomains() {
		name := title(d.GetName())
		svc := byService[d.GetService()]
		if err := emit(filepath.Join("internal", v.APIDir, "handler", "convert", d.GetName()+".gen.go"), handlerConvertTmpl,
			map[string]any{"Name": name, "Service": d.GetService(), "Actions": derivedActions(svc)}); err != nil {
			return err
		}
		if !o.NoBind {
			bind, err := bindViewOf(name, d, svc)
			if err != nil {
				return err
			}
			if bind != nil {
				if !bindDepsChecked[bind.ProtoPkgPath] {
					if err := o.ensureBindDeps(bind.ProtoPkgPath); err != nil {
						return err
					}
					bindDepsChecked[bind.ProtoPkgPath] = true
				}
				if !o.NoRoutes {
					bind.RoutesPkgPath = v.Module + "/internal/" + v.APIDir + "/routes"
					// 计划可见性：把同一批注解派生为 appkit.Route 清单。域包直接
					// 引用它（RouteTable: routes.<Name>）即可让 --plan/doctor 列出
					// 路由、让跨域冲突在计划期报错——注解仍是唯一真相源，不重复
					// 维护。放在独立叶子包（只 import appkit），域包引用它不引入
					// 对传输实现的依赖。
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
	// 授权别名表 + 默认动作聚合（proto service 派生）。
	aliases, merged := derivedAuthz(v.Spec, byService)
	return emit(filepath.Join("internal", v.APIDir, "authz", "alias.gen.go"), authzAliasTmpl,
		map[string]any{"APIDir": v.APIDir, "Module": v.Module, "Aliases": aliases, "Actions": merged,
			"Domains": v.Spec.GetDomains(), "HasDomains": len(v.Spec.GetDomains()) > 0})
}

// ensureBindDeps 保证绑定产物的两条流水线前置：bind 文件 import transport/web
// （独立 module）与 proto 的 Go 类型包（proto 阶段产物）。transport/web 缺失
// 只能由作者补依赖（给出 go get 修法）；proto 类型缺失则由本命令按 --proto-cmd
// 编排 proto 阶段，编排失败或仍无产物都是生成期硬错误——不给「生成成功、构建
// 失败」留缝隙。--check 零写入，不执行外部命令，只报可执行修法。
func (o *domainOptions) ensureBindDeps(protoPkg string) error {
	const transportWeb = "github.com/kalandramo/bald/transport/web"
	gomod := filepath.Join(o.OutDir, "go.mod")
	raw, err := os.ReadFile(gomod)
	if err != nil {
		return fmt.Errorf("bindings import %s, but reading %s failed: %w (add the dependency or pass --no-bind)", transportWeb, gomod, err)
	}
	if !strings.Contains(string(raw), transportWeb) {
		return fmt.Errorf("bindings import %s, but %s does not require it; run `go get %s` and regenerate (or pass --no-bind to skip annotation-derived bindings)", transportWeb, gomod, transportWeb)
	}
	if o.protoPkgReady(string(raw), protoPkg) {
		return nil
	}
	if o.Check {
		// --check 只读：执行 proto 阶段会写盘，与零写校验的承诺冲突。
		return fmt.Errorf("bindings import proto Go package %q, but its generated Go files are missing; run `%s` (or the proto stage yourself) and rerun --check, or pass --no-bind to verify the binding-free file set", protoPkg, o.ProtoCmd)
	}
	if err := o.runProtoStage(protoPkg); err != nil {
		return err
	}
	if !o.protoPkgReady(string(raw), protoPkg) {
		return fmt.Errorf("proto stage `%s` finished but proto Go package %q still has no generated Go files; check the proto/buf config in %s (or pass --no-bind to skip annotation-derived bindings)", o.ProtoCmd, protoPkg, o.OutDir)
	}
	return nil
}

// protoPkgReady 报告 go_package 的 Go 产物是否就位。包不在本 module 下时归
// 外部依赖自管（生成器不探别人的产出）。
func (o *domainOptions) protoPkgReady(gomod, protoPkg string) bool {
	rel := moduleLocalDir(gomod, protoPkg)
	if rel == "" {
		return true
	}
	entries, err := os.ReadDir(filepath.Join(o.OutDir, rel))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

// runProtoStage 在 --out 根执行 proto 生成命令（默认 buf generate）。绑定文件
// 从注解派生、与其内容无关，但 import 的 Go 类型必须就位——这是 gen domain
// 把 proto 阶段纳入生成流程的原因：一条命令产出一棵可编译的树。
func (o *domainOptions) runProtoStage(protoPkg string) error {
	fmt.Fprintf(o.Out, "proto: proto Go package %q is missing; running `%s` in %s\n", protoPkg, o.ProtoCmd, o.OutDir)
	if err := runShellCommand(o.OutDir, o.ProtoCmd, o.Out, o.ErrOut); err != nil {
		return fmt.Errorf("proto stage `%s` failed for %q: %w; fix the command (or pass --proto-cmd), run it yourself, or pass --no-bind to skip annotation-derived bindings", o.ProtoCmd, protoPkg, err)
	}
	return nil
}

// runShellCommand 以 shell 语义执行命令（管道/重定向/&& 都可用）：--proto-cmd
// 是作者配置的流水线命令，不是生成器拼接的参数，不能按空白强拆。
func runShellCommand(dir, cmdline string, stdout, stderr io.Writer) error {
	name, args := "sh", []string{"-c", cmdline}
	if runtime.GOOS == "windows" {
		name, args = "cmd", []string{"/C", cmdline}
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// moduleLocalDir 把 go_package 路径映射回消费方 module 内的相对目录；包不在
// 本 module 下（或 go.mod 无 module 行）时返回空——外部依赖是否已产出不归
// 生成器管。
func moduleLocalDir(gomod, pkg string) string {
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		mod := strings.TrimSpace(strings.TrimPrefix(line, "module "))
		if pkg == mod {
			return "."
		}
		if strings.HasPrefix(pkg, mod+"/") {
			return strings.TrimPrefix(pkg, mod+"/")
		}
		return ""
	}
	return ""
}
