package web

import (
	"encoding/json"
	"net/http"

	berrors "github.com/kalandramo/bald/pkg/errors"
)

// StatusCoder 是 web 层与错误模型的唯一契约：实现了 StatusCode() 的错误自己
// 决定 HTTP 状态码，未实现的统一 500。pkg/errors.Error 已实现该方法。
type StatusCoder interface {
	StatusCode() int
}

// ErrorResponse 是统一错误响应体，对齐 onexstack 的错误结构。
type ErrorResponse struct {
	Reason   string            `json:"reason,omitempty"`
	Message  string            `json:"message,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// WriteResponse 写出成功或失败响应：成功直接写 data（JSON）；失败写统一
// ErrorResponse 并映射 HTTP 状态码。错误状态码的优先级：实现了 StatusCoder
// 的取 StatusCode()，否则 500。
func WriteResponse(w http.ResponseWriter, data any, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	if data == nil {
		_, _ = w.Write([]byte("{}"))
		return
	}
	_ = json.NewEncoder(w).Encode(data)
}

// writeError 把错误映射成统一错误响应。状态码与结构化字段统一以
// *errors.Error 为准（FromError 内部 errors.As 会拆链，包裹错误也能命中）。
// 仅当错误链中完全找不到 *errors.Error 时，才退回 500 + 原始 Error()。
func writeError(w http.ResponseWriter, err error) {
	resp := ErrorResponse{}

	if wErr, ok := berrors.FromError(err); ok {
		resp.Reason = wErr.Reason
		resp.Message = wErr.Message
		resp.Metadata = wErr.Details
		w.WriteHeader(wErr.StatusCode())
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// 其他错误：理由留空，消息用原始 Error()（如标准库错误、gRPC 透传）。
	resp.Message = err.Error()
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(resp)
}

// AsError 是便捷断言：从 err 链中提取 *berrors.Error（若存在）。
// 等价于 berrors.FromError，放在 web 包便于 handler 内联使用。
func AsError(err error) (*berrors.Error, bool) {
	return berrors.FromError(err)
}
