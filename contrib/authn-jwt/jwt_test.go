package authnjwt

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

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

// TestIssueToken_UniqueJTI —— D6：同一秒内相同 claims 签发必须得到**不同** token。
//
// 缺陷背景（框架缺陷报告 D6，严重度：严重）：
//   - toJWT 不设 RegisteredClaims.ID（JWT 标准 jti）；
//   - iat/nbf/exp 均为秒级 NumericDate；
//   - RS256 对同一输入签名确定（无随机盐）。
// 三者叠加 → 同秒同 claims ⇒ 字节完全相同的 token。后果：
//   - 连续刷新拿到的「新」access_token 与旧值相同（有效期不延展，客户端立即过期）；
//   - 轮换出的 refresh_token 可能与刚被消费的旧值重合，一次性语义失效。
//
// 修法：toJWT 填 ID = uuid.NewString()。
func TestIssueToken_UniqueJTI(t *testing.T) {
	a := NewAuthenticator(WithHMACSecret([]byte("s")))

	// 同一 claims 连续签发两次（同一秒内——测试执行远快于 1 秒）。
	c := sampleClaims()
	tok1, err := a.IssueToken(c, time.Hour)
	require.NoError(t, err)
	tok2, err := a.IssueToken(c, time.Hour)
	require.NoError(t, err)

	if tok1 == tok2 {
		t.Fatalf("同秒同 claims 签发出**相同** token（jti 缺失）:\n  %s\n  %s", tok1, tok2)
	}

	// 两个 token 都应可验证（jti 不影响验签）。
	got1, err := a.AuthenticateToken(tok1)
	require.NoError(t, err)
	got2, err := a.AuthenticateToken(tok2)
	require.NoError(t, err)
	assert.Equal(t, got1.Subject, got2.Subject)

	// 解析出的 jti 应非空且互异（用 jwt 标准 parser 直接读 claims，
	// 不走 AuthenticateToken——它按设计不把 jti 回填到 AuthClaims）。
	p := jwt.NewParser()
	var jc1, jc2 jwtClaims
	_, _, err = p.ParseUnverified(tok1, &jc1)
	require.NoError(t, err)
	_, _, err = p.ParseUnverified(tok2, &jc2)
	require.NoError(t, err)
	assert.NotEmpty(t, jc1.ID, "jti（RegisteredClaims.ID）不应为空")
	assert.NotEmpty(t, jc2.ID, "jti（RegisteredClaims.ID）不应为空")
	assert.NotEqual(t, jc1.ID, jc2.ID, "两次签发的 jti 应互异")
}
