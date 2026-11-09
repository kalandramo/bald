// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package contextx_test

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/pkg/contextx"
)

// TestWithFromRoundTrip 逐一验证六组 With*/From* 存取：写入后读回同值。
func TestWithFromRoundTrip(t *testing.T) {
	ctx := context.Background()
	ctx = contextx.WithUserID(ctx, "u1")
	ctx = contextx.WithUsername(ctx, "alice")
	ctx = contextx.WithTraceID(ctx, "trace-1")
	ctx = contextx.WithRequestID(ctx, "req-1")
	ctx = contextx.WithTenantID(ctx, "t1")
	ctx = contextx.WithPlatform(ctx)

	if got := contextx.UserIDFromContext(ctx); got != "u1" {
		t.Fatalf("UserID 期望 u1，实际 %q", got)
	}
	if got := contextx.UsernameFromContext(ctx); got != "alice" {
		t.Fatalf("Username 期望 alice，实际 %q", got)
	}
	if got := contextx.TraceIDFromContext(ctx); got != "trace-1" {
		t.Fatalf("TraceID 期望 trace-1，实际 %q", got)
	}
	if got := contextx.RequestIDFromContext(ctx); got != "req-1" {
		t.Fatalf("RequestID 期望 req-1，实际 %q", got)
	}
	if got := contextx.TenantIDFromContext(ctx); got != "t1" {
		t.Fatalf("TenantID 期望 t1，实际 %q", got)
	}
	if !contextx.PlatformFromContext(ctx) {
		t.Fatal("Platform 期望 true")
	}
}

// TestFromContextMissingDefaults 验证空 context 上各 From* 的缺失兜底：字符串返回
// 空串、平台标记返回 false（fail-closed，隔离生效）。
func TestFromContextMissingDefaults(t *testing.T) {
	ctx := context.Background()
	if got := contextx.UserIDFromContext(ctx); got != "" {
		t.Fatalf("UserID 缺失期望空串，实际 %q", got)
	}
	if got := contextx.UsernameFromContext(ctx); got != "" {
		t.Fatalf("Username 缺失期望空串，实际 %q", got)
	}
	if got := contextx.TraceIDFromContext(ctx); got != "" {
		t.Fatalf("TraceID 缺失期望空串，实际 %q", got)
	}
	if got := contextx.RequestIDFromContext(ctx); got != "" {
		t.Fatalf("RequestID 缺失期望空串，实际 %q", got)
	}
	if got := contextx.TenantIDFromContext(ctx); got != "" {
		t.Fatalf("TenantID 缺失期望空串，实际 %q", got)
	}
	if contextx.PlatformFromContext(ctx) {
		t.Fatal("Platform 缺失期望 false")
	}
}
