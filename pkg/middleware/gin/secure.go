package gin

import "github.com/gin-gonic/gin"

// Secure 是 gin 安全中间件，设置若干安全响应头（HSTS、防 MIME 嗅探、XSS 保护等）。
func Secure() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Next()
	}
}
