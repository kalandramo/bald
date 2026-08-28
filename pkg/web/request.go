package web

import (
	"context"

	"github.com/gin-gonic/gin"
)

// HandlerFunc 是一个 HTTP 处理器：接收已绑定请求的泛型对象，返回响应数据与错误。
// 第一个参数为 context.Context（业务不应依赖 gin），请求绑定已由 Handle* 完成。
type HandlerFunc[T any, R any] func(ctx context.Context, req T) (R, error)

// HandleJSONRequest 仅从 JSON body 绑定请求，适合纯 JSON 接口。
func HandleJSONRequest[T any, R any](c *gin.Context, handler HandlerFunc[T, R]) {
	var req T
	simple := []Binder{JSON}
	if err := Bind(c, &req, simple); err != nil {
		resp := AsError(err)
		ErrorResponse(c, resp)
		return
	}
	data, err := handler(c, req)
	if err != nil {
		resp := AsError(err)
		ErrorResponse(c, resp)
		return
	}
	WriteResponse(c, data)
}

// HandleUriRequest 仅从 URI 路径变量绑定请求，适合路径即全部参数的接口。
func HandleUriRequest[T any, R any](c *gin.Context, handler HandlerFunc[T, R]) {
	var req T
	binders := []Binder{URI}
	if err := Bind(c, &req, binders); err != nil {
		resp := AsError(err)
		ErrorResponse(c, resp)
		return
	}
	data, err := handler(c, req)
	if err != nil {
		resp := AsError(err)
		ErrorResponse(c, resp)
		return
	}
	WriteResponse(c, data)
}

// HandleQueryRequest 仅从 URL 查询参数绑定请求，适合过滤/分页接口。
func HandleQueryRequest[T any, R any](c *gin.Context, handler HandlerFunc[T, R]) {
	var req T
	binders := []Binder{Query}
	if err := Bind(c, &req, binders); err != nil {
		resp := AsError(err)
		ErrorResponse(c, resp)
		return
	}
	data, err := handler(c, req)
	if err != nil {
		resp := AsError(err)
		ErrorResponse(c, resp)
		return
	}
	WriteResponse(c, data)
}

// HandleAllRequest 组合 URI → Query → JSON 多源绑定（URI 优先级最低，JSON 最高），
// 并在全部绑定完成后执行 validators（若提供）。完整覆盖 onexstack 的
// server.HandlerWrapper 动机，但避免了其"先 Validate 再 Bind"的顺序陷阱。
func HandleAllRequest[T any, R any](c *gin.Context, handler HandlerFunc[T, R], validators ...Validator[T]) {
	var req T
	all := []Binder{URI, Query, JSON}
	if err := Bind(c, &req, all, validators...); err != nil {
		resp := AsError(err)
		ErrorResponse(c, resp)
		return
	}
	data, err := handler(c, req)
	if err != nil {
		resp := AsError(err)
		ErrorResponse(c, resp)
		return
	}
	WriteResponse(c, data)
}
