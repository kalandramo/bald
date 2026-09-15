package audit

import (
	"context"

	"github.com/kalandramo/bald/log"
)

// LoggerAuditor 把审计事件写进框架日志（结构化 key-value，info/error 按
// 结果分级）。零外部依赖、开箱即用，是审计后端的最小真实实现：可经
// slog 等日志后端落文件/采集，生产可替换为落库/消息总线实现（contrib
// 桥接子模块）。
//
// 旁路语义：日志写入自身不会失败（log 契约无错误返回），panic 防护由
// 调用方 recordSafely 统一兜底。
type LoggerAuditor struct{}

// Record 实现 Auditor：输出一条结构化审计日志。
//
// 固定字段 subject/tenant_id/object/action/result 顺序输出，Meta 逐键
// 追加（map 遍历序不定，消费侧按键取值不受影响）；Result=error 时升为
// Error 级并附 error 字段，其余为 Info 级。
func (LoggerAuditor) Record(ctx context.Context, e AuditEvent) {
	args := []any{
		"subject", e.Subject,
		"tenant_id", e.TenantID,
		"object", e.Object,
		"action", e.Action,
		"result", string(e.Result),
	}
	for k, v := range e.Meta {
		args = append(args, k, v)
	}
	if e.Result == ResultError {
		if e.Error != "" {
			args = append(args, "error", e.Error)
		}
		log.Error(ctx, "audit", args...)
		return
	}
	log.Info(ctx, "audit", args...)
}

// NewLoggerAuditor 返回日志审计后端实例。
func NewLoggerAuditor() Auditor { return LoggerAuditor{} }
