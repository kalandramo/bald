package web

import (
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"

	berrors "github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/httperr"
)

// errorInfoType 是 details[0].@type 的固定值，指向 google.rpc.ErrorInfo——
// 与 grpc-gateway 转码 google.rpc.Status 时 errdetails.ErrorInfo 的 Any 类型
// URL 一致（决策⑧：三面一份契约）。
const errorInfoType = "type.googleapis.com/google.rpc.ErrorInfo"

// StatusBody 是统一 JSON 错误响应体（决策⑧）：与 grpc-gateway 转码输出的
// google.rpc.Status JSON 结构同形——键集、嵌套、空值语义完全一致
// （空 message 输出 ""、空 metadata 输出 {}，仅分隔空白不同）。
// 前端只写一次解析：展示读 message，程序化读 details[0].reason。
type StatusBody struct {
	Code    uint32          `json:"code"`
	Message string          `json:"message"`
	Details []ErrorInfoBody `json:"details"`
}

// ErrorInfoBody 对齐 errdetails.ErrorInfo 的 protojson 形状。
// Domain 恒为空串（bald 不使用该维度），保留字段以与 gateway 输出同形。
type ErrorInfoBody struct {
	Type     string            `json:"@type"`
	Reason   string            `json:"reason"`
	Domain   string            `json:"domain"`
	Metadata map[string]string `json:"metadata"`
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

// StatusOf 把任意 error 构造成统一错误响应体（决策⑧）。ErrorResponse 的
// 单源构造器，供需要自定义写出口的调用方（如中间件 AbortWithStatusJSON）
// 复用，保证全部出口同形。
//
// 语义：若 err 可经 berrors.FromError 还原为 *berrors.Error，则取其
// Code/Message/Reason/Details；其他 error 兜底 INTERNAL，message = err.Error()。
func StatusOf(err error) StatusBody {
	werr, ok := berrors.FromError(err)
	if !ok {
		werr = berrors.Internal("").WithMessage("%s", err.Error())
	}
	// Details 恒为非 nil slice：protojson 对空 repeated 字段输出 []（而非 null）。
	body := StatusBody{Code: werr.Code, Message: werr.Message, Details: []ErrorInfoBody{}}
	if werr.Reason != "" || len(werr.Details) > 0 {
		body.Details = []ErrorInfoBody{{
			Type:     errorInfoType,
			Reason:   werr.Reason,
			Metadata: werr.Details,
		}}
		if body.Details[0].Metadata == nil {
			// protojson 对空 map 字段输出 {}（而非 null），此处对齐。
			body.Details[0].Metadata = map[string]string{}
		}
	}
	return body
}

// ErrorResponse 把任意 error 写成统一错误响应体（决策⑧，grpcerr.ToStatus 的
// HTTP 对偶）。
//
// 语义见 StatusOf；HTTP 状态码 = httperr.CodeToHTTP(code)，message = Message
// （可展示文案——不再把 Error() 日志串当用户文案）；reason 与 details 非空时输出
// details[0]（与 grpcerr.ToStatus 的 WithDetails 条件一致，gateway 转码同形）。
// 其他 error 兜底 INTERNAL（500）、无 details——与 grpc-gateway 对未知 gRPC
// 错误的转码形状一致。
func ErrorResponse(c *gin.Context, err error) {
	c.JSON(httperr.CodeToHTTP(codeOf(err)), StatusOf(err))
}

// codeOf 提取错误的传输类别（berrors.FromError 命中取其 Code，否则 INTERNAL）。
func codeOf(err error) uint32 {
	if werr, ok := berrors.FromError(err); ok {
		return werr.Code
	}
	return berrors.CodeInternal
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
