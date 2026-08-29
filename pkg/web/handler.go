// Package web 提供面向 gin 的 HTTP 请求处理流水线：绑定、校验、响应。
//
// 设计取舍：本包强绑定 gin（直接消费 *gin.Context），因此只支持 gin 与
// grpc-gateway（后者复用同一套 biz/handler 逻辑，经 grpc-gateway 的 HTTP
// transcoding 落到 gin 之外的标准库 http）。这与 onexstack 的 pkg/core 思路一致，
// 但错误语义复用 bald 自有的 pkg/berrors（传输中立的 Code/Reason）。
//
// 业务只需实现一个 Handler：
//
//	var h web.Handler[greetReq, greetResp] = func(ctx, req *greetReq) (greetResp, error)
//
// 然后在路由里调用入口函数：
//
//	engine.POST("/v1/greet", func(c *gin.Context) {
//	    web.HandleJSONRequest(c, h, myValidator)
//	})
//
// 也可用方法值直接传 biz 层实现，参考 miniblog handler/http 分层。
package web

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/berrors"
)

// Handler 是业务处理函数：接收已绑定并校验的请求指针 *T，返回响应 R 或 error。
// 与 onexstack core.Handler 完全一致。
type Handler[T any, R any] func(ctx context.Context, req *T) (R, error)

// Validator 是请求校验器。校验错误应返回 pkg/berrors 中标识为校验错误的错误
// （如 errors.BadRequest），从而被 ErrorResponse 统一映射为 HTTP 400。
type Validator[T any] func(ctx context.Context, req *T) error

// HandleJSONRequest 仅从 JSON 请求体绑定，并调用 handler。用于 POST/PUT 等。
func HandleJSONRequest[T any, R any](
	c *gin.Context, handler Handler[T, R], validators ...Validator[T],
) {
	var req T
	if err := c.ShouldBindJSON(&req); err != nil {
		ErrorResponse(c, errors.BadRequest(err.Error()))
		return
	}
	if err := runValidators(c, &req, validators...); err != nil {
		ErrorResponse(c, err)
		return
	}
	data, err := handler(c.Request.Context(), &req)
	WriteResponse(c, data, err)
}

// HandleQueryRequest 仅从 URL 查询参数绑定，并调用 handler。用于 GET 列表/过滤。
func HandleQueryRequest[T any, R any](
	c *gin.Context, handler Handler[T, R], validators ...Validator[T],
) {
	var req T
	if err := c.ShouldBindQuery(&req); err != nil {
		ErrorResponse(c, errors.BadRequest(err.Error()))
		return
	}
	if err := runValidators(c, &req, validators...); err != nil {
		ErrorResponse(c, err)
		return
	}
	data, err := handler(c.Request.Context(), &req)
	WriteResponse(c, data, err)
}

// HandleUriRequest 仅从路径参数（gin 的 uri tag）绑定，并调用 handler。
func HandleUriRequest[T any, R any](
	c *gin.Context, handler Handler[T, R], validators ...Validator[T],
) {
	var req T
	if err := c.ShouldBindUri(&req); err != nil {
		ErrorResponse(c, errors.BadRequest(err.Error()))
		return
	}
	if err := runValidators(c, &req, validators...); err != nil {
		ErrorResponse(c, err)
		return
	}
	data, err := handler(c.Request.Context(), &req)
	WriteResponse(c, data, err)
}

// HandleAllRequest 依次绑定 URI → Query → JSON（后者覆盖前者），并调用 handler。
// 保留 bald 的 Bind 语义（URI 优先级最低、JSON 最高），并用 gin 原生绑定实现。
func HandleAllRequest[T any, R any](
	c *gin.Context, handler Handler[T, R], validators ...Validator[T],
) {
	var req T
	if err := ShouldBindAll(c, &req); err != nil {
		ErrorResponse(c, err)
		return
	}
	if err := runValidators(c, &req, validators...); err != nil {
		ErrorResponse(c, err)
		return
	}
	data, err := handler(c.Request.Context(), &req)
	WriteResponse(c, data, err)
}

// runValidators 依次执行校验器。nil 校验器被跳过。
func runValidators[T any](c *gin.Context, req *T, validators ...Validator[T]) error {
	for _, v := range validators {
		if v == nil {
			continue
		}
		if err := v(c.Request.Context(), req); err != nil {
			return err
		}
	}
	return nil
}
