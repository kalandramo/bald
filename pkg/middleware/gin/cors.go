package gin

import (
	"fmt"
	"net/http"
	"strings"

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
	// 对应契约字段 `exposed_headers`——契约段需要完整落地。
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

// Validate 校验跨域配置的**规范性约束**。
//
// 目前只拦一条：`AllowOrigin == "*"` 与 `AllowCredentials == true` 同时出现。
// 这是 W3C Fetch 规范的硬禁止（带凭证的请求，Allow-Origin 不得为通配符），
// 浏览器会直接拒绝该响应——即服务端「看起来配好了」、实际全部 CORS 失败，
// 是典型的静默失效。中间件本身无法替调用方改写语义（改写 "*" 为具体来源需要
// 请求上下文），故在装配期 fail-fast 让问题在启动时暴露。
func (c *CORSConfig) Validate() error {
	if c.AllowOrigin == "*" && c.AllowCredentials {
		return fmt.Errorf("gin: cors: allowed_origins [\"*\"] with allow_credentials=true is rejected by browsers " +
			"(fetch spec forbids wildcard origin with credentials); list explicit origins instead")
	}
	// AR-14：多源列表里混入 "*" 同样是浏览器拒绝的形态——fetch 规范只接受
	// 单个源或 "*"，通配符与具体源互斥。
	origins := splitOrigins(c.AllowOrigin)
	if len(origins) > 1 {
		for _, o := range origins {
			if o == "*" {
				return fmt.Errorf("gin: cors: allowed_origins mixes \"*\" with explicit origins " +
					"(fetch spec accepts a single origin or \"*\", never both); pick one form")
			}
		}
	}
	return nil
}

// splitOrigins 把 AllowOrigin 拆成源列表（逗号分隔、去空白、去空项）。
// 单值（"*" 或具体源）返回单元素列表。
func splitOrigins(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// CORS 是 gin 跨域中间件。OPTIONS 预检直接返回 204，普通请求写入响应头。
//
// 多源语义（AR-14）：AllowOrigin 含多个源时按请求 Origin 匹配白名单后回显
// **单个**源（fetch 规范只接受单值或 "*"——把多源拼进一个响应头会被浏览器
// 整体拒绝，配置静默失效），并始终写 `Vary: Origin`（响应随 Origin 变化，
// 缓存不得跨源复用）。单值语义不变。
func CORS(config *CORSConfig) gin.HandlerFunc {
	allowed := splitOrigins(config.AllowOrigin)
	multi := len(allowed) > 1
	return func(c *gin.Context) {
		origin := config.AllowOrigin
		if multi {
			reqOrigin := c.GetHeader("Origin")
			matched := false
			for _, o := range allowed {
				if o == reqOrigin {
					matched = true
					origin = o
					break
				}
			}
			if !matched {
				origin = ""
			}
			c.Header("Vary", "Origin")
		}
		if origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
		}
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
