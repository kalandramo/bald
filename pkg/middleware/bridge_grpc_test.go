// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package middleware

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

// TestUnaryBridgeInjectsTransporter 钉住 gRPC 一元桥：Transporter 注入、
// Operation=完整方法名、失败码在最内层被观测。
func TestUnaryBridgeInjectsTransporter(t *testing.T) {
	var seenOp string
	var seenCode codes.Code
	m := func(next Handler) Handler {
		return func(ctx context.Context, _ interface{}) (interface{}, error) {
			tr, ok := FromServerContext(ctx)
			if !ok {
				t.Fatal("transporter not injected")
			}
			seenOp = tr.Operation()
			_, err := next(ctx, nil)
			seenCode = tr.GRPCCode()
			return nil, err
		}
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Service/Method"}
	handler := func(ctx context.Context, _ interface{}) (interface{}, error) {
		return nil, errors.New("boom")
	}

	_, err := UnaryServerInterceptor(m)(context.Background(), nil, info, handler)
	if err == nil {
		t.Fatal("expected handler error passthrough")
	}
	if seenOp != "/pkg.Service/Method" {
		t.Errorf("Operation = %q", seenOp)
	}
	if seenCode != codes.Unknown {
		t.Errorf("grpc code observed = %v, want Unknown（普通 error 的默认归类）", seenCode)
	}
}

// TestUnaryBridgeReplyHeaderRoundTrip 钉住响应头通道：链内写 ReplyHeader 经同
// 引用立即可见（flush 分支 len(replyMD)>0 在真实 gRPC 调用面生效，单测覆盖
// 写入侧的 map 同引用不变量）。
func TestUnaryBridgeReplyHeaderRoundTrip(t *testing.T) {
	m := func(next Handler) Handler {
		return func(ctx context.Context, _ interface{}) (interface{}, error) {
			tr, _ := FromServerContext(ctx)
			tr.ReplyHeader().Set("x-reply", "yes")
			if tr.ReplyHeader().Get("x-reply") != "yes" {
				t.Fatal("ReplyHeader write not visible through same reference")
			}
			return nil, nil
		}
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/M"}
	if _, err := UnaryServerInterceptor(m)(metadata.NewIncomingContext(context.Background(), metadata.MD{}), nil, info,
		func(ctx context.Context, _ interface{}) (interface{}, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
}

// TestUnaryBridgeHappyPath 成功路径：gRPC 状态写 OK，响应透传。
func TestUnaryBridgeHappyPath(t *testing.T) {
	m := func(next Handler) Handler {
		return func(ctx context.Context, _ interface{}) (interface{}, error) {
			tr, _ := FromServerContext(ctx)
			_, _ = next(ctx, nil)
			if tr.GRPCCode() != codes.OK {
				t.Errorf("grpc code mid-chain = %v, want OK", tr.GRPCCode())
			}
			return "ok", nil
		}
	}

	resp, err := UnaryServerInterceptor(m)(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/M"},
		func(ctx context.Context, _ interface{}) (interface{}, error) { return "ok", nil })
	if err != nil || resp != "ok" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}

// TestStreamBridgeContextAndStatus 流桥：wrapped stream 的 ctx 带 Transporter，
// 业务 handler 的错误码在最内层观测。
func TestStreamBridgeContextAndStatus(t *testing.T) {
	var seenOp string
	m := func(next Handler) Handler {
		return func(ctx context.Context, _ interface{}) (interface{}, error) {
			tr, _ := FromServerContext(ctx)
			seenOp = tr.Operation()
			_, err := next(ctx, nil)
			return nil, err
		}
	}

	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/Stream", IsServerStream: true}
	err := StreamServerInterceptor(m)(nil, &fakeServerStream{ctx: context.Background()}, info,
		func(srv interface{}, stream grpc.ServerStream) error {
			return errors.New("stream boom")
		})
	if err == nil {
		t.Fatal("expected stream handler error passthrough")
	}
	if seenOp != "/pkg.Svc/Stream" {
		t.Errorf("Operation = %q", seenOp)
	}
}

// TestStreamBridgeHandlerSeesTransporterCtx 业务流 handler 从 stream.Context()
// 读到 Transporter（wrappedServerStream 的覆写点）。
func TestStreamBridgeHandlerSeesTransporterCtx(t *testing.T) {
	m := Passthrough()
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/Stream", IsServerStream: true}
	err := StreamServerInterceptor(m)(nil, &fakeServerStream{ctx: context.Background()}, info,
		func(srv interface{}, stream grpc.ServerStream) error {
			if _, ok := FromServerContext(stream.Context()); !ok {
				t.Error("stream context missing transporter")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

// fakeServerStream 是桥测试的最小 ServerStream（单测只触 Context 与 handler）。
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *fakeServerStream) Context() context.Context { return s.ctx }
