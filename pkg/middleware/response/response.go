// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package response 持有统一错误响应契约（errors-r08：google.rpc.Status JSON
// 三面一份）。它是中间件层的叶子渲染原语——只依赖 berrors/gin/httperr，不拖入
// 认证/授权/观测等重件，故 transport/web 与 gin 桥都能轻量委托。渲染实现仅此
// 一份（J2 重复税清偿）：web 的 StatusBody/StatusOf/ErrorResponse 是本包的别名
// 与委托，web 只剩绑定。
package response

import (
	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/httperr"
)

// errorInfoType 是 details[0].@type 的固定值，指向 google.rpc.ErrorInfo——
// 与 grpc-gateway 转码 google.rpc.Status 时 errdetails.ErrorInfo 的 Any 类型
// URL 一致（errors-r08：三面一份契约）。
const errorInfoType = "type.googleapis.com/google.rpc.ErrorInfo"

// StatusBody 是统一 JSON 错误响应体（errors-r08）：与 grpc-gateway 转码输出的
// google.rpc.Status JSON 结构同形——键集、嵌套、空值语义完全一致
// （空 message 输出 ""、空 metadata 输出 {}、空 details 输出 []）。
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

// StatusOf 把任意 error 构造成统一错误响应体。语义：若 err 可经
// berrors.FromError 还原为 *berrors.Error，则取其 Code/Message/Reason/Details；
// 其他 error 兜底 INTERNAL，message = err.Error()。Reason 与 Details 皆空时
// details 输出空 slice；有 Reason 或 Details 时输出单条 ErrorInfo，nil metadata
// 归一为 {}（protojson 对空 map 输出 {} 而非 null）。
func StatusOf(err error) StatusBody {
	werr, ok := berrors.FromError(err)
	if !ok {
		werr = berrors.Internal("").WithMessage("%s", err.Error())
	}
	// Details 恒为非 nil slice：protojson 对空 repeated 字段输出 []（而非 null）。
	body := StatusBody{Code: werr.Code, Message: werr.Message, Details: []ErrorInfoBody{}}
	if werr.Reason != "" || len(werr.Details) > 0 {
		metadata := werr.Details
		if metadata == nil {
			metadata = map[string]string{}
		}
		body.Details = []ErrorInfoBody{{
			Type:     errorInfoType,
			Reason:   werr.Reason,
			Metadata: metadata,
		}}
	}
	return body
}

// RenderError 把任意 error 写成统一错误响应体（StatusOf 的写出口）：
// HTTP 状态码 = httperr.CodeToHTTP(code)，body = StatusOf(err)。gin 桥短路
// 与 transport/web.ErrorResponse 共用本函数——错误渲染只此一份实现。
func RenderError(c *gin.Context, err error) {
	body := StatusOf(err)
	c.JSON(httperr.CodeToHTTP(body.Code), body)
}
