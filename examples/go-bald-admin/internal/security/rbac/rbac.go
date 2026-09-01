// Package rbac 实现基于内存角色映射的 authz.Authorizer（RBAC 范本，M1 验证用）。
//
// Deprecated: M6.1 起授权由 internal/security/casbin 桥接 casbin 实现（真实策略引擎，
// 命中 §0「禁止手写假实现」契约）。本包保留作无 casbin 退化实现与单元测试参考，业务侧
// 装配已切到 casbin。
//
// 设计约束（对齐 bald P7 原则）：
//   - 仅依赖 bald 核心的 authz.Authorizer 接口，不耦合 casbin / gin / grpc；
//     授权规则由业务侧定义，核心框架保持零策略引擎耦合。
//   - 角色→权限集合为内存静态表，作为 M1 验证授权范式的最小实现。
//
// 权限点约定：gin/grpc Authz 中间件传入 subject=用户 ID、object=请求路径、
// action=HTTP 方法（小写）。本 Authorizer 把路径归一化为「资源名」（去掉 /v1
// 前缀与动态 id 段），例如
//
//	GET  /v1/secret/123   -> object="secret", action="get"  -> 权限点 "secret:get"
//	DEL  /v1/secret/123   -> object="secret", action="delete"
//	GET  /v1/auth/whoami  -> object="auth",   action="get"   -> 权限点 "auth:get"
//
// 角色由业务侧从 token 解析后注入 subject 之外——本实现从 subject 无法取得角色，
// 因此改用「subject -> roles」映射表（M1 范本：admin/u-admin、viewer/u-alice）。
package rbac

import (
	"context"
	"fmt"
	"strings"

	"github.com/kalandramo/bald/pkg/authz"
)

// Authorizer 是基于内存角色映射的 RBAC 授权器。
type Authorizer struct {
	// rolePerms 记录每个角色可执行的 "object:action" 权限点集合。
	rolePerms map[string]map[string]struct{}
	// subjectRoles 记录每个 subject（用户 ID）拥有的角色。
	subjectRoles map[string][]string
}

// New 构造 RBAC Authorizer。
//   - rolePerms：角色名 -> 权限点列表（"object:action"）。
//   - subjectRoles：subject（用户 ID）-> 角色列表。
func New(rolePerms map[string][]string, subjectRoles map[string][]string) *Authorizer {
	m := make(map[string]map[string]struct{}, len(rolePerms))
	for role, perms := range rolePerms {
		set := make(map[string]struct{}, len(perms))
		for _, p := range perms {
			set[p] = struct{}{}
		}
		m[role] = set
	}
	return &Authorizer{rolePerms: m, subjectRoles: subjectRoles}
}

// normObject 把请求对象归一化为资源名，同时兼容两种传输形态：
//   - HTTP 路径："/v1/secret/123" -> "secret"（去 /v1 前缀，取首段资源）
//     "/v1/auth/whoami" -> "auth"
//   - gRPC FullMethod："/go.bald.admin.v1.SecretService/GetSecret" -> "SecretService"
//     （取 '/' 前的 Service 全名，再取最后一个 '.' 之后的服务段）
//
// 归一化后权限点 = "资源名:action"，与业务规则表匹配，实现传输中立的 RBAC。
func normObject(raw string) string {
	s := strings.Trim(raw, "/")
	if s == "" {
		return ""
	}
	// gRPC FullMethod：含 '.' 且形如 "pkg.Service/Method"。
	// 归一化为 "Service.Method"（去包前缀，保留方法段，使方法级授权可行）。
	if strings.Contains(s, ".") {
		svc := s
		method := ""
		if i := strings.Index(s, "/"); i >= 0 {
			svc, method = s[:i], s[i+1:]
		}
		if i := strings.LastIndex(svc, "."); i >= 0 {
			svc = svc[i+1:]
		}
		if method != "" {
			return svc + "." + method
		}
		return svc
	}
	// HTTP 路径：取首个非 v1/非空段作为资源名。
	for _, p := range strings.Split(s, "/") {
		if p == "v1" || p == "" {
			continue
		}
		return p
	}
	return ""
}

// Authorize 判定 subject（用户 ID）是否对 (object, action) 有权限。
//
// 规则：
//   - 取 subject 的全部角色，任一角色含该权限点即放行；
//   - 无任何角色匹配 → 拒绝（默认拒绝，需显式授权）。
//   - subject 为空（未认证）→ 拒绝。
func (a *Authorizer) Authorize(ctx context.Context, subject, object, action string) (bool, error) {
	if subject == "" {
		return false, fmt.Errorf("rbac: empty subject (not authenticated)")
	}
	resource := normObject(object)
	perm := resource + ":" + strings.ToLower(action)
	for _, role := range a.subjectRoles[subject] {
		if set, ok := a.rolePerms[role]; ok {
			if _, hit := set[perm]; hit {
				return true, nil
			}
		}
	}
	return false, nil
}

// compile-time 断言：*Authorizer 实现 authz.Authorizer。
var _ authz.Authorizer = (*Authorizer)(nil)
