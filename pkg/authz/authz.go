// Package authz 定义 bald 的授权抽象（零引擎耦合）。
//
// 设计原则（见 docs/devel/zh-CN/架构演进路线.md §0.5 / P7）：
//   - 核心只定义 Authorizer 接口，不 import casbin / rbac 等具体策略引擎；
//     Casbin/RBAC 作为可选桥接子模块（bald-authz-casbin）外置。
//   - 授权判定基于 (subject, object, action)；subject 从 authn.AuthClaims 取，
//     object/action 由拦截器从请求（如 FullMethod、HTTP route）推导。
package authz

import "context"

// Authorizer 授权判定接口。具体实现（Casbin、RBAC 表、硬编码规则）外置为桥接子模块，
// 由业务侧构造并注入 middleware/gin.AuthzMiddleware / middleware/grpc.AuthzInterceptor。
// 核心不持有 Authorizer 实例，授权器属业务策略，不进 appkit 通用 Registry。
type Authorizer interface {
	// Authorize 判定 subject 是否可对 object 执行 action。返回 (allowed, error)：
	//   - allowed=false 且无 error：明确拒绝（拦截器映射为 403）。
	//   - error!=nil：判定过程出错（如策略引擎不可用，拦截器映射为 500）。
	Authorize(ctx context.Context, subject, object, action string) (bool, error)
}

// Func 将普通函数适配为 Authorizer，便于内联实现或测试。
type Func func(ctx context.Context, subject, object, action string) (bool, error)

// Authorize 实现 Authorizer 接口。
func (f Func) Authorize(ctx context.Context, subject, object, action string) (bool, error) {
	return f(ctx, subject, object, action)
}

// AllowAll 永远放行的 Authorizer（开放接口/公开服务使用）。
func AllowAll() Authorizer {
	return Func(func(context.Context, string, string, string) (bool, error) { return true, nil })
}

// DenyAll 永远拒绝的 Authorizer（默认拒绝，需显式放行）。
func DenyAll() Authorizer {
	return Func(func(context.Context, string, string, string) (bool, error) { return false, nil })
}
