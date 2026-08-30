// Package audit 定义 bald 的审计日志抽象（零后端耦合、传输中立）。
//
// 设计原则（与 P7 认证、P9 授权对齐）：
//   - 核心只定义 AuditEvent 结构与 Auditor 接口，不 import 任何具体存储/消息总线
//     （落库 / 发 Kafka / 写 ES 等外置为桥接子模块）。
//   - 审计是旁路（side-effect），永远不阻断业务请求：即便 Auditor 报错也仅记日志，
//     不向上游返回错误。
//   - 审计三元组 (subject, object, action) 复用 P9 的 authz.ObjectResolver/ActionResolver
//     归一化原语，使 REST/gRPC 在同一份审计语义下被记录（谁在何时对什么资源做了什么）。
//
// 默认后端为 nop（静默），由用户在 AppKit 装配期经 audit.SetAuditor 注入具体实现，
// 或在拦截器/中间件构造时传入 Auditor 实例。
package audit

import (
	"context"
	"time"
)

// Result 是审计动作的结果分类。
type Result string

const (
	// ResultAllow 授权通过且 handler 成功返回。
	ResultAllow Result = "allow"
	// ResultDeny 授权被拒绝（PermissionDenied）。
	ResultDeny Result = "deny"
	// ResultError 请求处理过程出错（含认证失败/内部错误）。
	ResultError Result = "error"
)

// AuditEvent 是一条结构化访问审计记录。
//
// 字段均为扁平 key-value，便于直接落结构化日志或转存列式存储。
type AuditEvent struct {
	// Time 事件发生时刻（UTC）。由 Auditor 或拦截器填充，缺省时取记录时。
	Time time.Time
	// Subject 主体标识（来自 AuthClaims.Subject）；匿名/未认证时可能为空。
	Subject string
	// TenantID 租户标识（来自 AuthClaims.TenantID）；多租户隔离审计用。
	TenantID string
	// Object 被审计的资源（经 P9 归一化，如 "secret" "auth"）。
	Object string
	// Action 被审计的动作（经 P9 归一化，如 "get" "delete" "list" "write"）。
	Action string
	// Result 动作结果分类（allow/deny/error）。
	Result Result
	// Error 错误描述；Result=error 时填，其余为空。
	Error string
	// Meta 任意附加上下文（request_id / trace_id / 客户端 IP 等），由拦截器注入。
	Meta map[string]any
}

// Auditor 是审计记录器接口。具体实现（落库 / 消息总线 / 文件）外置为桥接子模块。
//
// 实现契约：Record 必须非阻塞或快速失败，绝不向上游抛错（旁路语义）；若自身写入
// 失败应内部降级（如记一条 error 日志），不得影响业务链路。
type Auditor interface {
	// Record 记录一条审计事件。
	Record(ctx context.Context, event AuditEvent)
}

// nopAuditor 是静默默认实现（未注入具体 Auditor 时不产生任何副作用）。
type nopAuditor struct{}

// Record 静默丢弃事件。
func (nopAuditor) Record(context.Context, AuditEvent) {}

// NopAuditor 返回静默默认 Auditor，可用于测试或无需审计的场景。
func NopAuditor() Auditor { return nopAuditor{} }
