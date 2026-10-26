// Package apiserver 承载示例的 HTTP 面（相当于生成物的 internal/apiserver
// 种子包：业务 handler 生成一次、此后归用户维护，域路由经 appkit.Domain.Routes
// 挂到同一 handler）。
//
// 强绑定 gin：web.HandleAllRequest / HandleJSONRequest / HandleUriRequest 完成
// 绑定→校验→响应，错误统一由 web 按 pkg/berrors 映射 HTTP 状态码（被 %w 包裹
// 的错误也会正确拆链映射，不会误落 500）。
package apiserver

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/berrors"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"github.com/kalandramo/bald/transport/web"
)

// NewRouter 组织业务 HTTP 路由（gin 引擎 + 中间件链 + 示例路由组）。
//
// 中间件链是安全策略，归代码：Recovery/RequestID/Logging 在根，CORS 只叠在
// 版本组上。仅在默认构建（无 grpcgw tag）生效——grpcgw 构建下网关转码面占用
// 同一 server.http 端口，同端口单面语义由契约 server.http.driver 选择。
func NewRouter() *gin.Engine {
	router := gin.New()
	router.Use(mid.Recovery(), mid.RequestIDMiddleware(), mid.Logging())
	exampleRoutes(router)
	return router
}

// exampleRoutes 演示 bald 的 HTTP 路由约定：路由注册直接由业务完成（使用 gin 引擎）。
func exampleRoutes(e *gin.Engine) {
	// 版本组：在根中间件（Recovery/RequestID/Logging）之后叠加 CORS 子链。
	v1 := e.Group("/v1", mid.CORS(mid.DefaultCORS()))

	// 健康检查：直接回字符串，不经过结构化响应。
	v1.GET("/ping", func(c *gin.Context) {
		_, _ = c.Writer.Write([]byte("pong\n"))
	})

	// 注：POST /v1/greet 已随 greet 域迁入 greetv1 包（Domain.Routes 自持
	// 注册，行为逐行等价）——装配层不再出现域的路由知识。

	// URI 通配符示例：/v1/users/:id 经 gin 原生 ShouldBindUri 绑定到 req.ID
	// （需 uri:"id" tag，与路径变量名一致）。
	type userReq struct {
		ID string `json:"id" uri:"id"`
	}
	v1.GET("/users/:id", func(c *gin.Context) {
		web.HandleUriRequest[userReq, map[string]string](c,
			func(_ context.Context, u *userReq) (map[string]string, error) {
				return map[string]string{"id": u.ID}, nil
			})
	})

	// 结构化示例②：多源绑定（URI > Query > JSON 后者覆盖）+ 校验器。
	// GET /v1/articles/:id?lang=zh   body: {"title":"hello"}
	type articleReq struct {
		ID    string `json:"id" uri:"id"` // 来自 URI 路径变量
		Lang  string `json:"lang"`        // 来自 Query（?lang=zh）
		Title string `json:"title"`       // 来自 JSON body
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
		// 注意：绑定阶段已结束，默认值应在 Defaulter.Default() 中填充
		// （见下方 articleReq 的 Default 方法），这里只做校验。
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
