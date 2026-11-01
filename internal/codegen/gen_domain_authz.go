package codegen

// gen_domain_authz.go 从 proto service/method 派生授权对账面：逐 RPC 动作
// 投影、object 归一化别名与默认动作聚合（规则与 pkg/authz 同源）。

import (
	"fmt"
	"path/filepath"
	"strings"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// serviceIndex 把扫描结果按 service 全名建索引。
func serviceIndex(scan *protoScan) map[string]protoService {
	idx := map[string]protoService{}
	for _, s := range scan.Services {
		idx[s.Package+"."+s.Name] = s
	}
	return idx
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

// derivedActions 把单个 service 的逐 RPC 授权动作折算为 map（key =
// FullMethod 去首斜杠；规则与 pkg/authz.DefaultGRPCAction 同源）。
func derivedActions(svc protoService) map[string]string {
	out := map[string]string{}
	for _, m := range svc.Methods {
		out[svc.Package+"."+svc.Name+"/"+m.Name] = grpcActionOf(m.Name)
	}
	return out
}

// derivedActionsOf 聚合一个域全部 service 的逐 RPC 动作（convert 文件的
// <Name>Actions 表按域一份；FullMethod 键全局唯一）。
func derivedActionsOf(svcs []protoService) map[string]string {
	out := map[string]string{}
	for _, svc := range svcs {
		for k, v := range derivedActions(svc) {
			out[k] = v
		}
	}
	return out
}

// serviceDomains 返回有 proto service 可派生的域（http_only 域除外）：
// authz/alias.gen.go 的 DefaultActions 只引用它们的 convert 表。
func serviceDomains(as *appspecv1.AppSpec) []*appspecv1.DomainSpec {
	out := make([]*appspecv1.DomainSpec, 0, len(as.GetDomains()))
	for _, d := range as.GetDomains() {
		if len(domainServices(d)) > 0 {
			out = append(out, d)
		}
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

// derivedAuthz 派生授权别名表（仅列 gRPC/HTTP 两侧归一化不一致的 service——
// bald-admin 教训：冗余映射会掩盖真实命名问题）与逐 RPC 动作聚合。
// http_only 域没有 proto service，不参与派生。
func derivedAuthz(as *appspecv1.AppSpec, byService map[string]protoService) (map[string]string, map[string]string, error) {
	aliases := map[string]string{}
	merged := map[string]string{}
	aliasFrom := map[string]string{} // grpc object -> 首次派生的 service
	for _, d := range as.GetDomains() {
		for _, full := range domainServices(d) {
			svc := byService[full]
			for k, v := range derivedActions(svc) {
				merged[k] = v
			}
			grpcObj := grpcObjectOf(full)
			httpObj := ""
			for _, m := range svc.Methods {
				if httpObj == "" && m.Path != "" {
					httpObj = httpObjectOf(m.Path)
				}
			}
			if httpObj == "" || grpcObj == "" || grpcObj == httpObj {
				continue
			}
			if prev, ok := aliasFrom[grpcObj]; ok && aliases[grpcObj] != httpObj {
				return nil, nil, fmt.Errorf("authorization object %q maps to both %q (%s) and %q (%s); object aliases must be unambiguous",
					grpcObj, aliases[grpcObj], prev, httpObj, full)
			}
			aliasFrom[grpcObj] = full
			aliases[grpcObj] = httpObj
		}
	}
	return aliases, merged, nil
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
