// Command bald-gin 演示用原生 gin 作为 HTTP 引擎，路由直接用 gin 编写，
// 「绑定/校验/响应」复用强绑定 gin 的 pkg/web 流水线。
//
// 关键点：web 包直接消费 *gin.Context，因此只支持 gin 与 grpc-gateway
// （后者经 grpc-gateway 的 HTTP transcoding 落到标准库 http）。这与 onexstack
// 的 pkg/core 思路一致，业务 handler 与传输层解耦（参考 miniblog handler 分层）。
//
// 运行：
//
//	go run ./_example/bald-gin
//
//	# 验证（PowerShell 见 example/bald 说明，用 --% 关闭解析）：
//	curl -i http://127.0.0.1:8080/v1/ping
//	curl -i -XPOST http://127.0.0.1:8080/v1/greet -H "Content-Type: application/json" -d '{"name":"bald"}'
//	curl -i http://127.0.0.1:8080/v1/users/42
package main

import (
	"context"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/pflag"

	baldlog "github.com/kalandramo/bald/pkg/log"
	"github.com/kalandramo/bald/pkg/appkit"
	baldoptions "github.com/kalandramo/bald/pkg/options"
	"github.com/kalandramo/bald/pkg/registry/inmemory"
	"github.com/kalandramo/bald/pkg/server"
	"github.com/kalandramo/bald/pkg/web"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
)

func main() {
	httpOpts := baldoptions.NewSecureServingOptions()
	httpOpts.Addr = ":8080"
	grpcOpts := baldoptions.NewGRPCOptions()

	// 日志系统接入（进程入口 bootstrap 全局 Logger）。
	logOpts := baldlog.NewOptions()
	logOpts.AddFlags(pflag.CommandLine)
	baldlog.SetLogger(baldlog.NewSlogLogger(logOpts))
	logger := baldlog.GetLogger()

	httpOpts.AddFlags(pflag.CommandLine, baldoptions.Join("bald-gin", "http"))
	grpcOpts.AddFlags(pflag.CommandLine, baldoptions.Join("bald-gin", "grpc"))

	ready := func(ctx context.Context) error { return nil }

	// 用原生 gin 构造路由，「绑定/校验/响应」全部走强绑定 gin 的 pkg/web。
	// 路径参数由 gin 原生 ShouldBindUri 处理，无需额外桥接中间件。
	engine := gin.New()
	engine.Use(gin.Recovery())

	engine.GET("/v1/ping", func(c *gin.Context) {
		_, _ = c.Writer.Write([]byte("pong"))
	})

	type greetReq struct {
		Name string `json:"name"`
	}
	type greetResp struct {
		Greet string `json:"greet"`
	}
	engine.POST("/v1/greet", func(c *gin.Context) {
		web.HandleJSONRequest[greetReq, greetResp](c,
			func(_ context.Context, g *greetReq) (greetResp, error) {
				return greetResp{Greet: "hello, " + g.Name}, nil
			})
	})

	type userReq struct {
		ID string `json:"id" uri:"id"`
	}
	engine.GET("/v1/users/:id", func(c *gin.Context) {
		web.HandleUriRequest[userReq, map[string]string](c,
			func(_ context.Context, u *userReq) (map[string]string, error) {
				return map[string]string{"id": u.ID}, nil
			})
	})

	// 直接把 gin.Engine 作为 http.Handler 交给 bald 服务器层。
	httpSrv := server.NewHTTPServer(httpOpts, engine, ready)

	app := appkit.New(
		appkit.Name("bald-gin"),
		appkit.Version("v0.1.0"),
		appkit.StopTimeout(15*time.Second),
		appkit.Registrar(inmemory.New()),
		appkit.Servers(httpSrv),
		appkit.AfterStart(func(ctx context.Context) error {
			logger.Info(ctx, "bald-gin started", "http", httpSrv.Endpoint())
			return nil
		}),
	)

	if err := app.Run(context.Background()); err != nil {
		logger.Error(context.Background(), "bald-gin exited", "error", err)
		os.Exit(1)
	}
}
