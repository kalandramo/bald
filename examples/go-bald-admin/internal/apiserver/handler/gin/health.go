package gin

import (
	gingonic "github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/health"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
)

// RegisterHealth 把 health 业务域暴露为 HTTP 路由（薄适配层）。
// 真实校验/错误映射由 web 与 berrors 统一处理；M0 仅做最简演示。
func RegisterHealth(e *gingonic.Engine) {
	v1 := e.Group("/v1", mid.CORS(mid.DefaultCORS()))
	v1.GET("/ping", func(c *gingonic.Context) {
		c.String(200, health.Ping(c.Request.Context()))
	})
	v1.GET("/info", func(c *gingonic.Context) {
		c.JSON(200, health.Info(c.Request.Context()))
	})
}
