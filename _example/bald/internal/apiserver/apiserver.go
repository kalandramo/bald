package apiserver

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/berrors"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"github.com/kalandramo/bald/transport/web"
)

// NewRouter 组织业务 HTTP 路由。域路由经 appkit.Domain.Routes 挂到同一引擎上；
// 中间件链是安全策略，归应用自持。
func NewRouter() *gin.Engine {
	router := gin.New()
	router.Use(mid.Recovery(), mid.RequestIDMiddleware(), mid.Logging())
	exampleRoutes(router)
	return router
}

func exampleRoutes(e *gin.Engine) {
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
