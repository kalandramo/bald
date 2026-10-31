package codegen

// gen_domain_authz.go 从 proto service/method 派生授权对账面：逐 RPC 动作
// 投影、object 归一化别名与默认动作聚合（规则与 pkg/authz 同源）。

import (
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
