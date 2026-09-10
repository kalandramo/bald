// Package gin 提供 go-bald-admin 的 HTTP handler 装配（协议层）。
//
// 本文件演示 M1 认证授权范式：用 bald 的 gin 中间件
// （middleware/gin.AuthnMiddleware / AuthzMiddleware）保护路由。业务经由 biz 层
// 调用，handler 仅做协议转换与中间件接线，不写入认证/授权策略。
package gin

import (
	"errors"
	"net/http"

	gingonic "github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	web "github.com/kalandramo/bald/transport/web"

	authbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auth"
	secretbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/secret"
)

// RegisterAuth 挂载认证与受限资源路由。
//   - POST /v1/login            公开
//   - GET  /v1/auth/whoami      需认证（任意已登录用户）→ 权限点 "auth:get"
//   - GET  /v1/secret/:id       需认证 + "secret:get"（viewer/admin）
//   - DEL  /v1/secret/:id       需认证 + "secret:delete"（仅 admin）
func RegisterAuth(
	e *gingonic.Engine,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	biz *authbiz.Biz,
	secretBiz *secretbiz.SecretBiz,
) {
	e.POST("/v1/login", func(c *gingonic.Context) {
		var cred authbiz.Credential
		if err := c.ShouldBindJSON(&cred); err != nil {
			bindErr(c, err)
			return
		}
		// T6 登录审计：客户端信息 handler 层提取（biz 保持协议无关）。
		cred.ClientIP = c.ClientIP()
		cred.UserAgent = c.Request.UserAgent()
		pair, err := biz.Login(c.Request.Context(), cred)
		if err != nil {
			// 仅凭据错误归 401（reason=BAD_CREDENTIAL 供前端程序化识别）；查询/签发等
			// 内部错误归 500——此前一刀切 401 会把 DB 故障伪装成"密码错误"。
			if errors.Is(err, authbiz.ErrBadCredential) {
				web.ErrorResponse(c, berrors.Unauthenticated("BAD_CREDENTIAL").WithMessage("%s", err))
				return
			}
			writeBizErr(c, err)
			return
		}
		c.JSON(http.StatusOK, pair)
	})

	// 需认证分组。
	authed := e.Group("/v1")
	authed.Use(mid.AuthnMiddleware(authenticator))
	// Authz 由路由级中间件按 (资源名, 动作) 判定；归一化由核心拦截器完成（P9 反哺）：
	// HTTP 侧经 DefaultHTTPObject/DefaultHTTPAction 把 path/method 翻译为与 gRPC 同源的权限点。
	authzMW := mid.AuthzMiddleware(authorizer,
		mid.WithObjectResolver(authz.DefaultHTTPObject),
		mid.WithActionResolver(authz.DefaultHTTPAction),
	)

	authed.GET("/auth/whoami", authzMW, func(c *gingonic.Context) {
		info, err := biz.WhoAmI(c.Request.Context())
		if err != nil {
			writeBizErr(c, err) // NotFound（认证后用户被删/停用）→404、内部→500
			return
		}
		c.JSON(http.StatusOK, info)
	})

	authed.GET("/secret/:id", authzMW, func(c *gingonic.Context) {
		// M6.3：经真实 DAL 读取，自动受 ctx 租户隔离约束；越权跨租户检索被 store 拦为 404。
		item, err := secretBiz.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			writeBizErr(c, err) // NotFound→404（含跨租户）、缓存/内部→500
			return
		}
		c.JSON(http.StatusOK, item)
	})

	authed.DELETE("/secret/:id", authzMW, func(c *gingonic.Context) {
		// M6 CR 修复：真实删除，经 biz 落 store，自动受 ctx 租户隔离约束；
		// 跨租户/不存在返回 404（与 Get 一致）。
		ok, err := secretBiz.Delete(c.Request.Context(), c.Param("id"))
		if err != nil {
			writeBizErr(c, err) // NotFound→404、内部→500
			return
		}
		if !ok {
			web.ErrorResponse(c, berrors.NotFound("NOT_FOUND").WithMessage("secret not found"))
			return
		}
		c.JSON(http.StatusOK, gingonic.H{"deleted": c.Param("id")})
	})
}
