package authnjwt

import (
	"context"
	"testing"
	"time"

	"github.com/kalandramo/bald/pkg/authn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleClaims() authn.AuthClaims {
	return authn.AuthClaims{
		Subject:  "u-1",
		TenantID: "t-9",
		Name:     "Alice",
		Scopes:   []string{"user:read"},
		Roles:    []string{"member"},
		Issuer:   "bald",
	}
}

func TestRoundTrip_HMAC(t *testing.T) {
	secret := []byte("test-secret-not-leaked")
	a := NewAuthenticator(WithHMACSecret(secret))

	c := sampleClaims()
	tok, err := a.IssueToken(c, time.Hour)
	require.NoError(t, err)

	got, err := a.AuthenticateToken(tok)
	require.NoError(t, err)
	assert.Equal(t, "u-1", got.Subject)
	assert.Equal(t, "t-9", got.TenantID)
	assert.Equal(t, "Alice", got.Name)
	assert.True(t, got.HasScope("user:read"))
	assert.True(t, got.HasRole("member"))
	assert.False(t, got.Expired())

	ctx := authn.ContextWithToken(context.Background(), tok)
	got2, err := a.Authenticate(ctx)
	require.NoError(t, err)
	assert.Equal(t, got.Subject, got2.Subject)
}

func TestExpiredToken(t *testing.T) {
	secret := []byte("s")
	a := NewAuthenticator(WithHMACSecret(secret))
	tok, err := a.IssueToken(authn.AuthClaims{Subject: "u"}, -time.Hour)
	require.NoError(t, err)
	_, err = a.AuthenticateToken(tok)
	assert.Error(t, err)
}

func TestAlgorithmNoneRejected(t *testing.T) {
	noneTok := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1In0."
	a := NewAuthenticator(WithHMACSecret([]byte("secret")))
	_, err := a.AuthenticateToken(noneTok)
	assert.Error(t, err)
}

func TestVerifyOnlyCannotSign(t *testing.T) {
	// 仅注入公钥的验签方不应能签发。
	priv, err := GenerateRSA(2048)
	require.NoError(t, err)
	verifier := NewAuthenticator(WithRSAKeys(nil, &priv.PublicKey))
	_, err = verifier.IssueToken(sampleClaims(), time.Hour)
	assert.Error(t, err)
}

func TestRoundTrip_RSA(t *testing.T) {
	priv, err := GenerateRSA(2048)
	require.NoError(t, err)

	// 签发方持私钥，验签方只持公钥——密钥解耦是核心收益。
	signer := NewAuthenticator(WithRSAKeys(priv, &priv.PublicKey))
	verifier := NewAuthenticator(WithRSAKeys(nil, &priv.PublicKey))

	tok, err := signer.IssueToken(sampleClaims(), time.Hour)
	require.NoError(t, err)

	got, err := verifier.AuthenticateToken(tok)
	require.NoError(t, err)
	assert.Equal(t, "u-1", got.Subject)
	assert.Equal(t, "t-9", got.TenantID)
}

func TestRSA_ForgedByOtherKeyRejected(t *testing.T) {
	privA, err := GenerateRSA(2048)
	require.NoError(t, err)
	privB, err := GenerateRSA(2048)
	require.NoError(t, err)

	signer := NewAuthenticator(WithRSAKeys(privA, &privA.PublicKey))
	evilVerifier := NewAuthenticator(WithRSAKeys(nil, &privB.PublicKey)) // 错误公钥

	tok, err := signer.IssueToken(sampleClaims(), time.Hour)
	require.NoError(t, err)

	_, err = evilVerifier.AuthenticateToken(tok)
	assert.Error(t, err, "用错误公钥验签必须失败")
}

func TestRoundTrip_ECDSA(t *testing.T) {
	priv, err := GenerateECDSA(nil)
	require.NoError(t, err)

	signer := NewAuthenticator(WithECDSAKeys(priv, &priv.PublicKey))
	verifier := NewAuthenticator(WithECDSAKeys(nil, &priv.PublicKey))

	tok, err := signer.IssueToken(sampleClaims(), time.Hour)
	require.NoError(t, err)

	got, err := verifier.AuthenticateToken(tok)
	require.NoError(t, err)
	assert.Equal(t, "u-1", got.Subject)
}
