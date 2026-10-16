// compose.go 实现「多后端组装 + 逆序回滚」的声明式收敛（批 1 自 appkit
// 内化，装配层评估 D1 §4.2a：组装是审计能力自己的语义，不该住在装配层）。
//
// appkit 的 AuditRegistry.Build 只做「契约 backends 列表 → 构造器列表」的
// 映射（不触发构造），构造、失败逆序回滚与多后端合并全部收敛到 Compose：
//
//	auditor, cleanup, err := audit.Compose(ctx,
//	    audit.NamedBackend{Name: "store", New: newStore},
//	    audit.NamedBackend{Name: "stream", New: newStream},
//	)
//
// 语义与原先 appkit 内联实现逐字一致：按声明序构造（顺序即广播序）；
// 任一单元失败（重复名或构造出错）逆序回滚已构造单元的 cleanup；成功时
// 单后端直接返回、多后端经 NewMultiAuditor 合并，cleanup 聚合为逆序回放。
package audit

import (
	"context"
	"errors"
	"fmt"
)

// ErrDuplicateBackend 标记 Compose 的重复后端名失败。经 *ComposeError.Err
// 携带，调用方用 errors.Is 识别后给出自己的契约文案（Compose 不认识任何
// 契约段名，不代拟文案）。
var ErrDuplicateBackend = errors.New("duplicate backend")

// ComposeError 定位 Compose 组装失败发生在哪个单元：Backend 是失败单元的
// 名字；Err 是失败原因（重复名时为 ErrDuplicateBackend，构造失败时为构造
// 函数返回的错误原值，调用方可用 errors.Is/As 继续解包）。
type ComposeError struct {
	Backend string
	Err     error
}

func (e *ComposeError) Error() string {
	return fmt.Sprintf("audit backend %q: %v", e.Backend, e.Err)
}

// Unwrap 透出底层错误，保持 errors.Is/As 链路可用。
func (e *ComposeError) Unwrap() error { return e.Err }

// NamedBackend 是带名的单个后端惰性构造器：Compose 按声明序调用 New
// （顺序即广播序），Name 用于重复检测与失败定位。
type NamedBackend struct {
	Name string
	New  func(ctx context.Context) (Auditor, func(context.Context) error, error)
}

// Compose 按声明序构造全部后端并组装为一个 Auditor：
//
//   - 空列表：返回 NopAuditor + nil cleanup + nil error（与 NewMultiAuditor
//     的「全空返回 Nop」语义一致）；
//   - 单后端：直接返回该实例（类型断言友好），不做包装；
//   - 多后端：NewMultiAuditor 合并，cleanup 聚合为逆序回放（后构造先释放，
//     errors.Join 汇总成员错误），无 cleanup 单元时返回 nil；
//   - 任一单元失败（名字重复或 New 出错）：逆序回滚已构造单元的 cleanup
//     （失败路径尽力释放，错误忽略），返回 *ComposeError 定位失败单元。
func Compose(ctx context.Context, backs ...NamedBackend) (Auditor, func(context.Context) error, error) {
	var (
		auditors []Auditor
		seen     = make(map[string]struct{}, len(backs))
		cleanups []func(context.Context) error
		rollback = func() {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i](ctx) // 失败路径尽力释放，错误忽略
			}
		}
	)
	for _, b := range backs {
		if _, dup := seen[b.Name]; dup {
			rollback()
			return nil, nil, &ComposeError{Backend: b.Name, Err: ErrDuplicateBackend}
		}
		seen[b.Name] = struct{}{}
		a, cleanup, err := b.New(ctx)
		if err != nil {
			rollback()
			return nil, nil, &ComposeError{Backend: b.Name, Err: err}
		}
		auditors = append(auditors, a)
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}

	var a Auditor
	if len(auditors) == 1 {
		a = auditors[0] // 单后端直接返回（类型断言友好）
	} else {
		a = NewMultiAuditor(auditors...) // 列表序 = 广播序
	}
	var aggregated func(context.Context) error
	if len(cleanups) > 0 {
		aggregated = func(ctx context.Context) error {
			var errs []error
			for i := len(cleanups) - 1; i >= 0; i-- { // 逆序：后构造先释放
				if err := cleanups[i](ctx); err != nil {
					errs = append(errs, err)
				}
			}
			return errors.Join(errs...)
		}
	}
	return a, aggregated, nil
}
