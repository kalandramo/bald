package apiserver

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/pkg/middleware/bundle"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"github.com/kalandramo/bald/transport/web"
)

// NewRouter 按契约段组织业务 HTTP 路由（appkit.WithHTTPContract 的钩子）。
//
// 求值点在契约装载之后：入参就是 server.http.middleware 段，Recovery/RequestID/
// Logging/CORS/限流/超时的参数由 bundle.FromMiddleware 翻译成链——配置文件里写
// 的限流真的会挡请求。域路由经 appkit.Domain.Routes 挂到同一引擎上。
//
// 横切链是安全策略，归应用自持：认证姿态在这一行显式宣布（契约里没有 authn 段，
// 框架不代替业务决定「本服务公开」）。
func NewRouter(mw *bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
	opts, closeChain, err := bundle.FromMiddleware(mw)
	if err != nil {
		return nil, nil, err
	}
	router := gin.New()
	router.Use(bundle.New(append([]bundle.Option{
		bundle.Public(), // 演示服务有意公开；接入认证时改为 bundle.Authn(<authenticator>)
	}, opts...)...).Gin()...)
	exampleRoutes(router)
	return router, closeChain, nil
}

func exampleRoutes(e *gin.Engine) {
	// 组级 CORS 是业务按分组的自持选择（契约的 middleware.cors 段配的是全局链）；
	// 本演示不开全局 cors 段，避免两层各写一次同源响应头。
	v1 := e.Group("/v1", mid.CORS(mid.DefaultCORS()))

	v1.GET("/ping", func(c *gin.Context) {
		_, _ = c.Writer.Write([]byte("pong\n"))
	})

	type userReq struct {
		ID string `json:"id" uri:"id"`
	}
	v1.GET("/users/:id", func(c *gin.Context) {
		web.HandleUriRequest[userReq, map[string]string](c,
			func(_ context.Context, u *userReq) (map[string]string, error) {
				return map[string]string{"id": u.ID}, nil
			})
	})

	// 多源绑定（URI > Query > JSON 后者覆盖）+ 校验器。
	type articleReq struct {
		ID    string `json:"id" uri:"id"`
		Lang  string `json:"lang"`
		Title string `json:"title"`
	}
	type articleResp struct {
		ID    string `json:"id"`
		Lang  string `json:"lang"`
		Title string `json:"title"`
	}
	validateArticle := func(_ context.Context, a *articleReq) error {
		if a.Title == "" {
			return berrors.BadRequest("EMPTY_TITLE").WithMessage("title 不能为空")
		}
		if a.Lang == "" {
			a.Lang = "en"
		}
		return nil
	}
	v1.POST("/articles/:id", func(c *gin.Context) {
		web.HandleAllRequest[articleReq, articleResp](c,
			func(_ context.Context, a *articleReq) (articleResp, error) {
				return articleResp{ID: a.ID, Lang: a.Lang, Title: a.Title}, nil
			},
			validateArticle)
	})
}
