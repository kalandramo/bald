package baldjwt

import (
	"context"
	"testing"
	"time"

	"github.com/kalandramo/bald/pkg/authn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoundTrip(t *testing.T) {
	secret := "test-secret-not-leaked"
	a := NewAuthenticator(secret)

	c := authn.AuthClaims{
		Subject:  "u-1",
		TenantID: "t-9",
		Name:     "Alice",
		Scopes:   []string{"user:read"},
		Roles:    []string{"member"},
		Issuer:   "bald",
	}
	tok, err := IssueToken(secret, c, time.Hour)
	require.NoError(t, err)

	got, err := a.AuthenticateToken(tok)
	require.NoError(t, err)
	assert.Equal(t, "u-1", got.Subject)
	assert.Equal(t, "t-9", got.TenantID)
	assert.Equal(t, "Alice", got.Name)
	assert.True(t, got.HasScope("user:read"))
	assert.True(t, got.HasRole("member"))
	assert.False(t, got.Expired())

	// Authenticate 从 context 读取 token。
	ctx := authn.ContextWithToken(context.Background(), tok)
	got2, err := a.Authenticate(ctx)
	require.NoError(t, err)
	assert.Equal(t, got.Subject, got2.Subject)
}

func TestExpiredToken(t *testing.T) {
	secret := "s"
	a := NewAuthenticator(secret)
	tok, err := IssueToken(secret, authn.AuthClaims{Subject: "u"}, -time.Hour)
	require.NoError(t, err)
	_, err = a.AuthenticateToken(tok)
	assert.Error(t, err)
}

func TestAlgorithmNoneRejected(t *testing.T) {
	// 用 none 算法伪造的 token 必须被拒（算法白名单）。
	noneTok := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1In0."
	a := NewAuthenticator("secret")
	_, err := a.AuthenticateToken(noneTok)
	assert.Error(t, err)
}

func TestEmptySecretPanics(t *testing.T) {
	assert.Panics(t, func() { NewAuthenticator("") })
}
