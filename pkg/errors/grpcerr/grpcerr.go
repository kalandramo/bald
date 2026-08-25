// Package grpcerr 提供 pkg/errors.Error 与 gRPC status 的双向转换，并携带
// errdetails.ErrorInfo（Reason + Details），保证跨服务错误语义透传。
//
// 该子包是核心包 pkg/errors 的"可选桥接"：仅在用 gRPC 传输的项目里 import，
// 调用方在传输边界各写一行——server 拦截器收口用 ToStatus，client 收到错误用
// FromStatus。核心包因此保持零 gRPC 依赖。
//
// Kratos 的 api/errors 同样实现 GRPCStatus() + ErrorInfo，故 FromStatus 天然
// 能解析 Kratos 错误；反过来 ToStatus 产出的 status 也能被 Kratos 客户端理解，
// 实现跨生态互通。
package grpcerr

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/pkg/errors"
)

// ToStatus 把错误（链中任意 *errors.Error）转成 gRPC status，并附
// errdetails.ErrorInfo{Reason, Details}。
//
//   - 命中 *errors.Error：用其 Code（→ gRPC codes）、Message、Reason、Details 构造。
//   - 未命中（原生 error 或 gRPC status）：退回 Unknown + err.Error()，语义不丢。
func ToStatus(err error) *status.Status {
	if err == nil {
		return status.New(codes.OK, "")
	}

	if wErr, ok := errors.FromError(err); ok {
		st := status.New(codes.Code(wErr.Code), wErr.Message)
		if wErr.Reason != "" || len(wErr.Details) > 0 {
			details := errdetails.ErrorInfo{Reason: wErr.Reason, Metadata: wErr.Details}
			if stWith, derr := st.WithDetails(&details); derr == nil {
				return stWith
			}
		}
		return st
	}

	return status.Convert(err)
}

// FromStatus 把 gRPC status 解析回 *errors.Error：从 ErrorInfo 恢复 Reason 与
// Details，HTTP/gRPC 双栈语义在接收端闭环。无法解析时退回 Unknown。
func FromStatus(st *status.Status) error {
	if st == nil {
		return nil
	}

	ret := errors.New(uint32(st.Code()), st.Message())
	for _, detail := range st.Details() {
		if typed, ok := detail.(*errdetails.ErrorInfo); ok {
			if typed.Reason != "" {
				ret.Reason = typed.Reason
			}
			if len(typed.Metadata) > 0 {
				ret.Details = typed.Metadata
			}
			return ret
		}
	}
	return ret
}
