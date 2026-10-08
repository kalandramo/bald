package gin

import (
	"fmt"
	"io"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

// RecoveryConfig 配置 panic 恢复中间件，对应契约 recovery 段。
type RecoveryConfig struct {
	// StackTrace 为 true 时，panic 响应体的 data.stack 字段携带调用栈。
	// 契约字段 server.http.middleware.recovery.stack_trace。
	//
	// ⚠️ 生产环境应保持 false：栈信息可能泄露内部路径与代码结构。默认 false。
	StackTrace bool
}

// Recovery 是 gin 中间件，恢复 handler 中的 panic 并返回统一 JSON 错误响应，
// 而不是 gin 默认的纯文本栈。返回 500。
//
// 可选 RecoveryConfig（变参以保持既有 Recovery() 调用零改动）：
// cfg.StackTrace=true 时在响应体附带调用栈。缺省（不传或空 cfg）行为与
// 此前完全一致——响应体不含栈。
func Recovery(cfg ...RecoveryConfig) gin.HandlerFunc {
	var c RecoveryConfig
	if len(cfg) > 0 {
		c = cfg[0]
	}
	return gin.RecoveryWithWriter(io.Discard, func(gc *gin.Context, err any) {
		body := gin.H{
			"error": gin.H{
				"code":    "Internal",
				"message": fmt.Sprintf("%v", err),
			},
		}
		if c.StackTrace {
			body["data"] = gin.H{"stack": string(debug.Stack())}
		}
		gc.AbortWithStatusJSON(500, body)
	})
}
