// Package auth 实现 M1 认证授权范本的业务逻辑（biz 层）。
//
// 本层不依赖 gin/grpc：业务函数以纯 Go 入参/出参呈现，由 handler 层负责协议
// 转换（HTTP/gRPC transcoding）。这是 osbuilder/bald 推荐的分层：
// handler（协议）→ biz（业务）→ store（数据）。
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	authnjwt "github.com/kalandramo/bald-authn-jwt"
	"github.com/kalandramo/bald/pkg/authn"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
	"golang.org/x/crypto/bcrypt"

	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// ErrBadCredential 凭据错误。
var ErrBadCredential = errors.New("auth: invalid username or password")

// Credential 登录凭据。
type Credential struct {
	Username string
	Password string
}

// TokenPair 登录成功后签发的令牌对。
type TokenPair struct {
	AccessToken string
	ExpiresAt   int64 // unix 秒
}

// UserInfo 当前登录用户信息（WhoAmI 返回）。
type UserInfo struct {
	Username  string
	UserID    string
	TenantID  string
	Roles     []string
	TokenType string // 如 "Bearer"
}

// Biz 认证业务。
type Biz struct {
	signer authnjwt.Signer // 私钥签发器（非对称场景只持私钥）
}

// New 构造认证 Biz。signer 来自 bootstrap（bald-authn-jwt 签发实例）。
func New(signer authnjwt.Signer) *Biz {
	return &Biz{signer: signer}
}

// Login 校验凭据（查 store）并签发 JWT。
func (b *Biz) Login(ctx context.Context, c Credential) (*TokenPair, error) {
	u, err := bootstrappkg.UserStore.Get(ctx, &store.Where{
		Filters: []*storev1.FilterCondition{store.Eq("username", c.Username)},
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrBadCredential
		}
		return nil, fmt.Errorf("auth: query user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(c.Password)); err != nil {
		return nil, ErrBadCredential
	}

	now := time.Now()
	ttl := 2 * time.Hour
	claims := authn.AuthClaims{
		Issuer:   "go-bald-admin",
		Subject:  u.ID,
		TenantID: u.TenantID,
		Roles:    u.RolesList(),
		Name:     u.Username,
	}
	// 签发由 Signer 完成（非对称：仅持私钥的签发实例，验证方只持公钥）。
	token, err := b.signer.IssueToken(claims, ttl)
	if err != nil {
		return nil, fmt.Errorf("auth: issue token: %w", err)
	}
	return &TokenPair{AccessToken: token, ExpiresAt: now.Add(ttl).Unix()}, nil
}

// WhoAmI 从已认证上下文解析当前用户（subject 由 authn 中间件注入）。
func (b *Biz) WhoAmI(ctx context.Context) (*UserInfo, error) {
	claims := authn.AuthClaimsFromContext(ctx)
	if claims == nil {
		return nil, fmt.Errorf("auth: subject not found in context")
	}
	return &UserInfo{
		Username:  claims.Name,
		UserID:    claims.Subject,
		TenantID:  claims.TenantID,
		Roles:     claims.Roles,
		TokenType: "Bearer",
	}, nil
}
