package audit

// Compose 单测：多后端组装、后端为空、第 N 个失败时已建后端
// 逆序关闭、重复名回滚、cleanup 聚合。

import (
	"context"
	"errors"
	"testing"
)

// recordingAuditor 记录 Record 调用（组装顺序断言用）。
type recordingAuditor struct {
	name  string
	calls *[]string
}

func (a recordingAuditor) Record(_ context.Context, _ AuditEvent) {
	*a.calls = append(*a.calls, "record:"+a.name)
}

// namedBackend 构造 NamedBackend：构造时记 "build:<name>"；cleanup 记
// "cleanup:<name>" 并返回 cleanupErr；buildErr 非 nil 时构造失败。
func namedBackend(name string, calls *[]string, buildErr, cleanupErr error) NamedBackend {
	return NamedBackend{Name: name, New: func(context.Context) (Auditor, func(context.Context) error, error) {
		*calls = append(*calls, "build:"+name)
		if buildErr != nil {
			return nil, nil, buildErr
		}
		cleanup := func(context.Context) error {
			*calls = append(*calls, "cleanup:"+name)
			return cleanupErr
		}
		return recordingAuditor{name: name, calls: calls}, cleanup, nil
	}}
}

func assertEvents(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events[%d]=%q want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

// 后端为空：NopAuditor + nil cleanup + nil error（与 NewMultiAuditor 全空语义一致）。
func TestCompose_Empty(t *testing.T) {
	a, cleanup, err := Compose(context.Background())
	if err != nil {
		t.Fatalf("empty compose: %v", err)
	}
	if a != NopAuditor() {
		t.Fatalf("empty compose should return NopAuditor, got %T(%v)", a, a)
	}
	if cleanup != nil {
		t.Fatal("empty compose should return nil cleanup")
	}
}

// 单后端：直接返回该实例（类型断言友好），不包 MultiAuditor。
func TestCompose_Single(t *testing.T) {
	var calls []string
	a, _, err := Compose(context.Background(), namedBackend("store", &calls, nil, nil))
	if err != nil {
		t.Fatalf("single compose: %v", err)
	}
	rec, ok := a.(recordingAuditor)
	if !ok || rec.name != "store" {
		t.Fatalf("single compose should return the backend itself, got %T(%v)", a, a)
	}
	if _, isMulti := a.(MultiAuditor); isMulti {
		t.Fatal("single compose must not wrap MultiAuditor")
	}
}

// 多后端：按声明序合并 MultiAuditor（顺序即广播序），cleanup 聚合逆序回放，
// 成员 cleanup 错误经 errors.Join 汇总不吞项。
func TestCompose_MultiOrderAndAggregate(t *testing.T) {
	var calls []string
	a, cleanup, err := Compose(context.Background(),
		namedBackend("log", &calls, nil, nil),
		namedBackend("store", &calls, nil, nil),
		namedBackend("stream", &calls, nil, errors.New("flush failed")),
	)
	if err != nil {
		t.Fatalf("multi compose: %v", err)
	}
	m, ok := a.(MultiAuditor)
	if !ok || len(m) != 3 {
		t.Fatalf("multi compose should return 3-member MultiAuditor, got %T(%v)", a, a)
	}
	for i, want := range []string{"log", "store", "stream"} {
		rec, ok := m[i].(recordingAuditor)
		if !ok || rec.name != want {
			t.Fatalf("member[%d] should be %q, got %T(%v)", i, want, m[i], m[i])
		}
	}
	if cleanup == nil {
		t.Fatal("aggregate cleanup should be non-nil when members carry cleanups")
	}
	if err := cleanup(context.Background()); err == nil || err.Error() != "flush failed" {
		t.Fatalf("aggregate cleanup should surface member error, got %v", err)
	}
	assertEvents(t, calls, []string{
		"build:log", "build:store", "build:stream",
		"cleanup:stream", "cleanup:store", "cleanup:log",
	})
}

// 第 N 个失败：已建后端逆序关闭（后构造先释放），错误定位到失败单元。
func TestCompose_NthFailureRollsBackReverse(t *testing.T) {
	var calls []string
	a, _, err := Compose(context.Background(),
		namedBackend("log", &calls, nil, nil),
		namedBackend("store", &calls, nil, nil),
		namedBackend("stream", &calls, errors.New("boom"), nil),
	)
	if err == nil || a != nil {
		t.Fatalf("partial failure should fail: a=%v err=%v", a, err)
	}
	var ce *ComposeError
	if !errors.As(err, &ce) || ce.Backend != "stream" || ce.Err.Error() != "boom" {
		t.Fatalf("error should locate failing backend, got %v", err)
	}
	// stream 构造失败（无 cleanup 可回滚）→ 逆序回滚 store、log。
	assertEvents(t, calls, []string{"build:log", "build:store", "build:stream", "cleanup:store", "cleanup:log"})
}

// 重复名：在构造序中检测（前序已构造项先逆序回滚），错误携带 ErrDuplicateBackend。
func TestCompose_DuplicateName(t *testing.T) {
	var calls []string
	_, _, err := Compose(context.Background(),
		namedBackend("store", &calls, nil, nil),
		namedBackend("store", &calls, nil, nil),
	)
	if err == nil {
		t.Fatal("duplicate name should fail")
	}
	if !errors.Is(err, ErrDuplicateBackend) {
		t.Fatalf("error should carry ErrDuplicateBackend, got %v", err)
	}
	var ce *ComposeError
	if !errors.As(err, &ce) || ce.Backend != "store" {
		t.Fatalf("error should locate duplicated backend, got %v", err)
	}
	assertEvents(t, calls, []string{"build:store", "cleanup:store"})
}

// 全部成员 cleanup 为 nil：聚合 cleanup 为 nil（不产生空函数）。
func TestCompose_NilCleanupsAggregateToNil(t *testing.T) {
	_, cleanup, err := Compose(context.Background(),
		NamedBackend{Name: "log", New: func(context.Context) (Auditor, func(context.Context) error, error) {
			return NewLoggerAuditor(), nil, nil
		}},
	)
	if err != nil || cleanup != nil {
		t.Fatalf("nil cleanups should aggregate to nil: cleanup=%v err=%v", cleanup != nil, err)
	}
}
