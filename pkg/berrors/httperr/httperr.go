// Package httperr 是 berrors 错误模型在 HTTP/gin 传输边界的桥接子包，
// 与 grpcerr（gRPC 边界）对称——核心包保持传输中立、零 gRPC/net/http/gin 依赖，
// 所有「跨传输协议转换」都外置到对应子包（见 berrors 包注释的零依赖契约）。
//
// 本包刻意只暴露纯函数，不直接 import gin：gin 边界函数（WriteHTTP）留在调用方
// pkg/web 侧，避免错误包反向依赖 web 框架。这样 berrors 的错误包家族里，
// grpcerr 依赖 gRPC、httperr 仅依赖标准库（用 int 表示状态码），互不耦合。
package httperr

import (
	berrors "github.com/kalandramo/bald/pkg/berrors"
)

// 设计决策——为何用 int 而非 http.StatusCode：错误核心包保持零 net/http 依赖，
// 即便在边界子包也只本包需要标准库类型，不牵连上层。用法示例：
//
//	w.WriteHeader(httperr.CodeToHTTP(wErr.Code))
//
// HTTP 到 berrors.Code 的反向映射是非平凡的：正向 Code→HTTP 是多对一
// （多个业务 Code 可能映射到同一 4xx，如 FailedPrecondition 或 OutOfRange），
// 故 HTTPToCode 为每个状态码返回语义上最贴切的 Code。

// codeToHTTP 是权威的 Code → HTTP 状态映射。
var codeToHTTP = map[uint32]int{
	berrors.CodeOK:                 200,
	berrors.CodeCanceled:           499,
	berrors.CodeUnknown:            500,
	berrors.CodeInvalidArgument:    400,
	berrors.CodeDeadlineExceeded:   504,
	berrors.CodeNotFound:           404,
	berrors.CodeAlreadyExists:      409,
	berrors.CodePermissionDenied:   403,
	berrors.CodeResourceExhausted:  429,
	berrors.CodeFailedPrecondition: 400,
	berrors.CodeAborted:            409,
	berrors.CodeOutOfRange:         400,
	berrors.CodeUnimplemented:      501,
	berrors.CodeInternal:           500,
	berrors.CodeUnavailable:        503,
	berrors.CodeDataLoss:           500,
	berrors.CodeUnauthenticated:    401,
}

// httpToCode 是反向 HTTP 状态 → Code 映射。因正向是多对一，每个 HTTP 状态
// 只能唯一落到一个 Code，这里取语义最贴切者。
var httpToCode = map[int]uint32{
	200: berrors.CodeOK,
	400: berrors.CodeInvalidArgument,
	401: berrors.CodeUnauthenticated,
	403: berrors.CodePermissionDenied,
	404: berrors.CodeNotFound,
	409: berrors.CodeAlreadyExists,
	429: berrors.CodeResourceExhausted,
	500: berrors.CodeInternal,
	501: berrors.CodeUnimplemented,
	503: berrors.CodeUnavailable,
	504: berrors.CodeDeadlineExceeded,
}

// CodeToHTTP 返回给定 Code 对应的 HTTP 状态码。未知 Code 兜底 500（Internal）。
// 反向映射见 HTTPToCode。
func CodeToHTTP(code uint32) int {
	if httpStatus, ok := codeToHTTP[code]; ok {
		return httpStatus
	}
	return 500 // 兜底 Internal
}

// HTTPToCode 返回给定 HTTP 状态码对应的 Code。因 Code→HTTP 是多对一，返回的
// Code 是语义上最贴切的一个。未知状态兜底 Unknown。
func HTTPToCode(httpStatus int) uint32 {
	if code, ok := httpToCode[httpStatus]; ok {
		return code
	}
	return berrors.CodeUnknown
}

// StatusCode 返回 *berrors.Error 对应的 HTTP 状态码（CodeToHTTP(Code)）。
// 替代原核心包的 Error.StatusCode() 方法：该方法若留在核心包，会让核心包
// 反向依赖本子包（循环依赖），故上移为边界函数。gin 侧可直接：
//
//	c.AbortWithStatusJSON(httperr.StatusCode(e), gin.H{...})
func StatusCode(e *berrors.Error) int {
	return CodeToHTTP(e.Code)
}
