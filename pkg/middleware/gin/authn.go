package gin

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	berrors "github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/httperr"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/transport/web"
)

// AuthnOption 配置 AuthnMiddleware。
type AuthnOption func(*authnOptions)

type authnOptions struct {
	// auditor 指定认证失败审计事件的接收后端；nil 时用 audit.GetAuditor() 全局实例。
	// 由 bundle 注入（与请求审计同一后端），保证 authn abort 路径与业务审计同源。
	auditor audit.Auditor
}

// AuthnWithAuditor 指定认证失败审计后端；缺省用全局 audit.GetAuditor()。
func AuthnWithAuditor(a audit.Auditor) AuthnOption {
	return func(o *authnOptions) { o.auditor = a }
}

// AuthnMiddleware 是 gin 认证中间件：从 Authorization 头抽取 Bearer token 存入 ctx，
// 交由注入的 Authenticator 校验并把 AuthClaims 写入 ctx（含租户/用户键，供下游
// pkg/store 多租户自动隔离）。
//
// 认证失败（缺 token / 校验失败）时发送一条 ResultDeny 审计事件后 abort——审计
// 中间件注册在 Authn 内侧，abort 后不再执行，认证失败必须由本层显式留痕（D3）。
//
// authenticator 为 nil 时中间件退化为空操作（不认证，等同于公开服务）。
func AuthnMiddleware(authenticator authn.Authenticator, opts ...AuthnOption) gin.HandlerFunc {
	cfg := &authnOptions{}
	for _, opt := range opts {
		opt(cfg)
	}
	auditor := cfg.auditor
	if auditor == nil {
		auditor = audit.GetAuditor()
	}
	return func(c *gin.Context) {
		if authenticator == nil {
			c.Next()
			return
		}
		token, err := bearerFromHeader(c.GetHeader("Authorization"))
		if err != nil {
			e := berrors.Unauthenticated("MISSING_TOKEN").WithMessage("%s", err.Error())
			log.GetLogger().Error(c.Request.Context(), "authentication failed", "error", err)
			auditAuthnFailure(c, auditor, e.Reason)
			c.AbortWithStatusJSON(httperr.StatusCode(e), web.ErrorBody{
				Error: web.ErrorDetail{Code: e.Reason, Message: e.Message},
			})
			return
		}
		ctx := authn.ContextWithToken(c.Request.Context(), token)

		claims, err := authenticator.Authenticate(ctx)
		if err != nil {
			e := berrors.Unauthenticated("UNAUTHENTICATED").WithMessage("%s", err.Error())
			log.GetLogger().Error(c.Request.Context(), "authentication failed", "error", err)
			auditAuthnFailure(c, auditor, e.Reason)
			c.AbortWithStatusJSON(httperr.StatusCode(e), web.ErrorBody{
				Error: web.ErrorDetail{Code: e.Reason, Message: e.Message},
			})
			return
		}
		// 过期校验已下沉至 Authenticator.Authenticate（实现契约必须校验 ExpiresAt），
		// 中间件不再重复判断。

		ctx = authn.ContextWithAuthClaims(ctx, claims)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// auditAuthnFailure 在 authn abort 路径显式发一条 ResultDeny 审计事件（旁路语义，
// panic/错误仅记日志，绝不影响 abort 本身）。Subject 为空（认证失败无 claims）。
func auditAuthnFailure(c *gin.Context, auditor audit.Auditor, reason string) {
	recordSafely(auditor, c.Request.Context(), audit.AuditEvent{
		Object: "authn",
		Action: "authenticate",
		Result: audit.ResultDeny,
		Error:  reason,
		Meta: map[string]any{
			"reason":     reason,
			"path":       c.Request.URL.Path,
			"client_ip":  c.ClientIP(),
			"request_id": contextx.RequestIDFromContext(c.Request.Context()),
		},
	})
}

// bearerFromHeader 从 Authorization 头取 Bearer token。
func bearerFromHeader(h string) (string, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", errNoToken
	}
	t := strings.TrimPrefix(h, prefix)
	if t == "" {
		return "", errNoToken
	}
	return t, nil
}

type tokenErr struct{ s string }

func (e tokenErr) Error() string { return e.s }

var errNoToken = tokenErr{"missing or malformed Authorization bearer token"}
