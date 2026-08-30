// Package audit 是 go-bald-admin 的审计后端桥接（bald audit.Auditor 的具体实现）。
//
// 范例采用「结构化日志审计」后端：每条 AuditEvent 经框架 log 契约输出为一条带
// subject/object/action/result 的结构化日志（可经 slog 后端落文件/采集）。这是真实
// 副作用（非 fake/stub），符合设计文档 §0 实现契约；生产可替换为落库/消息总线实现。
package audit

import (
	"context"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/log"
)

// LoggerAuditor 把审计事件写进框架日志（info/error 按结果分级）。
type LoggerAuditor struct{}

// Record 实现 audit.Auditor：输出结构化审计日志。
func (LoggerAuditor) Record(ctx context.Context, e audit.AuditEvent) {
	logger := log.GetLogger()
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
	if e.Result == audit.ResultError {
		if e.Error != "" {
			args = append(args, "error", e.Error)
		}
		logger.Error(ctx, "audit", args...)
		return
	}
	logger.Info(ctx, "audit", args...)
}

// New 返回日志审计后端实例。
func New() audit.Auditor { return LoggerAuditor{} }
