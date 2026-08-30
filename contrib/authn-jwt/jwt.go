// Package authnjwt 实现 bald 的 authn.Authenticator：使用 github.com/golang-jwt/jwt/v5
// 签发并校验 Bearer Token。
//
// 支持两类算法：
//   - 对称（HMAC，HS256）：签发与验签共用一把密钥，适合单体 / 演示。
//   - 非对称（RSA / ECDSA，RS256 / ES256 等）：签发方独占私钥，验证方只持公钥，
//     实现签发权与验签权解耦，可对接企业 OIDC / JWKS 生态。
//
// 通过 Option 注入密钥，核心契约 authn.Authenticator 不变。
//
// 桥接说明：authn.AuthClaims 是 bald 核心的强类型声明（不 import jwt 库），
// 本包在签发/验签时与 jwt.RegisteredClaims 互转，避免污染核心。
package authnjwt

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/kalandramo/bald/pkg/authn"
)

// defaultTTL 默认 token 有效期。
const defaultTTL = 2 * time.Hour

// Options 控制 Authenticator 的密钥与算法。
// 至少提供一组密钥（HMAC 或 非对称），否则 NewAuthenticator 返回错误。
type Options struct {
	// hmacSecret 对称密钥（与私钥公钥互斥，可二选一）。
	hmacSecret []byte

	// rsaPrivate / rsaPublic 非对称 RSA 密钥对。
	rsaPrivate *rsa.PrivateKey
	rsaPublic  *rsa.PublicKey

	// ecdsaPrivate / ecdsaPublic 非对称 ECDSA 密钥对。
	ecdsaPrivate *ecdsa.PrivateKey
	ecdsaPublic  *ecdsa.PublicKey

	// signingAlg 签发算法；缺省由注入的密钥自动推断（HMAC→HS256，RSA→RS256，ECDSA→ES256）。
	signingAlg string

	// allowedAlgs 验签白名单；缺省按注入密钥自动展开。
	allowedAlgs []string

	// issuer 签发者标识；非空时签发写入 iss 声明，验签强制匹配（防越权 issuer）。
	issuer string

	// leeway 验签时钟漂移容忍（默认 0）。
	leeway time.Duration
}

// Option 配置函数。
type Option func(*Options)

// WithHMACSecret 使用对称密钥（HS256）。
func WithHMACSecret(secret []byte) Option {
	return func(o *Options) { o.hmacSecret = secret }
}

// WithRSAKeys 注入 RSA 私钥（签发）与公钥（验签）。
// 仅验签方可以只传 priv=nil。
func WithRSAKeys(priv *rsa.PrivateKey, pub *rsa.PublicKey) Option {
	return func(o *Options) {
		o.rsaPrivate = priv
		o.rsaPublic = pub
	}
}

// WithECDSAKeys 注入 ECDSA 私钥（签发）与公钥（验签）。
// 仅验签方可以只传 priv=nil。
func WithECDSAKeys(priv *ecdsa.PrivateKey, pub *ecdsa.PublicKey) Option {
	return func(o *Options) {
		o.ecdsaPrivate = priv
		o.ecdsaPublic = pub
	}
}

// WithSigningAlg 显式指定签发算法（覆盖自动推断）。
func WithSigningAlg(alg string) Option {
	return func(o *Options) { o.signingAlg = alg }
}

// WithAllowedAlgs 显式指定验签白名单（覆盖自动推断）。
func WithAllowedAlgs(algs ...string) Option {
	return func(o *Options) { o.allowedAlgs = algs }
}

// WithIssuer 设置签发者标识；非空时签发写入 iss 声明，验签强制匹配。
func WithIssuer(issuer string) Option {
	return func(o *Options) { o.issuer = issuer }
}

// WithLeeway 设置验签时钟漂移容忍（默认 0）。
func WithLeeway(d time.Duration) Option {
	return func(o *Options) { o.leeway = d }
}

// Signer 签发能力接口（auth biz 依赖它，而非裸密钥）。
// 由 Authenticator 实现，单测可注入伪实现。
type Signer interface {
	IssueToken(claims authn.AuthClaims, ttl time.Duration) (string, error)
}

// Authenticator 基于 golang-jwt 的认证器，实现 authn.Authenticator 与 Signer。
type Authenticator struct {
	opts Options
	// 缓存的解析器（含算法白名单）。
	parser *jwt.Parser
	// 缓存的签发方法。
	signMethod jwt.SigningMethod
	// 缓存的签发密钥（非对称为私钥，对端只给公钥时为空）。
	signKey any
}

// NewAuthenticator 构造认证器。
// 必填：一组密钥（HMAC 或非对称）。算法按注入自动推断，可用 Option 覆盖。
// 返回具体类型 *Authenticator（同时实现 authn.Authenticator 与 Signer）；
// 拦截器注入用 authn.Authenticator，业务签发用 Signer。
func NewAuthenticator(opts ...Option) *Authenticator {
	o := Options{}
	for _, opt := range opts {
		opt(&o)
	}

	a := &Authenticator{opts: o}

	// 推断算法与验签白名单。
	signAlg := o.signingAlg
	allowed := o.allowedAlgs
	switch {
	case o.hmacSecret != nil:
		if signAlg == "" {
			signAlg = "HS256"
		}
		a.signMethod = jwt.GetSigningMethod(signAlg)
		a.signKey = o.hmacSecret
		if len(allowed) == 0 {
			allowed = []string{signAlg}
		}
	case o.rsaPrivate != nil || o.rsaPublic != nil:
		if signAlg == "" {
			signAlg = "RS256"
		}
		a.signMethod = jwt.GetSigningMethod(signAlg)
		if o.rsaPrivate != nil {
			a.signKey = o.rsaPrivate
		}
		if len(allowed) == 0 {
			allowed = []string{"RS256", "RS384", "RS512"}
		}
	case o.ecdsaPrivate != nil || o.ecdsaPublic != nil:
		if signAlg == "" {
			signAlg = "ES256"
		}
		a.signMethod = jwt.GetSigningMethod(signAlg)
		if o.ecdsaPrivate != nil {
			a.signKey = o.ecdsaPrivate
		}
		if len(allowed) == 0 {
			allowed = []string{"ES256", "ES384", "ES512"}
		}
	default:
		// 无密钥无法工作；返回零值认证器（Parse 必失败），由调用方确保注入。
		a.parser = jwt.NewParser(jwt.WithValidMethods([]string{}))
		return a
	}

	a.parser = jwt.NewParser(jwt.WithValidMethods(allowed), jwt.WithLeeway(o.leeway))
	return a
}

// keyfunc 根据 token 头 alg 选择对应公钥，防止 alg=none / 密钥错位混淆。
func (a *Authenticator) keyfunc(token *jwt.Token) (any, error) {
	alg := token.Method.Alg()
	switch {
	case a.opts.hmacSecret != nil && strings.HasPrefix(alg, "HS"):
		return a.opts.hmacSecret, nil
	case a.opts.rsaPublic != nil && strings.HasPrefix(alg, "RS"):
		return a.opts.rsaPublic, nil
	case a.opts.ecdsaPublic != nil && strings.HasPrefix(alg, "ES"):
		return a.opts.ecdsaPublic, nil
	default:
		return nil, fmt.Errorf("unsupported alg %q or missing public key", alg)
	}
}

// jwtClaims 是 authn.AuthClaims 与 jwt 标准注册的桥接类型，实现 jwt.Claims。
type jwtClaims struct {
	Subject  string   `json:"sub"`
	Name     string   `json:"name,omitempty"`
	TenantID string   `json:"tid,omitempty"`
	Scopes   []string `json:"scp,omitempty"`
	Roles    []string `json:"roles,omitempty"`
	Issuer   string   `json:"iss,omitempty"`
	jwt.RegisteredClaims
}

// toJWT 把核心声明映射到 jwt 桥接类型。
func toJWT(c authn.AuthClaims) jwtClaims {
	jc := jwtClaims{
		Subject:  c.Subject,
		Name:     c.Name,
		TenantID: c.TenantID,
		Scopes:   c.Scopes,
		Roles:    c.Roles,
		Issuer:   c.Issuer,
	}
	if !c.ExpiresAt.IsZero() {
		jc.ExpiresAt = jwt.NewNumericDate(c.ExpiresAt)
	}
	return jc
}

// fromJWT 把 jwt 桥接类型映射回核心声明。
func fromJWT(jc jwtClaims) *authn.AuthClaims {
	c := &authn.AuthClaims{
		Subject:  jc.Subject,
		Name:     jc.Name,
		TenantID: jc.TenantID,
		Scopes:   jc.Scopes,
		Roles:    jc.Roles,
		Issuer:   jc.Issuer,
	}
	if jc.ExpiresAt != nil {
		c.ExpiresAt = jc.ExpiresAt.Time
	}
	return c
}

// Parse 校验 token 字符串，成功返回核心声明。
// 这里 Parse 同时实现 authn.Authenticator 的 AuthenticateToken 约定之外的
// 便捷入口（与 AuthenticateToken 等价，便于直接调用）。
func (a *Authenticator) Parse(tokenStr string) (*authn.AuthClaims, error) {
	if tokenStr == "" {
		return nil, errors.New("empty token")
	}

	tok, err := a.parser.ParseWithClaims(tokenStr, &jwtClaims{}, a.keyfunc)
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, errors.New("invalid token")
	}

	jc, ok := tok.Claims.(*jwtClaims)
	if !ok {
		return nil, errors.New("unexpected claims type")
	}
	if a.opts.issuer != "" && jc.Issuer != a.opts.issuer {
		return nil, fmt.Errorf("unexpected issuer %q", jc.Issuer)
	}
	return fromJWT(*jc), nil
}

// AuthenticateToken 实现 authn.Authenticator：直接校验 token 字符串。
func (a *Authenticator) AuthenticateToken(token string) (*authn.AuthClaims, error) {
	return a.Parse(token)
}

// Authenticate 实现 authn.Authenticator：从 ctx 抽取 token 并校验。
// token 由传输层中间件经 authn.ContextWithToken 预存入 ctx。
func (a *Authenticator) Authenticate(ctx context.Context) (*authn.AuthClaims, error) {
	tok := authn.TokenFromContext(ctx)
	if tok == "" {
		return nil, errors.New("missing token in context")
	}
	return a.Parse(tok)
}

// IssueToken 签发 JWT。
// 签名密钥来自构造期注入（HMAC 对称密钥 / RSA / ECDSA 私钥）。
// 仅验签方（未注入私钥）调用会返回错误。
func (a *Authenticator) IssueToken(claims authn.AuthClaims, ttl time.Duration) (string, error) {
	if a.signKey == nil {
		return "", errors.New("authenticator has no signing key (verify-only)")
	}
	if ttl == 0 {
		ttl = defaultTTL
	}

	jc := toJWT(claims)
	if a.opts.issuer != "" {
		jc.Issuer = a.opts.issuer
	}
	now := time.Now()
	jc.IssuedAt = jwt.NewNumericDate(now)
	jc.NotBefore = jwt.NewNumericDate(now)
	// 仅当调用方未显式给定过期时间时，才用 ttl 兜底；
	// 尊重 claims.ExpiresAt（toJWT 已映射），避免无脑覆盖业务自定义有效期。
	if jc.ExpiresAt == nil {
		jc.ExpiresAt = jwt.NewNumericDate(now.Add(ttl))
	}

	tok := jwt.NewWithClaims(a.signMethod, jc)
	signed, err := tok.SignedString(a.signKey)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// ---- 兼容层：保留旧 HMAC 直接调用形态（避免范例之外调用方一次性改写）----

// IssueToken 包级便捷函数：用对称密钥签发 HS256 token。
// 仅供未切换到 Option 构造的旧调用方临时使用，新代码请用 NewAuthenticator(...).IssueToken。
func IssueToken(secret []byte, claims authn.AuthClaims, ttl time.Duration) (string, error) {
	return NewAuthenticator(WithHMACSecret(secret)).IssueToken(claims, ttl)
}

// ---- 演示 / 测试辅助：生成 RSA 与 ECDSA 密钥对 ----

// GenerateRSA 生成 bits 位 RSA 密钥对（默认 2048）。
func GenerateRSA(bits int) (*rsa.PrivateKey, error) {
	if bits <= 0 {
		bits = 2048
	}
	return rsa.GenerateKey(rand.Reader, bits)
}

// GenerateECDSA 生成指定曲线 ECDSA 密钥对（默认 P256）。
func GenerateECDSA(curve elliptic.Curve) (*ecdsa.PrivateKey, error) {
	if curve == nil {
		curve = elliptic.P256()
	}
	return ecdsa.GenerateKey(curve, rand.Reader)
}
