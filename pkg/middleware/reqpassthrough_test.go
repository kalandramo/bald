// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package middleware

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
)

// TestUnifiedChainPreservesRequest_GRPCUnary 钉住 req 透传回归：统一内置件
// （Recovery/RequestID/Timeout/RateLimit/Authn/Authz/Audit）经 gRPC unary 桥
// 链式装配后，业务 handler 必须收到原始请求消息。旧实现 next(ctx, nil) 会把
// 消息 nil 化——gRPC unary 桥终端 handler(ctx, req) 直接消费链传下的 req，
// 任何一层丢 req 都会让业务 handler 拿到 nil。HTTP/流式桥终端忽略 req（用
// 闭包里的 c/srv），故此回归只在 unary 面可见。
func TestUnifiedChainPreservesRequest_GRPCUnary(t *testing.T) {
	type message struct{ ID int }
	sentinel := &message{ID: 42}

	var got any
	terminal := func(_ context.Context, req interface{}) (interface{}, error) {
		got = req
		return "resp", nil
	}

	chain := Chain(
		Recovery(),
		RequestID(),
		Timeout(time.Second),
		RateLimit(nil, false),
		Authn(nil),
		Authz(nil),
		Audit(),
	)
	un := UnaryServerInterceptor(chain)
	resp, err := un(context.Background(), sentinel, &grpc.UnaryServerInfo{FullMethod: "/svc/Get"}, terminal)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if resp != "resp" {
		t.Errorf("resp = %v, want resp", resp)
	}
	if got != sentinel {
		t.Errorf("业务 handler 收到 req = %v, want 原始消息 %p（req 透传不得 nil 化）", got, sentinel)
	}
}