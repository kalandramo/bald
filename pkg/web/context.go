package web

import (
	"context"

	"github.com/kalandramo/bald/pkg/contextx"
)

// RequestIDFromContext 从 context 读取请求 ID。由 pkg/middleware/gin.RequestID
// 注入，缺失时返回空串。
func RequestIDFromContext(ctx context.Context) string {
	return contextx.RequestIDFromContext(ctx)
}
