package gin

import (
	"fmt"
	"io"

	"github.com/gin-gonic/gin"
)

// Recovery 是 gin 中间件，恢复 handler 中的 panic 并返回统一 JSON 错误响应，
// 而不是 gin 默认的纯文本栈。返回 500。
func Recovery() gin.HandlerFunc {
	return gin.RecoveryWithWriter(io.Discard, func(c *gin.Context, err any) {
		c.AbortWithStatusJSON(500, gin.H{
			"error": gin.H{
				"code":    "Internal",
				"message": fmt.Sprintf("%v", err),
			},
		})
	})
}
