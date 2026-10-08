package gin

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// CORSConfig 定义跨域配置。
type CORSConfig struct {
	AllowOrigin      string
	AllowMethods     string
	AllowHeaders     string
	AllowCredentials bool
	MaxAge           int
	// ExposedHeaders 是暴露给浏览器的响应头（逗号分隔）。
	// 对应契约 fields `exposed_headers`（此前 CORSConfig 无此字段，该契约字段
	// 无处落地——补上以让契约段完整生效）。
	ExposedHeaders string
}

// DefaultCORS 返回默认跨域配置：允许任意来源、常见方法、常见请求头。
func DefaultCORS() *CORSConfig {
	return &CORSConfig{
		AllowOrigin:      "*",
		AllowMethods:     "GET, POST, PUT, DELETE, OPTIONS",
		AllowHeaders:     "Content-Type, Authorization",
		AllowCredentials: false,
		MaxAge:           86400,
	}
}

// CORS 是 gin 跨域中间件。OPTIONS 预检直接返回 204，普通请求写入响应头。
func CORS(config *CORSConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", config.AllowOrigin)
		c.Header("Access-Control-Allow-Methods", config.AllowMethods)
		c.Header("Access-Control-Allow-Headers", config.AllowHeaders)
		if config.ExposedHeaders != "" {
			c.Header("Access-Control-Expose-Headers", config.ExposedHeaders)
		}
		if config.AllowCredentials {
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		c.Header("Access-Control-Max-Age", fmt.Sprintf("%d", config.MaxAge))
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
