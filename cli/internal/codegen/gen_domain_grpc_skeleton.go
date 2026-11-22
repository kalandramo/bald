package codegen

// gen_domain_grpc_skeleton.go 是 `gen domain --grpc-skeleton` 的产物模板与
// 落盘逻辑：为带 proto service 的域生成 gRPC service 骨架（struct + 构造 +
// 每 RPC 的方法签名 + notFoundOr 错误归一模板）。
//
// 归属刻意是 **seeded**（生成一次，此后归用户维护）：proto↔biz 的字段转换、
// claims 读取、业务错误 reason 无法从 proto 派生，必须手写——owned 会在
// --adopt 时被反复覆盖，直接毁掉手写实现。与 domainValueTmpl 同一 precedent。
// 生成器只保证「方法签名齐全、编译可过、错误出口统一成 notFoundOr 模板」，
// 骨架体返回 Unimplemented，逼作者逐个填实现（漏填不会静默返回零值）。

import (
	"fmt"
	"path/filepath"
	"sort"
)

// grpcSkeletonTmpl 是 handler/grpc/<domain>.grpc.gen.go（seeded）。
var grpcSkeletonTmpl = `// Code generated once by "bald gen domain --grpc-skeleton"; now maintained by you.
//
// {{ .Domain }} 域的 gRPC service 骨架。方法体是待填充的 stub：
// proto↔biz 转换、claims 读取、业务错误 reason 必须手写；生成物不再覆盖。
package grpc

import (
	"context"

	"github.com/kalandramo/bald/berrors"
{{- range .Imports }}
	{{ .Alias }} "{{ .Path }}"
{{- end }}

	{{ .BizAlias }} "{{ .BizPackage }}"
)
{{- range .Services }}
{{- $svc := . }}

// {{ .ImplName }} 实现 {{ .Service }}（嵌入 Unimplemented 满足
// require_unimplemented_servers 的前向兼容约定）。
type {{ .ImplName }} struct {
	{{ .Alias }}.Unimplemented{{ .Short }}Server
	biz *{{ $.BizAlias }}.{{ $.BizType }}
}

// {{ .CtorName }} 构造 {{ .Service }} 的实现（biz 由域 Wire 注入）。
func {{ .CtorName }}(biz *{{ $.BizAlias }}.{{ $.BizType }}) {{ .Alias }}.{{ .Short }}Server {
	return &{{ .ImplName }}{biz: biz}
}
{{- range .Methods }}

// {{ .RPC }} 是 {{ $svc.Service }}.{{ .RPC }} 的实现骨架。
func (s *{{ $svc.ImplName }}) {{ .RPC }}(ctx context.Context, req *{{ .Request }}) (*{{ .Response }}, error) {
	// TODO(biz): 手写 proto → biz → proto 转换；错误归一用
	//   notFoundOr(err, "{{ $.Domain }}/not_found", "<readable>")。
	_ = ctx
	_ = req
	_ = s.biz
	return nil, berrors.Unimplemented("{{ $.Domain }}/{{ .RPC }}")
}
{{- end }}
{{- end }}
`

// grpcSkeletonService 是一个 service 在骨架里的渲染视图。
type grpcSkeletonService struct {
	Service  string // proto service 全名
	Short    string // service 短名（GreetService）
	Alias    string // proto Go 包 import 别名
	ImplName string // 实现结构体名（greetService）
	CtorName string // 构造器名（NewGreetServer）
	Methods  []bindMethodView
}

// grpcSkeletonData 是一份骨架文件的模板数据。
type grpcSkeletonData struct {
	Domain     string
	BizPackage string
	BizAlias   string
	BizType    string
	Imports    []bindImport
	Services   []grpcSkeletonService
}

// emitGRPCSkeletons 为声明了 grpc_biz_package + grpc_biz_type 的域生成骨架。
// 二者任一为空即跳过（生成器不猜应用特定命名）。已存在同名文件时跳过——
// seeded 只生成一次，不覆盖手写实现（重 seed 需显式删文件再生成）。
//
// 类型表达式与 import 完全复用 bindViewOf 的派生结果（Request/Response 已带
// proto 包别名、知名类型走各自 Go 包），保证骨架与 REST 绑定同源，不各拼一套。
func (o *domainOptions) emitGRPCSkeletons(v *treeView, scan *protoScan, files *[]genFile) error {
	if !o.GRPCSkeleton {
		return nil
	}
	byService := serviceIndex(scan)
	for _, d := range v.Spec.GetDomains() {
		if d.GetGrpcBizPackage() == "" || d.GetGrpcBizType() == "" {
			continue
		}
		names := domainServices(d)
		if len(names) == 0 {
			continue
		}
		svcs := make([]protoService, 0, len(names))
		for _, full := range names {
			svc, ok := byService[full]
			if !ok {
				return fmt.Errorf("domains (%s): service %q not found in scanned protos", d.GetName(), full)
			}
			svcs = append(svcs, svc)
		}
		// bindViewOf 需要每个 rpc 都有 google.api.http 注解才会产 routes；骨架
		// 不依赖注解，故即使 bindViewOf 返回 nil 也要拿到它的 service/method 视图。
		bind, err := bindViewOf(title(d.GetName()), d, svcs)
		if err != nil {
			return err
		}
		data := grpcSkeletonData{
			Domain:     d.GetName(),
			BizPackage: d.GetGrpcBizPackage(),
			BizAlias:   grpcBizAlias(d.GetGrpcBizPackage()),
			BizType:    d.GetGrpcBizType(),
		}
		if bind != nil {
			data.Imports = bind.Imports
			for _, svcView := range bind.Services {
				short := svcView.Interface
				data.Services = append(data.Services, grpcSkeletonService{
					Service:  svcView.Service,
					Short:    short,
					Alias:    svcView.Alias,
					ImplName: grpcImplName(short),
					CtorName: grpcServerFuncName(short),
					Methods:  svcView.Methods,
				})
			}
		} else {
			// 无注解域：自建最小视图（proto 包 alias 直接由 go_package 派生）。
			for _, svc := range svcs {
				protoPath, protoAlias := parseProtoGoPackage(svc.GoPackage)
				if protoPath != "" && !hasImport(data.Imports, protoPath) {
					data.Imports = append(data.Imports, bindImport{Alias: protoAlias, Path: protoPath})
				}
				view := grpcSkeletonService{
					Service:  svc.Package + "." + svc.Name,
					Short:    svc.Name,
					Alias:    protoAlias,
					ImplName: grpcImplName(svc.Name),
					CtorName: grpcServerFuncName(svc.Name),
				}
				for _, m := range svc.Methods {
					reqExpr, reqImp := goTypeExpr(svc.Package, protoAlias, m.Request)
					respExpr, respImp := goTypeExpr(svc.Package, protoAlias, m.Response)
					for _, imp := range []*bindImport{reqImp, respImp} {
						if imp != nil && !hasImport(data.Imports, imp.Path) {
							data.Imports = append(data.Imports, *imp)
						}
					}
					view.Methods = append(view.Methods, bindMethodView{RPC: m.Name, Request: reqExpr, Response: respExpr})
				}
				data.Services = append(data.Services, view)
			}
		}
		if len(data.Services) == 0 {
			continue
		}
		sortImports(data.Imports)
		path := filepath.Join("internal", v.APIDir, "handler", "grpc", d.GetName()+".grpc.gen.go")
		raw, err := renderTreeTemplate(filepath.Base(path), grpcSkeletonTmpl, data)
		if err != nil {
			return err
		}
		*files = append(*files, genFile{Path: path, Own: Seeded, Raw: raw, GoSrc: true})
	}
	return nil
}

// grpcBizAlias 从 biz import 路径取包别名（基名清洗成合法标识符）。
func grpcBizAlias(importPath string) string {
	p, alias := parseProtoGoPackage(importPath)
	_ = p
	if alias != "" {
		return alias
	}
	return domainAlias(importPath)
}

// hasImport 报告 import 清单是否已含某路径。
func hasImport(imports []bindImport, path string) bool {
	for _, imp := range imports {
		if imp.Path == path {
			return true
		}
	}
	return false
}

// sortImports 按路径稳定排序 import 清单（生成物确定性）。
func sortImports(imports []bindImport) {
	sort.Slice(imports, func(i, j int) bool { return imports[i].Path < imports[j].Path })
}
