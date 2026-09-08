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
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/store"
	"golang.org/x/crypto/bcrypt"

	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// ErrBadCredential 凭据错误。
var ErrBadCredential = errors.New("auth: invalid username or password")

// dummyHash 进程启动时预生成一次的 bcrypt 哈希：用户不存在路径做等代价比较，
// 抹平「用户存在与否」的响应时序差（防用户名枚举侧信道）——存在路径每次都跑
// bcrypt 比较，不存在路径若直接返回会快出数量级。
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcrypt.DefaultCost)

// Credential 登录凭据。ClientIP/UserAgent 由 handler 层从协议请求提取传入
// （T6 登录审计落库用；biz 层保持协议无关）。
type Credential struct {
	Username  string
	Password  string
	ClientIP  string // 客户端 IP（gin: c.ClientIP()）
	UserAgent string // 客户端 UA（gin: c.Request.UserAgent()）
}

// TokenPair 登录成功后签发的令牌对。
// json tag 必备：HTTP handler 直接 c.JSON 序列化本结构（非 proto 消息不走
// writePB），无 tag 会按 Go 导出名输出（{"AccessToken":...}），前端取
// access_token 得 undefined，后续请求携带 "Bearer undefined" 触发 JWT 解析错。
type TokenPair struct {
	AccessToken string `json:"access_token"`
	ExpiresAt   int64  `json:"expires_at"` // unix 秒
}

// UserInfo 当前登录用户信息（WhoAmI 返回）。
type UserInfo struct {
	Username  string   `json:"username"`
	UserID    string   `json:"user_id"`
	TenantID  string   `json:"tenant_id"`
	Roles     []string `json:"roles"`
	TokenType string   `json:"token_type"` // 如 "Bearer"
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
// T6 登录审计：成功/失败/内部错误均记一条 category=login 审计事件（源
// login_audit_log 语义精简：IP/UA/结果/失败原因；风险评分/MFA/设备指纹后续迭代）。
// 经全局 audit.GetAuditor()（bootstrap audit.backends 热切换同一入口），旁路不阻断。
func (b *Biz) Login(ctx context.Context, c Credential) (*TokenPair, error) {
	u, err := bootstrappkg.UserStore.Get(ctx, &store.Where{
		Filters: []*storev1.FilterCondition{store.Eq("username", c.Username)},
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(c.Password)) // 等代价比较，见 dummyHash 注释
			auditLogin(ctx, c, "", audit.ResultDeny, "invalid credentials")
			return nil, ErrBadCredential
		}
		auditLogin(ctx, c, "", audit.ResultError, err.Error())
		return nil, fmt.Errorf("auth: query user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(c.Password)); err != nil {
		auditLogin(ctx, c, u.TenantID, audit.ResultDeny, "invalid credentials")
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
		auditLogin(ctx, c, u.TenantID, audit.ResultError, err.Error())
		return nil, fmt.Errorf("auth: issue token: %w", err)
	}
	auditLogin(ctx, c, u.TenantID, audit.ResultAllow, "")
	return &TokenPair{AccessToken: token, ExpiresAt: now.Add(ttl).Unix()}, nil
}

// auditLogin 记录登录审计事件（category=login，Object/Action 用 P9 归一化
// "auth"/"login"；IP/UA 经 Meta 传递由落库后端提取为独立列）。
func auditLogin(ctx context.Context, c Credential, tenantID string, result audit.Result, errMsg string) {
	ev := audit.AuditEvent{
		Time:     time.Now(),
		Subject:  c.Username, // 登录场景以账号名为主体（用户不存在时也是有效线索）
		TenantID: tenantID,
		Object:   "auth",
		Action:   "login",
		Result:   result,
		Error:    errMsg,
		Meta: map[string]any{
			"category":   "login",
			"client_ip":  c.ClientIP,
			"user_agent": c.UserAgent,
		},
	}
	recordSafely(ctx, ev)
}

// recordSafely 旁路记录：审计失败仅降级记日志，绝不影响登录主流程。
func recordSafely(ctx context.Context, ev audit.AuditEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.GetLogger().Warn(ctx, "login audit panic recovered", "panic", r)
		}
	}()
	audit.GetAuditor().Record(ctx, ev)
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
