// Package authz 定义 bald 的授权抽象（零引擎耦合）。
//
// 设计原则（见 docs/devel/zh-CN/架构演进路线.md §0.5 / P7）：
//   - 核心只定义 Authorizer 接口，不 import casbin / rbac 等具体策略引擎；
//     Casbin/RBAC 作为可选桥接子模块（bald-authz-casbin）外置。
//   - 授权判定基于 (subject, object, action)；subject 从 authn.AuthClaims 取，
//     object/action 由拦截器从请求（如 FullMethod、HTTP route）推导。
package authz

import (
	"strings"
)

// ObjectResolver 从传输层请求上下文推导授权对象（object）。
// 入参为 gRPC FullMethod（形如 "/pkg.Service/Method"）或 HTTP 路径（形如 "/v1/secret/123"）。
// 返回空字符串表示沿用拦截器默认（gRPC=FullMethod，HTTP=path）。
type ObjectResolver func(request string) string

// ActionResolver 从传输层请求上下文推导授权动作（action）。
// 入参语义同 ObjectResolver；HTTP 侧为方法名（GET/DELETE/...），gRPC 侧为 FullMethod。
type ActionResolver func(request string) string

// DefaultGRPCObject 把 gRPC FullMethod 归一化为传输中立的资源名，与 HTTP 路由同源：
//
//	"/go.bald.admin.v1.SecretService/GetSecret" -> "secret"
//	"/go.bald.admin.v1.AuthService/WhoAmI"      -> "auth"
//
// 规则：取 FullMethod 的 Service 段（最后一个 '.' 之前、首个 '/' 之后），
// 去除 "Service" 后缀并转小写。这样同一业务资源在 REST（/v1/secret/*）与
// gRPC（SecretService.*）下归一到同一资源名，避免「双命名空间泄漏」
// （M6.8 CR Issue4 的根因：gin 与 grpc 各用各的 object 命名，策略需双写）。
func DefaultGRPCObject(fullMethod string) string {
	svc := strings.Trim(fullMethod, "/")
	if i := strings.Index(svc, "/"); i >= 0 {
		svc = svc[:i]
	}
	if i := strings.LastIndex(svc, "."); i >= 0 {
		svc = svc[i+1:]
	}
	svc = strings.TrimSuffix(svc, "Service")
	return strings.ToLower(svc)
}

// DefaultGRPCAction 把 gRPC FullMethod 归一化为传输中立的动作，与 HTTP 动词同源：
//
//	SecretService/GetSecret    -> "get"
//	SecretService/DeleteSecret -> "delete"
//	SecretService/ListUsers    -> "list"
//	SecretService/CreateX      -> "write"
//
// 规则：取 FullMethod 方法段（最后一个 '/' 之后），按前缀推导动作。
// 与 HTTP 侧动作空间（get/delete/list/write）对齐，使同一策略可同时约束 REST 与 gRPC。
func DefaultGRPCAction(fullMethod string) string {
	method := fullMethod
	if i := strings.LastIndex(fullMethod, "/"); i >= 0 {
		method = fullMethod[i+1:]
	}
	switch {
	case strings.HasPrefix(method, "Delete"):
		return "delete"
	case strings.HasPrefix(method, "List"):
		return "list"
	case strings.HasPrefix(method, "Create"),
		strings.HasPrefix(method, "Update"),
		strings.HasPrefix(method, "Set"),
		strings.HasPrefix(method, "Put"):
		return "write"
	default:
		return "get"
	}
}

// DefaultHTTPObject 把 HTTP 路径归一化为资源名（去 /v1 前缀，取首个资源段）：
//
//	"/v1/secret/123" -> "secret"
//	"/v1/auth/whoami" -> "auth"
func DefaultHTTPObject(path string) string {
	s := strings.Trim(path, "/")
	if s == "" {
		return ""
	}
	for _, p := range strings.Split(s, "/") {
		if p == "v1" || p == "" {
			continue
		}
		return p
	}
	return ""
}

// DefaultHTTPAction 把 HTTP 方法归一化为小写动作（与 gRPC 动作空间对齐）。
func DefaultHTTPAction(method string) string {
	return strings.ToLower(method)
}
