package audit

import "context"

// MultiAuditor 把事件顺序广播到全部后端（fan-out）。
//
// 顺序即构造序；nil 成员跳过。单个成员 panic 不在本层隔离——旁路 recover
// 由调用方统一兜底（中间件 recordSafely 纪律），本层不 recover 可让成员
// bug 在测试中暴露而非被静默吞掉（与 log.MultiLogger 同款取舍）。
type MultiAuditor []Auditor

// Record 顺序广播事件到全部非 nil 成员。
func (m MultiAuditor) Record(ctx context.Context, event AuditEvent) {
	for _, a := range m {
		if a == nil {
			continue
		}
		a.Record(ctx, event)
	}
}

// NewMultiAuditor 组合多个后端为一个广播审计器：过滤 nil 成员，全空时
// 返回 Nop（零副作用）。R1-2 协调器按后端名排序重建全局审计器时使用，
// 保证「多后端共存」场景一次注入即可全量生效。
func NewMultiAuditor(auditors ...Auditor) Auditor {
	list := make([]Auditor, 0, len(auditors))
	for _, a := range auditors {
		if a != nil {
			list = append(list, a)
		}
	}
	if len(list) == 0 {
		return NopAuditor()
	}
	return MultiAuditor(list)
}
