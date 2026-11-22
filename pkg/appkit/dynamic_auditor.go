package appkit

import (
	"context"
	"sync/atomic"

	"github.com/kalandramo/bald/pkg/audit"
)

// auditorHolder 以固定类型承载接口值。不用 atomic.Value[audit.Auditor]，
// 因为 atomic.Value 要求每次 Store 的 concrete type 一致，而 Nop/Multi/Stream
// 等实现异构，直接存放会在切换时 panic。包一层指针即可保证类型恒定。
type auditorHolder struct{ a audit.Auditor }

// DynamicAuditor 是可热切换的审计 sink 代理。每次 Record 原子读取当前目标，
// 因此构造期快照了 auditor 的中间件无需重建：只要注入的是本代理，Store 切换后
// 的下一次 Record 即落到新目标。
type DynamicAuditor struct {
	cur       atomic.Pointer[auditorHolder]
	transform func(context.Context, audit.AuditEvent) audit.AuditEvent
}

// 编译期保证实现完整。
var _ audit.Auditor = (*DynamicAuditor)(nil)

// DynamicAuditorOption 配置 DynamicAuditor。
type DynamicAuditorOption func(*DynamicAuditor)

// WithAuditTransform 设置事件变换函数：每次 Record 在转发前对事件做一次变换，
// 可用于注入 trace_id / 补齐字段等。
func WithAuditTransform(fn func(context.Context, audit.AuditEvent) audit.AuditEvent) DynamicAuditorOption {
	return func(d *DynamicAuditor) { d.transform = fn }
}

// NewDynamicAuditor 构造代理，初始目标为 NopAuditor（静默），可带若干 Option。
func NewDynamicAuditor(opts ...DynamicAuditorOption) *DynamicAuditor {
	d := &DynamicAuditor{}
	for _, o := range opts {
		o(d)
	}
	d.cur.Store(&auditorHolder{a: audit.NopAuditor()})
	return d
}

// Store 切换当前审计目标；nil 归一为 NopAuditor，避免 Record 时空目标。
func (d *DynamicAuditor) Store(a audit.Auditor) {
	if a == nil {
		a = audit.NopAuditor()
	}
	d.cur.Store(&auditorHolder{a: a})
}

// Load 返回当前审计目标，永不为 nil。nil 接收者安全（返回 NopAuditor）。
func (d *DynamicAuditor) Load() audit.Auditor {
	if d == nil {
		return audit.NopAuditor()
	}
	if h := d.cur.Load(); h != nil {
		return h.a
	}
	return audit.NopAuditor()
}

// Record 实现 audit.Auditor：先应用变换（若有），再把事件转发给当前目标。
// nil 接收者安全（静默丢弃）。
func (d *DynamicAuditor) Record(ctx context.Context, ev audit.AuditEvent) {
	if d == nil {
		return
	}
	if d.transform != nil {
		ev = d.transform(ctx, ev)
	}
	d.Load().Record(ctx, ev)
}
