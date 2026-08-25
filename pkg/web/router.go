// Package web 提供标准库 net/http 之上的轻量 Web 能力：路由分组、中间件链、
// 泛型请求绑定流水线与统一响应。
//
// 设计要点（详见 docs/devel/zh-CN/路由注册与绑定设计.md）：
//   - Router 是对 http.ServeMux 的极薄封装，只加两样 ServeMux 没有的东西：
//     分组（Group）与中间件链（func(http.Handler) http.Handler）。
//   - 业务路由归属业务模块：Handler 接口用 ApplyTo(router, middlewares...) 挂载，
//     认证等中间件由装配层注入。
//   - Router 实现 http.Handler，可直接传入 server.NewHTTPServer，服务器层零改动。
package web

import (
	"net/http"
	"strings"
)

// Middleware 是一条标准的中间件：包裹 next，返回新的 http.Handler。
type Middleware func(next http.Handler) http.Handler

// Router 是基于 http.ServeMux 的路由器，支持分组与中间件链。
type Router struct {
	mux         *http.ServeMux
	middlewares []Middleware
	prefix      string
}

// NewRouter 创建根路由器。
func NewRouter(middlewares ...Middleware) *Router {
	return &Router{
		mux:         http.NewServeMux(),
		middlewares: middlewares,
	}
}

// Group 返回带路径前缀 + 中间件子链的子路由器。子链在调用方链之后执行：
// 顺序为 根中间件 → 子组中间件 → 路由中间件（由外到内）。所有 Handle 注册的
// pattern 都会自动拼接该前缀（见 compose/joinPath）。
func (r *Router) Group(prefix string, middlewares ...Middleware) *Router {
	return &Router{
		mux:         r.mux,
		middlewares: append(append([]Middleware{}, r.middlewares...), middlewares...),
		prefix:      joinPath(r.prefix, prefix),
	}
}

// Handle 注册方法级路由。method 传 "" 匹配所有方法；pattern 遵循
// http.ServeMux 的 Go 1.22+ 语法，如 "/v1/users" 或 "/v1/users/{id}"。
// pattern 会自动拼接 Group 累积的前缀。通配符段可由 Request.PathValue("id")
// 读取；本方法还会把 pattern 中提取的变量名注入 ctx，供 URI 绑定器按字段名自动绑定。
func (r *Router) Handle(method, pattern string, handler http.Handler, middlewares ...Middleware) {
	fullPattern := joinPath(r.prefix, pattern)
	vars := extractPathVars(fullPattern)
	full := r.compose(handler, middlewares...)
	withVars := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if len(vars) > 0 {
			req = req.WithContext(withPathVars(req.Context(), vars))
		}
		full.ServeHTTP(w, req)
	})
	if method == "" {
		r.mux.Handle(fullPattern, withVars)
		return
	}
	r.mux.Handle(method+" "+fullPattern, withVars)
}

// HandleFunc 是 Handle 的便捷形式。
func (r *Router) HandleFunc(method, pattern string, h func(http.ResponseWriter, *http.Request), middlewares ...Middleware) {
	r.Handle(method, pattern, http.HandlerFunc(h), middlewares...)
}

// ServeHTTP 让 *Router 成为合法 http.Handler——这是服务器层零改动的关键。
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

// compose 把 handler 与三层中间件（根 + 路由级）组装成最终 handler。
func (r *Router) compose(handler http.Handler, routeMW ...Middleware) http.Handler {
	all := make([]Middleware, 0, len(r.middlewares)+len(routeMW))
	all = append(all, r.middlewares...)
	all = append(all, routeMW...)
	return wrap(handler, all)
}

// wrap 按由外到内（索引 0 先执行）的顺序包裹 handler。
func wrap(h http.Handler, mws []Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// joinPath 合并前缀与子模式，处理 "/v1" + "/users/{id}" 等拼接。
// 标准库 ServeMux 的 pattern 直接拼接即可（前缀不带尾斜杠）。
func joinPath(prefix, pattern string) string {
	if prefix == "" {
		return pattern
	}
	if pattern == "" {
		return prefix
	}
	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(pattern, "/")
}
