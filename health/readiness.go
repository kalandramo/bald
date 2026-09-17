package health

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Readiness 把聚合结果适配为「返回 error」的形态，便于喂给任何以
// `func(ctx context.Context) error` 表达就绪的位置：gRPC health 状态同步、
// 业务自建探针、启动门禁等。
//
// 语义与 [Handler] 的 HTTP 映射一致：整体 Up/Unknown 返回 nil（宁可乐观，
// 与《Bald 健康检查设计》决策②同口径）；任一检查器 Down 返回错误，错误串
// 聚合全部 Down 检查器的名称与消息（按名称排序，便于日志比对）。
func (h *Health) Readiness(ctx context.Context) error {
	res := h.Check(ctx)
	if res.Status != StatusDown {
		return nil
	}

	failed := make([]string, 0, len(res.Details))
	for name, detail := range res.Details {
		m, ok := detail.(map[string]any)
		if !ok || m["status"] != StatusDown.String() {
			continue
		}
		if msg, _ := m["message"].(string); msg != "" {
			failed = append(failed, name+": "+msg)
			continue
		}
		failed = append(failed, name)
	}
	sort.Strings(failed)

	if len(failed) == 0 {
		return errors.New("health: not ready")
	}
	return fmt.Errorf("health: not ready: %s", strings.Join(failed, "; "))
}
