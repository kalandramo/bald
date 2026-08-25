package web

import (
	"context"
	"net/http"
)

// HandlerFunc 是业务处理函数：输入请求、输出响应与错误。
type HandlerFunc[T any, R any] func(ctx context.Context, req *T) (R, error)

// Validator 是校验函数，在绑定完成后统一执行，首个错误即返回。
type Validator[T any] func(ctx context.Context, obj *T) error

// HandleAllRequest 是把"绑定 → 默认值 → 校验 → 业务 → 响应"焊成一条流水线的
// 泛型入口：URI + Query + JSON 全部绑定，统一校验后交给 handler，最后用
// WriteResponse 写出。对应 onexstack core.HandleAllRequest。
func HandleAllRequest[T any, R any](
	w http.ResponseWriter, r *http.Request,
	handler HandlerFunc[T, R],
	validators ...Validator[T],
) {
	var req T
	if err := Bind(r, &req, []Binder{URI, Query, JSON}, validators...); err != nil {
		WriteResponse(w, nil, err)
		return
	}
	response, err := handler(r.Context(), &req)
	WriteResponse(w, response, err)
}

// HandleJSONRequest 是单源（仅 JSON body）的快捷函数。
func HandleJSONRequest[T any, R any](
	w http.ResponseWriter, r *http.Request,
	handler HandlerFunc[T, R],
	validators ...Validator[T],
) {
	var req T
	if err := Bind(r, &req, []Binder{JSON}, validators...); err != nil {
		WriteResponse(w, nil, err)
		return
	}
	response, err := handler(r.Context(), &req)
	WriteResponse(w, response, err)
}

// HandleQueryRequest 是单源（仅 Query）的快捷函数。
func HandleQueryRequest[T any, R any](
	w http.ResponseWriter, r *http.Request,
	handler HandlerFunc[T, R],
	validators ...Validator[T],
) {
	var req T
	if err := Bind(r, &req, []Binder{Query}, validators...); err != nil {
		WriteResponse(w, nil, err)
		return
	}
	response, err := handler(r.Context(), &req)
	WriteResponse(w, response, err)
}

// HandleUriRequest 是单源（仅 URI path 变量）的快捷函数。
func HandleUriRequest[T any, R any](
	w http.ResponseWriter, r *http.Request,
	handler HandlerFunc[T, R],
	validators ...Validator[T],
) {
	var req T
	if err := Bind(r, &req, []Binder{URI}, validators...); err != nil {
		WriteResponse(w, nil, err)
		return
	}
	response, err := handler(r.Context(), &req)
	WriteResponse(w, response, err)
}
