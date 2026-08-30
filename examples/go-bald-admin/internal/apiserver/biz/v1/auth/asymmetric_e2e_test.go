package auth

import (
	"context"
	"testing"
	"time"

	"github.com/kalandramo/bald/pkg/authn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// TestRSA_SignWithPrivate_VerifyWithPublic 验证 M6.5 非对称方案：
// 签发方持私钥、验签方只持公钥，二者密钥分离，验签方无法伪造。
func TestRSA_SignWithPrivate_VerifyWithPublic(t *testing.T) {
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}

	claims := authn.AuthClaims{
		Issuer:   "go-bald-admin",
		Subject:  "u-1",
		TenantID: "t-default",
		Roles:    []string{"admin"},
		Name:     "Alice",
	}

	// Login 经 Signer（私钥）签发。
	tok, err := bootstrappkg.Signer.IssueToken(claims, time.Hour)
	require.NoError(t, err)

	// 注入拦截器的 Authenticator 只持公钥，应能验签。
	got, err := bootstrappkg.Authenticator.AuthenticateToken(tok)
	require.NoError(t, err)
	assert.Equal(t, "u-1", got.Subject)
	assert.Equal(t, "t-default", got.TenantID)
}
