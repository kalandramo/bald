package web

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	berrors "github.com/kalandramo/bald/pkg/errors"
)

// StatusCoder 表示对象可声明语义 HTTP 状态码（如 xerrors.Error）。
type StatusCoder interface {
	StatusCode() int
}

// errorBody 是统一的错误响应体。
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// withStatus 同时携带状态码与错误信息。pkg/errors 的 Error 已实现 StatusCoder。
type withStatus struct {
	status int
	err    error
}

func (w *withStatus) Error() string { return w.err.Error() }
func (w *withStatus) Unwrap() error { return w.err }
func (w *withStatus) StatusCode() int { return w.status }

// WrapStatus 把普通 error 包裹上自定义状态码（不会覆盖已有的 StatusCoder）。
func WrapStatus(status int, err error) error {
	if err == nil {
		return nil
	}
	return &withStatus{status: status, err: err}
}

// AsError 把任意 error 规范为带状态码的错误。
//   - 已实现 StatusCoder → 直接复用语义状态码（pkg/errors 错误在此自然生效）；
//   - 否则按 go 1.20+ errors.As 兼容旧式 wrapped httpError；
//   - 兜底 500。
func AsError(err error) error {
	if err == nil {
		return nil
	}
	var sc StatusCoder
	if errors.As(err, &sc) {
		return err
	}
	return &withStatus{status: http.StatusInternalServerError, err: err}
}

// WriteResponse 写统一响应：成功写 data，失败写 ErrorResponse（自动映射状态码）。
// 当 data 为 nil 时不写 body（用于 204/head 等场景）。
func WriteResponse(c *gin.Context, data any) {
	if data == nil {
		c.Status(http.StatusOK)
		return
	}
	c.JSON(http.StatusOK, data)
}

// ErrorResponse 写统一错误响应。err 经 AsError 归一，状态码取自 StatusCoder，
// 兜底 500；校验错误（ValidationError）固定 400。Code 填稳定的 reason，Message
// 填业务消息，对应 pkg/errors 的 Reason/Message 语义。
func ErrorResponse(c *gin.Context, err error) {
	if err == nil {
		return
	}
	resp := AsError(err)
	status := http.StatusInternalServerError
	if sc, ok := resp.(StatusCoder); ok {
		status = sc.StatusCode()
	}
	if isValidation(resp) {
		status = http.StatusBadRequest
	}
	code := http.StatusText(status)
	message := ""
	if e, ok := berrors.FromError(resp); ok {
		code = e.Reason
		message = e.Message
	} else if resp != nil {
		message = resp.Error()
	}
	c.JSON(status, errorBody{
		Error: struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: code, Message: message},
	})
}

// isValidation 判断是否为校验错误（pkg/errors 的 ValidationError 等）。
func isValidation(err error) bool {
	type validator interface{ IsValidation() bool }
	var v validator
	return errors.As(err, &v) && v.IsValidation()
}
