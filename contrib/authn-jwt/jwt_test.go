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

// TestJWTClaims_PlatformRoundTrip 锁定 Platform 字段的双向映射（2026-09-24）。
//
// 背景：authn.AuthClaims 新增 Platform 字段（平台级身份）后，本桥接层的
// jwtClaims **未同步**——签发时静默丢弃、解析时无从恢复，表现为「设置了
// Platform=true 但认证后恒为 false」。此缺口由 bald-admin 的 e2e 探针
// 实测暴露（whoami 回显 Platform=false 而 token 是以 true 签发的）。
//
// 本测试同时是**加字段纪律**的回归门：jwtClaims 是 AuthClaims 的全量镜像，
// 核心侧新增字段必须在此同步映射。
func TestJWTClaims_PlatformRoundTrip(t *testing.T) {
	a := NewAuthenticator(WithHMACSecret([]byte("test-secret-not-leaked")))

	// 1) Platform=true 必须往返保真。
	c := sampleClaims()
	c.Platform = true
	tok, err := a.IssueToken(c, time.Hour)
	require.NoError(t, err)
	got, err := a.AuthenticateToken(tok)
	require.NoError(t, err)
	assert.True(t, got.Platform, "Platform=true 经签发→解析后必须仍为 true")

	// 2) Platform=false（默认）不得被误置为 true——fail-closed。
	c2 := sampleClaims() // Platform 零值 false
	tok2, err := a.IssueToken(c2, time.Hour)
	require.NoError(t, err)
	got2, err := a.AuthenticateToken(tok2)
	require.NoError(t, err)
	assert.False(t, got2.Platform, "Platform 缺省必须为 false（fail-closed）")

	// 3) 平台标记经 contextx 贯通：Authenticate 后 ctx 可读出。
	ctx := authn.ContextWithToken(context.Background(), tok)
	got3, err := a.Authenticate(ctx)
	require.NoError(t, err)
	assert.True(t, got3.Platform)
}

// TestJWTClaims_AllCoreFieldsRoundTrip 是**加字段纪律**的结构化守护：
// 逐字段断言 AuthClaims 的所有非零字段都能经 JWT 往返保真。
//
// 若将来 AuthClaims 新增字段而 jwtClaims 未同步，本测试不会自动失败
// （Go 无字段级反射对比），但下方的字段清单注释提示维护者补断言。
// 当前覆盖：Subject / Name / TenantID / Platform / Scopes / Roles / Issuer。
func TestJWTClaims_AllCoreFieldsRoundTrip(t *testing.T) {
	a := NewAuthenticator(WithHMACSecret([]byte("test-secret-not-leaked")))
	c := authn.AuthClaims{
		Subject:  "u-all",
		Name:     "AllFields",
		TenantID: "t-all",
		Platform: true,
		Scopes:   []string{"a:read", "b:write"},
		Roles:    []string{"admin", "auditor"},
		Issuer:   "bald-test",
	}
	tok, err := a.IssueToken(c, time.Hour)
	require.NoError(t, err)
	got, err := a.AuthenticateToken(tok)
	require.NoError(t, err)

	assert.Equal(t, c.Subject, got.Subject)
	assert.Equal(t, c.Name, got.Name)
	assert.Equal(t, c.TenantID, got.TenantID)
	assert.Equal(t, c.Platform, got.Platform, "Platform 必须往返保真")
	assert.Equal(t, c.Scopes, got.Scopes)
	assert.Equal(t, c.Roles, got.Roles)
	assert.Equal(t, c.Issuer, got.Issuer)
}
