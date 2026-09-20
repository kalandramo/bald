package cobramcp

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// 说明：本文件提供 cobramcp 的认证中间件构建逻辑。
//
// cobramcp 不内置具体认证引擎（JWT/OIDC/API Key 外置为桥接子模块），
// 只提供两类能力：
//  1. 透传用户注入的 HTTP 中间件（Config.AuthMiddleware / MCPOptions.AuthMiddleware）；
//  2. 内置静态 Bearer token 校验（Config.AuthToken / --auth-token）。
//
// 认证在 http.Handler 层完成，因为 mcp-go 的 SSEContextFunc / HTTPContextFunc
// 只返回 context.Context、无 error 返回位，无法拒绝请求。

// buildAuthMiddleware 把 AuthMiddleware（外层）与 AuthToken（内层静态校验）
// 组合成单一中间件链。两者都未配置时返回 nil（不启用认证）。
//
// publicPaths 是免认证路径（如 OAuth well-known 发现端点）——客户端在未持
// token 时也需访问，否则 OAuth 发现流程会死锁。
func buildAuthMiddleware(
	userMW func(http.Handler) http.Handler,
	token string,
	publicPaths ...string,
) func(http.Handler) http.Handler {
	// 内层：静态 token 校验。
	var chain func(http.Handler) http.Handler
	if token != "" {
		chain = staticTokenMiddleware(token, publicPaths...)
	}

	// 外层：用户注入的中间件。用户中间件包裹静态校验（先执行用户逻辑）。
	if userMW != nil {
		if chain == nil {
			chain = userMW
		} else {
			inner := chain
			chain = func(next http.Handler) http.Handler {
				return userMW(inner(next))
			}
		}
	}

	return chain
}

// staticTokenMiddleware 校验 Authorization: Bearer <token> 与期望值相等。
// 校验失败返回 401 并中断请求（不调用 next）。
//
// 安全要点：
//   - 用 crypto/subtle.ConstantTimeCompare 做常数时间比较，防时序攻击。
//   - publicPaths 中的路径免认证（OAuth well-known 发现端点必须公开）。
func staticTokenMiddleware(token string, publicPaths ...string) func(http.Handler) http.Handler {
	want := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range publicPaths {
				if p != "" && r.URL.Path == p {
					next.ServeHTTP(w, r)
					return
				}
			}

			got := bearerToken(r.Header.Get("Authorization"))
			// 长度不等时 ConstantTimeCompare 直接返回 0，但仍先做长度检查
			// 以避免对空 token 的意外放行。
			if got == "" || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken 从 Authorization 头提取 Bearer token。
// 兼容裸 token（无 "Bearer " 前缀）——与 transport/sse 的 DefaultTokenExtractor 行为一致。
func bearerToken(auth string) string {
	auth = strings.TrimSpace(auth)
	if auth == "" {
		return ""
	}
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return auth
}

// 编译期断言：staticTokenMiddleware 的返回类型可直接传给 transport/mcp 的
// Middleware 参数（两者底层类型均为 func(http.Handler) http.Handler）。
var _ func(http.Handler) http.Handler = staticTokenMiddleware("x")
