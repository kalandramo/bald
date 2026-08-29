// Package authn 定义 bald 的认证抽象（零引擎耦合）。
//
// 设计原则（见 docs/devel/zh-CN/架构演进路线.md §0.5 / P7）：
//   - 核心只定义 Authenticator 接口与 AuthClaims 结构，不 import 任何具体验签库
//     （jwt / oidc / casbin / JWKS 等外置为桥接子模块）。
//   - AuthClaims 为强类型结构（对齐 go-lulu plugins/security/authn/claims.go），
//     而非弱类型 map，保证 proto-first / 类型真相源。
//   - 认证成功后把 Claims 注入 context，下游（多租户、授权、审计）从 context 读取。
package authn

import (
	"context"
	"time"

	"github.com/kalandramo/bald/pkg/contextx"
)

// AuthClaims 是认证通过的统一身份声明。
//
// 字段对齐 OIDC 标准声明 + 多租户/权限扩展；具体 IdP（自建 JWT、企业 OIDC、
// API Key）在转换时填充对应字段。Subject 必填，其余可选。
type AuthClaims struct {
	// Subject 主体标识（用户/服务账号唯一 ID），必填。
	Subject string
	// TenantID 租户标识；非空时经 pkg/store 多租户机制自动隔离数据。
	TenantID string
	// Name 展示名（可选）。
	Name string
	// Scopes 授权范围列表（细粒度能力，如 "user:read"）。
	Scopes []string
	// Roles 角色列表（粗粒度，如 "admin" "member"）。
	Roles []string
	// ExpiresAt 过期时间；零值表示不过期。
	ExpiresAt time.Time
	// Issuer 签发者（可选，便于审计/路由）。
	Issuer string
}

// Authenticator 是认证器接口。具体实现（JWT / OIDC / API Key / Basic）外置为
// 桥接子模块，通过 pkg/registry.RegisterAuthenticator 注册，业务 import _ 即插即用。
type Authenticator interface {
	// Authenticate 从请求 context（含传输层元数据，如 gRPC metadata / gin header）中
	// 抽取凭证并校验，返回 AuthClaims。失败返回 error（由拦截器映射为 401）。
	Authenticate(ctx context.Context) (*AuthClaims, error)

	// AuthenticateToken 直接校验一个 token 字符串（用于非 HTTP/gRPC 场景或测试）。
	AuthenticateToken(token string) (*AuthClaims, error)
}

// AuthClaimsFromContext 读取已注入的 Claims，缺失返回 nil。
func AuthClaimsFromContext(ctx context.Context) *AuthClaims {
	if c, ok := ctx.Value(ctxKeyClaims{}).(*AuthClaims); ok {
		return c
	}
	return nil
}

// ContextWithAuthClaims 将 Claims 注入 context，并同步写入 contextx 的租户/用户键，
// 供 pkg/store 多租户与业务 handler 读取。
func ContextWithAuthClaims(ctx context.Context, c *AuthClaims) context.Context {
	ctx = context.WithValue(ctx, ctxKeyClaims{}, c)
	if c != nil {
		ctx = contextx.WithUserID(ctx, c.Subject)
		if c.Name != "" {
			ctx = contextx.WithUsername(ctx, c.Name)
		}
		if c.TenantID != "" {
			ctx = contextx.WithTenantID(ctx, c.TenantID)
		}
	}
	return ctx
}

// HasScope 判断 Claims 是否含某 scope（精确匹配）。
func (c *AuthClaims) HasScope(scope string) bool {
	for _, s := range c.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// HasRole 判断 Claims 是否含某 role。
func (c *AuthClaims) HasRole(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Expired 判断 Claims 是否已过期（ExpiresAt 零值视为永不过期）。
func (c *AuthClaims) Expired() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt)
}

type ctxKeyClaims struct{}

// TokenExtractor 从请求 context 抽取原始 token 字符串。具体形态（Bearer header、
// gRPC metadata、cookie）由桥接层（gin/grpc 中间件）实现，核心 Authenticator 不感知
// 传输细节。中间件在调用 Authenticate 前把抽取到的 token 经 ContextWithToken 存入 ctx。
type TokenExtractor interface {
	Extract(ctx context.Context) (string, error)
}

// ContextWithToken 把原始 token 存入 context，供 Authenticator.Authenticate 读取。
func ContextWithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, ctxKeyToken{}, token)
}

// TokenFromContext 读取已存入的 token，缺失返回空串。
func TokenFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyToken{}).(string); ok {
		return v
	}
	return ""
}

type ctxKeyToken struct{}
