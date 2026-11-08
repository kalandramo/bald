package web

import (
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/middleware/response"
)

// StatusBody 是统一 JSON 错误响应体（决策⑧）。渲染原语归中间件层的叶子包
// （pkg/middleware/response.StatusBody）——本别名保住 web 的公开契约面，实现只此
// 一份（J2 重复税清偿）：键集、嵌套、空值语义与 grpc-gateway 转码的
// google.rpc.Status JSON 完全一致。前端只写一次解析：展示读 message，
// 程序化读 details[0].reason。
type StatusBody = response.StatusBody

// ErrorInfoBody 对齐 errdetails.ErrorInfo 的 protojson 形状（中间件层契约的别名）。
type ErrorInfoBody = response.ErrorInfoBody

// WriteResponse 写入统一 JSON 响应：若 err 非 nil 则转写错误响应，否则写入 data。
// data 为 nil（或零值引用）时仅置 200，不写 body。
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

// StatusOf 把任意 error 构造成统一错误响应体（决策⑧），委托中间件层渲染原语。
// 供需要自定义写出口的调用方（如中间件 AbortWithStatusJSON）复用，保证全部出口同形。
// 语义见 response.StatusOf。
func StatusOf(err error) StatusBody {
	return response.StatusOf(err)
}

// ErrorResponse 把任意 error 写成统一错误响应体（决策⑧，grpcerr.ToStatus 的
// HTTP 对偶），委托中间件层 response.RenderError——错误渲染只此一份实现，web 只剩绑定。
// HTTP 状态码 = httperr.CodeToHTTP(code)，body 见 StatusOf。
func ErrorResponse(c *gin.Context, err error) {
	response.RenderError(c, err)
}

// isEmpty 判断响应数据是否为 nil 或零值引用（map/slice/ptr/func/chan 的 nil）。
func isEmpty[T any](data T) bool {
	v := reflect.ValueOf(data)
	switch v.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Func, reflect.Chan, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}
