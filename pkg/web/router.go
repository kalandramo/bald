// Package web 提供基于 gin 的轻量 Web 能力：路由分组、中间件链、
// 泛型请求绑定流水线与统一响应。
//
// 设计要点（详见 docs/devel/zh-CN/路由注册与绑定设计.md）：
//   - Router 是对 gin.Engine 的极薄封装，保留分组（Group）与中间件链能力。
//   - 业务路由归属业务模块：Handler 接口用 ApplyTo(router, middlewares...) 挂载，
//     认证等中间件由装配层注入。
//   - Router 实现 http.Handler，可直接传入 server.NewHTTPServer，服务器层零改动。
package web

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Middleware 是一条 gin 中间件：包裹 next，返回 gin.HandlerFunc。
type Middleware = gin.HandlerFunc

// Router 是基于 gin.Engine 的路由器，支持分组与中间件链。
type Router struct {
	engine *gin.Engine
	group  *gin.RouterGroup
}

// NewRouter 创建根路由器，middlewares 为全局（根）中间件链。
func NewRouter(middlewares ...Middleware) *Router {
	engine := gin.New()
	engine.Use(middlewares...)
	return &Router{engine: engine, group: engine.Group("/")}
}

// Group 返回带路径前缀 + 中间件子链的子路由器。子链在根中间件之后执行：
// 顺序为 根中间件 → 子组中间件 → 路由中间件（由外到内）。
func (r *Router) Group(prefix string, middlewares ...Middleware) *Router {
	return &Router{
		engine: r.engine,
		group:  r.group.Group(prefix, middlewares...),
	}
}

// Handle 注册方法级路由。method 为空字符串时匹配全部方法；pattern 遵循 gin 的
// 路径语法（如 "/v1/users" 或 "/v1/users/:id"）。路由级中间件在路由级 handler
// 之前执行，与组中间件组成由外到内链。
func (r *Router) Handle(method, pattern string, handler gin.HandlerFunc, middlewares ...Middleware) {
	handlers := make([]gin.HandlerFunc, 0, len(middlewares)+1)
	handlers = append(handlers, middlewares...)
	handlers =  append(handlers, handler)
	r.group.Handle(method, pattern, handlers...)
}

// HandleFunc 是 Handle 的便捷形式，接收裸 func(*gin.Context)。
func (r *Router) HandleFunc(method, pattern string, h func(*gin.Context), middlewares ...Middleware) {
	r.Handle(method, pattern, gin.HandlerFunc(h), middlewares...)
}

// Engine 暴露底层 gin.Engine，便于注册原始 gin 路由或中间件（如静态文件）。
func (r *Router) Engine() *gin.Engine { return r.engine }

// ServeHTTP 让 *Router 成为合法 http.Handler——这是服务器层零改动的关键。
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.engine.ServeHTTP(w, req)
}
