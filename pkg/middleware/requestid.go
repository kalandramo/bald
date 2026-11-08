// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// requestid.go 是统一形态的请求 ID 中间件：读 Transporter 的 RequestHeader、
// 回写 ReplyHeader——一份实现同时服务 gRPC（metadata）与 HTTP（http.Header），
// 这是 Transporter 抹平能力的直接受益者（两链旧实现各 39 行，合并后一份）。

package middleware

import (
	"context"

	"github.com/google/uuid"

	"github.com/kalandramo/bald/pkg/contextx"
)

// defaultRequestIDHeader 与 gin 侧契约注释点名的默认头名一致。
const defaultRequestIDHeader = "X-Request-ID"

// RequestIDConfig 配置统一请求 ID 中间件。
type RequestIDConfig struct {
	// HeaderName 是读取/回写请求 ID 的头名。空值回退默认 "X-Request-ID"。
	// 对应契约 request_id.header_name（批次 3 统一段后双协议共用）。
	HeaderName string
}

// RequestID 确保每个请求都有唯一请求 ID：优先从 Transporter 的请求头获取，
// 缺失时生成新 UUID；写入 ReplyHeader（gRPC 侧由桥冲回 metadata，HTTP 侧落在
// 响应头）并注入 context（contextx.WithRequestID）。
//
// 本件是 Transporter 抹平的样板：gin 侧旧实现读 c.Request.Header、gRPC 侧读
// metadata——统一后读写头完全经 Transporter，协议差异消失。
func RequestID(cfg ...RequestIDConfig) Middleware {
	header := defaultRequestIDHeader
	if len(cfg) > 0 && cfg[0].HeaderName != "" {
		header = cfg[0].HeaderName
	}
	return func(next Handler) Handler {
		return func(ctx context.Context, _ interface{}) (interface{}, error) {
			tr, ok := FromServerContext(ctx)
			if !ok {
				// 无 Transporter 的调用面（如直调统一链的单测）——生成 ID 仅注
				// 入 context，不做头读写。
				requestID := uuid.New().String()
				return next(contextx.WithRequestID(ctx, requestID), nil)
			}

			requestID := tr.RequestHeader().Get(header)
			if requestID == "" {
				requestID = uuid.New().String()
			}
			tr.ReplyHeader().Set(header, requestID)

			return next(contextx.WithRequestID(ctx, requestID), nil)
		}
	}
}
