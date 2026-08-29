package gin

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/authn"
	berrors "github.com/kalandramo/bald/pkg/berrors"
	"github.com/kalandramo/bald/pkg/berrors/httperr"
)

// AuthnMiddleware 是 gin 认证中间件：从 Authorization 头抽取 Bearer token 存入 ctx，
// 交由注入的 Authenticator 校验并把 AuthClaims 写入 ctx（含租户/用户键，供下游
// pkg/store 多租户自动隔离）。
//
// authenticator 为 nil 时中间件退化为空操作（不认证，等同于公开服务）。
func AuthnMiddleware(authenticator authn.Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authenticator == nil {
			c.Next()
			return
		}
		token, err := bearerFromHeader(c.GetHeader("Authorization"))
		if err != nil {
			e := berrors.Unauthenticated("MISSING_TOKEN").WithMessage("%s", err.Error())
			c.AbortWithStatusJSON(httperr.StatusCode(e), gin.H{
				"reason":  e.Reason,
				"message": e.Message,
			})
			return
		}
		ctx := authn.ContextWithToken(c.Request.Context(), token)

		claims, err := authenticator.Authenticate(ctx)
		if err != nil {
			e := berrors.Unauthenticated("UNAUTHENTICATED").WithMessage("%s", err.Error())
			c.AbortWithStatusJSON(httperr.StatusCode(e), gin.H{
				"reason":  e.Reason,
				"message": e.Message,
			})
			return
		}
		if claims.Expired() {
			e := berrors.Unauthenticated("TOKEN_EXPIRED")
			c.AbortWithStatusJSON(httperr.StatusCode(e), gin.H{
				"reason":  e.Reason,
				"message": e.Message,
			})
			return
		}

		ctx = authn.ContextWithAuthClaims(ctx, claims)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
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
