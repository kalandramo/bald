package web

// Handler 是业务 HTTP 模块的接口。路由不集中注册，而由每个业务模块在自己的
// ApplyTo 里挂载——这与 osbuilder 模板的 c.Handler.ApplyTo(v1, authMiddlewares...)
// 一致：路由属于谁就画在谁的模块里，认证等中间件由装配层注入，权限边界不落在
// 业务模块手里。
type Handler interface {
	// Name 返回模块名，用于日志/指标标签。
	Name() string
	// ApplyTo 把本模块的路由挂载到传入的 router，middlewares 为装配层注入的
	// 路由级中间件（典型如认证）。
	ApplyTo(r *Router, middlewares ...Middleware) error
}
