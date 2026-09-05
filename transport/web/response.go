package web

import (
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/httperr"
)

// ErrorBody 是统一的 JSON 错误响应体，对齐 onexstack core.ErrorBody。
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 是错误体的内层结构。
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteResponse 写入统一 JSON 响应：若 err 非 nil 则转写错误响应，否则写入 data。
// data 为 nil（或零值引用）时仅置 200，不写 body，对齐 onexstack core.WriteResponse。
func WriteResponse[T any](c *gin.Context, data T, err error) {
	if err != nil {
		ErrorResponse(c, err)
		return
	}
	if isEmpty(data) {
		c.Status(http.StatusOK)
		return
	}
	c.JSON(http.StatusOK, data)
}

// ErrorResponse 把任意 error 统一写成 {error:{code,message}} JSON。
//
// 语义来源：若 err 可经 errors.FromError 还原为 *errors.Error，则取其 StatusCode
// 与 Reason/Message；否则兜底为 errors.Unknown（HTTP 500）。校验错误（由
// errors.BadRequest 等构造）自然映射为 400。
func ErrorResponse(c *gin.Context, err error) {
	var werr *errors.Error
	if e, ok := errors.FromError(err); ok {
		werr = e
	} else {
		werr = errors.Internal(err.Error())
	}
	c.JSON(httperr.StatusCode(werr), ErrorBody{
		Error: ErrorDetail{
			Code:    werr.Reason,
			Message: werr.Message,
		},
	})
}

// isEmpty 判断响应数据是否为 nil 或零值引用（map/slice/ptr/func/chan 的 nil）。
func isEmpty[T any](data T) bool {
	v := reflect.ValueOf(data)
	switch v.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Func, reflect.Chan, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}
