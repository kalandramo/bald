// Package baldjwt 是 bald 的 JWT 认证桥接子模块（独立 go.mod）。
//
// 设计约束（见 docs/devel/zh-CN/架构演进路线.md §0.5 / P7）：
//   - 实现 pkg/authn.Authenticator 接口，但本包不 import gin / grpc / casbin；
//     传输层 token 抽取由 pkg/middleware/{gin,grpc} 在调用 Authenticate 前完成，
//     通过 authn.ContextWithToken 传入（依赖倒置）。
//   - 使用 golang-jwt/jwt/v5 解析；强制校验签名算法为 HMAC（防 alg=none）。
//   - 不提供硬编码默认密钥（规避 onexstack token 包反模式），必须显式传入 secret。
package baldjwt

import (
	"context"
	"errors"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"

	"github.com/kalandramo/bald/pkg/authn"
)

// Option 配置 JWT Authenticator。
type Option func(*authenticator)

// WithLeeway 设置过期时钟偏移容忍（默认 0）。
func WithLeeway(d time.Duration) Option {
	return func(a *authenticator) { a.leeway = d }
}

// WithIssuer 校验 token 的 iss 声明（可选，为空则不校验）。
func WithIssuer(issuer string) Option {
	return func(a *authenticator) { a.issuer = issuer }
}

// NewAuthenticator 构造 JWT Authenticator。secret 为 HMAC 签名密钥，必填。
func NewAuthenticator(secret string, opts ...Option) authn.Authenticator {
	if secret == "" {
		panic("bald-authn-jwt: secret must not be empty")
	}
	a := &authenticator{secret: []byte(secret)}
	for _, o := range opts {
		o(a)
	}
	return a
}

type authenticator struct {
	secret  []byte
	leeway  time.Duration
	issuer  string
}

// customClaims 是 JWT 载荷与 AuthClaims 之间的映射载体。
type customClaims struct {
	jwt.RegisteredClaims
	TenantID string   `json:"tid,omitempty"`
	Name     string   `json:"name,omitempty"`
	Scopes   []string `json:"scp,omitempty"`
	Roles    []string `json:"roles,omitempty"`
}

func (a *authenticator) Authenticate(ctx context.Context) (*authn.AuthClaims, error) {
	token := authn.TokenFromContext(ctx)
	if token == "" {
		return nil, errors.New("missing token in context")
	}
	return a.parse(token)
}

func (a *authenticator) AuthenticateToken(token string) (*authn.AuthClaims, error) {
	return a.parse(token)
}

// parse 校验签名（强制 HMAC）并映射为 AuthClaims。
func (a *authenticator) parse(token string) (*authn.AuthClaims, error) {
	claims := &customClaims{}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"})) // 算法白名单，防 alg=none
	_, err := parser.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) {
		return a.secret, nil
	})
	if err != nil {
		return nil, err
	}
	if a.issuer != "" && claims.Issuer != a.issuer {
		return nil, errors.New("token issuer mismatch")
	}
	c := &authn.AuthClaims{
		Subject:   claims.Subject,
		TenantID:  claims.TenantID,
		Name:      claims.Name,
		Scopes:    claims.Scopes,
		Roles:     claims.Roles,
		Issuer:    claims.Issuer,
		ExpiresAt: claims.ExpiresAt.Time,
	}
	return c, nil
}

// IssueToken 签发一个 JWT（供 login 端点或测试使用）。
func IssueToken(secret string, c authn.AuthClaims, ttl time.Duration) (string, error) {
	if secret == "" {
		return "", errors.New("secret must not be empty")
	}
	now := time.Now()
	claims := &customClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   c.Subject,
			Issuer:    c.Issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		TenantID: c.TenantID,
		Name:     c.Name,
		Scopes:   c.Scopes,
		Roles:    c.Roles,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString([]byte(secret))
}
