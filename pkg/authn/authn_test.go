// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package authn_test

import (
	"context"
	"testing"
	"time"

	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/contextx"
)

// TestContextWithAuthClaimsFull 验证完整 Claims（含 Name/TenantID/Platform）注入后
// 可从 context 读回，且同步写入 contextx 的用户/租户/平台键。
func TestContextWithAuthClaimsFull(t *testing.T) {
	claims := &authn.AuthClaims{
		Subject:  "u1",
		Name:     "alice",
		TenantID: "t1",
		Platform: true,
	}
	ctx := authn.ContextWithAuthClaims(context.Background(), claims)

	got := authn.AuthClaimsFromContext(ctx)
	if got == nil || got.Subject != "u1" {
		t.Fatalf("AuthClaims 读回失败: %+v", got)
	}
	if contextx.UserIDFromContext(ctx) != "u1" {
		t.Fatal("UserID 未同步")
	}
	if contextx.UsernameFromContext(ctx) != "alice" {
		t.Fatal("Username 未同步")
	}
	if contextx.TenantIDFromContext(ctx) != "t1" {
		t.Fatal("TenantID 未同步")
	}
	if !contextx.PlatformFromContext(ctx) {
		t.Fatal("Platform 未同步")
	}
}

// TestContextWithAuthClaimsPartial 验证仅 Subject（Name/TenantID 为空、Platform 为
// false）时只同步 UserID，不写入 Username/TenantID/Platform 键。
func TestContextWithAuthClaimsPartial(t *testing.T) {
	ctx := authn.ContextWithAuthClaims(context.Background(), &authn.AuthClaims{Subject: "u2"})
	if contextx.UserIDFromContext(ctx) != "u2" {
		t.Fatal("UserID 未同步")
	}
	if contextx.UsernameFromContext(ctx) != "" {
		t.Fatal("空 Name 不应写入 Username")
	}
	if contextx.TenantIDFromContext(ctx) != "" {
		t.Fatal("空 TenantID 不应写入 TenantID")
	}
	if contextx.PlatformFromContext(ctx) {
		t.Fatal("Platform=false 不应写入平台标记")
	}
}

// TestContextWithNilClaims 验证注入 nil 不 panic 且读回 nil。
func TestContextWithNilClaims(t *testing.T) {
	ctx := authn.ContextWithAuthClaims(context.Background(), nil)
	if got := authn.AuthClaimsFromContext(ctx); got != nil {
		t.Fatalf("期望 nil，实际 %+v", got)
	}
}

// TestAuthClaimsFromContextMissing 验证未注入时读回 nil。
func TestAuthClaimsFromContextMissing(t *testing.T) {
	if got := authn.AuthClaimsFromContext(context.Background()); got != nil {
		t.Fatalf("缺失期望 nil，实际 %+v", got)
	}
}

// TestHasScopeHasRole 验证 Scopes/Roles 精确匹配命中与未命中分支。
func TestHasScopeHasRole(t *testing.T) {
	c := &authn.AuthClaims{Scopes: []string{"user:read", "user:write"}, Roles: []string{"admin"}}
	if !c.HasScope("user:read") {
		t.Fatal("HasScope 应命中 user:read")
	}
	if c.HasScope("user:delete") {
		t.Fatal("HasScope 不应命中未声明的 scope")
	}
	if !c.HasRole("admin") {
		t.Fatal("HasRole 应命中 admin")
	}
	if c.HasRole("member") {
		t.Fatal("HasRole 不应命中未声明的 role")
	}
}

// TestExpired 验证三种过期判定：零值永不过期、过去已过期、未来未过期。
func TestExpired(t *testing.T) {
	if (&authn.AuthClaims{}).Expired() {
		t.Fatal("零值 ExpiresAt 应视为永不过期")
	}
	if (&authn.AuthClaims{ExpiresAt: time.Now().Add(-time.Minute)}).Expired() == false {
		t.Fatal("过去的 ExpiresAt 应判定为已过期")
	}
	if (&authn.AuthClaims{ExpiresAt: time.Now().Add(time.Hour)}).Expired() {
		t.Fatal("未来的 ExpiresAt 不应判定为已过期")
	}
}

// TestTokenContextRoundTrip 验证 token 存取与缺失兜底（空串）。
func TestTokenContextRoundTrip(t *testing.T) {
	ctx := authn.ContextWithToken(context.Background(), "abc")
	if got := authn.TokenFromContext(ctx); got != "abc" {
		t.Fatalf("token 期望 abc，实际 %q", got)
	}
	if got := authn.TokenFromContext(context.Background()); got != "" {
		t.Fatalf("缺失期望空串，实际 %q", got)
	}
}
