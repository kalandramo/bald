package authnjwt

import (
	"context"
	"reflect"
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

// ---------------------------------------------------------------------------
// 字段集自动同步守护（2026-09-24）
// ---------------------------------------------------------------------------

// claimFieldSyncExempt 是**允许不同步**的字段白名单（键为 AuthClaims 字段名）。
//
// 为什么需要豁免：jwtClaims 并非 AuthClaims 的逐字段镜像——时间字段走
// jwt.RegisteredClaims 的标准注册声明（形态不同）：
//
//	AuthClaims.ExpiresAt  time.Time        → jwtClaims 内嵌
//	                                          RegisteredClaims.ExpiresAt *NumericDate
//
// 这类差异是**刻意的桥接设计**（复用 jwt 库的标准声明类型），不是遗漏。
// 白名单是显式的——新增豁免必须在此登记并写明理由，防止「顺手加白名单」掩盖
// 真实遗漏。
var claimFieldSyncExempt = map[string]string{
	"ExpiresAt": "走 jwt.RegisteredClaims.ExpiresAt（*NumericDate），形态与 time.Time 不同",
}

// TestJWTClaims_FieldSetSync 用**反射**比对 AuthClaims 与 jwtClaims 的字段集，
// 核心侧新增字段而未同步到桥接层时**自动失败**。
//
// 存在理由（2026-09-24）：本桥接层曾漏同步 Platform 字段（v0.13.0 新增），
// 导致以 Platform=true 签发的 token 认证后恒为 false——「设置了却不生效」，
// 极难排查。此前靠类型注释约定「必须同步」，无机制强制；本测试把它变成硬门禁。
//
// 规则：
//  1. AuthClaims 的每个字段，要么在 jwtClaims 中有**同名字段**（含内嵌
//     RegisteredClaims 的提升字段），要么在 claimFieldSyncExempt 中登记。
//  2. jwtClaims 自己的字段（非提升）必须能在 AuthClaims 中找到同名者——
//     防止桥接层出现核心侧没有的「幽灵字段」。
//
// 注：Go 的反射无法直接遍历内嵌结构的提升字段，故此处显式展开
// jwt.RegisteredClaims 的字段名参与比对。
func TestJWTClaims_FieldSetSync(t *testing.T) {
	coreFields := map[string]bool{}
	ct := reflect.TypeOf(authn.AuthClaims{})
	for i := 0; i < ct.NumField(); i++ {
		coreFields[ct.Field(i).Name] = true
	}

	// jwtClaims 的字段集 = 自身字段 + 内嵌 RegisteredClaims 的字段（提升）。
	jwtFields := map[string]bool{}
	jt := reflect.TypeOf(jwtClaims{})
	for i := 0; i < jt.NumField(); i++ {
		f := jt.Field(i)
		if f.Anonymous {
			// 内嵌：展开其字段（提升语义）。
			for j := 0; j < f.Type.NumField(); j++ {
				jwtFields[f.Type.Field(j).Name] = true
			}
			continue
		}
		jwtFields[f.Name] = true
	}

	// 规则 1：核心字段必须被映射（同名或显式豁免）。
	for name := range coreFields {
		if jwtFields[name] {
			continue
		}
		if reason, ok := claimFieldSyncExempt[name]; ok {
			t.Logf("豁免字段 %s：%s", name, reason)
			continue
		}
		t.Errorf("AuthClaims.%s 未同步到 jwtClaims——"+
			"新增核心字段必须在此映射（toJWT/fromJWT），"+
			"否则签发时静默丢弃、解析后无从恢复。"+
			"若确属刻意不映射，请在 claimFieldSyncExempt 登记理由", name)
	}

	// 规则 2：桥接层字段必须在核心侧有对应（防幽灵字段）。
	// 豁免：jwtClaims 顶层重复声明了 sub/iss（shadow RegisteredClaims 的同名
	// 字段）以控制 json tag，属桥接实现细节。
	shadowAllowed := map[string]string{
		"Subject": "shadow RegisteredClaims.Subject（自定义 json tag sub）",
		"Issuer":  "shadow RegisteredClaims.Issuer（自定义 json tag iss）",
	}
	for name := range jwtFields {
		if coreFields[name] {
			continue
		}
		// 标准注册声明里核心侧不映射的（aud/nbf/iat/jti）是 jwt 库机制所需。
		if jwtStandardOnly[name] {
			continue
		}
		if _, ok := shadowAllowed[name]; ok {
			continue
		}
		t.Errorf("jwtClaims.%s 在 AuthClaims 中无对应字段——"+
			"桥接层不应出现核心侧不存在的幽灵字段", name)
	}
}

// jwtStandardOnly 是 jwt.RegisteredClaims 中核心侧**刻意不映射**的标准声明
// （由 jwt 库自身机制维护，非业务声明）。
var jwtStandardOnly = map[string]bool{
	"Audience":  true, // aud：本框架不用受众
	"NotBefore": true, // nbf：由库按签发时刻自动填
	"IssuedAt":  true, // iat：同上
	"ID":        true, // jti：toJWT 内部生成（D6 修复），非业务字段
}
