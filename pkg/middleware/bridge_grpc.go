// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// bridge_grpc.go 把统一 Middleware 桥接到 gRPC 的 unary 与 stream 拦截器
// （自 onexmesh 移植）。桥向 ctx 注入 Transporter（Kind=GRPC，
// Operation=完整方法名），下游中间件据此读协议元数据——不认识 grpc。
package middleware

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// UnaryServerInterceptor 把统一 Middleware 桥接成 gRPC 服务端一元拦截器。
// 它向 ctx 注入 Transporter，并在最内层记录 gRPC 状态码。
func UnaryServerInterceptor(m Middleware) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		// replyMD 与 replyHeader 是同一张 map：写 ReplyHeader() 的中间件改的
		// 就是它，链结束后经 grpc.SetHeader 冲回 gRPC。
		replyMD := metadata.MD{}
		tr := NewTransporter(
			KindGRPC,
			info.FullMethod,
			"",
			NewMetadataHeader(md),
			NewMetadataHeader(replyMD),
		)
		defer ReleaseTransporter(tr)
		ctx = NewServerContext(ctx, tr)

		// 状态在最内层记录而不是链返回后——差别就是可见性的全部理由：链按
		// metrics→logging→tracing 回卷，m(...) 返回后再写状态就没人读了。
		// 包住 handler（与 gin 桥的终端 c.Next() 同位）让链上每一层都在栈上时
		// 状态已就位。
		inner := Handler(func(ctx context.Context, req interface{}) (interface{}, error) {
			resp, err := handler(ctx, req)
			tr.SetGRPCCode(status.Code(err))
			return resp, err
		})

		resp, err := m(inner)(ctx, req)
		if len(replyMD) > 0 {
			// SetHeader 的错误只在真实 gRPC 传输面发生（ctx 必须来自服务端流）——
			// 单测环境会失败，桥语义是尽力冲回，不阻断响应。
			_ = grpc.SetHeader(ctx, replyMD)
		}
		return resp, err
	}
}

// StreamServerInterceptor 把统一 Middleware 桥接成 gRPC 服务端流拦截器：
// 中间件的 req 参数携带服务端流，recovery/logging/metrics/tracing/timeout
// 写一遍即可同时包住流式 RPC。业务流式 handler 收到的流的 ctx 里带注入的
// Transporter。
func StreamServerInterceptor(m Middleware) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		md, _ := metadata.FromIncomingContext(ss.Context())
		// 与 Unary 不同：响应头不在这里冲回 gRPC——流式响应头必须在首条消息
		// 之前发送，与统一链「handler 之后写」的时机冲突。
		tr := NewTransporter(
			KindGRPC,
			info.FullMethod,
			"",
			NewMetadataHeader(md),
			NewMetadataHeader(metadata.MD{}),
		)
		defer ReleaseTransporter(tr)
		ctx := NewServerContext(ss.Context(), tr)
		wrapped := &wrappedServerStream{ServerStream: ss, ctx: ctx}

		// 状态在最内层记录，理由同 UnaryServerInterceptor。
		h := m(func(ctx context.Context, req interface{}) (interface{}, error) {
			err := handler(srv, wrapped)
			tr.SetGRPCCode(status.Code(err))
			return nil, err
		})
		_, err := h(ctx, srv)
		return err
	}
}

// wrappedServerStream 覆写 Context，把携带 Transporter 的 context 暴露给
// 业务流式 handler。
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context { return w.ctx }
