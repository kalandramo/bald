package authnjwt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kalandramo/bald/pkg/authn"
)

// TestAudienceValidation 复现 AR-23：配置 WithAudience 后——自签 token 带
// aud 通过；同一密钥下签给别的服务（无 aud/aud 错位）的 token 被拒绝（J4）。
func TestAudienceValidation(t *testing.T) {
	secret := testSecret
	a := newAuth(t, WithHMACSecret(secret), WithAudience("bald-demo"))

	tok, err := a.IssueToken(authn.AuthClaims{Subject: "u-1"}, time.Hour)
	require.NoError(t, err)
	if _, err := a.AuthenticateToken(tok); err != nil {
		t.Fatalf("本服务签发的 token 应通过: %v", err)
	}

	// 同一密钥、另一服务视角（aud=other-svc）：bald-demo 的 token 被拒。
	other := newAuth(t, WithHMACSecret(secret), WithAudience("other-svc"))
	if _, err := other.AuthenticateToken(tok); err == nil {
		t.Fatal("aud 错位的 token 应被拒绝（J4 身份不完整 fail-closed）")
	}
}
