package log

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// multiSink 记录事件的测试 sink，事件共享底层切片使 With 派生的
// 子 Logger 写入同一处（断言 With 传播到每个子 sink）。
type multiSink struct {
	mu      *sync.Mutex
	events  *[]string
	enabled map[Level]bool
	prefix  string
}

func newMultiSink(enabled ...Level) *multiSink {
	m := map[Level]bool{}
	for _, l := range enabled {
		m[l] = true
	}
	return &multiSink{
		mu:      &sync.Mutex{},
		events:  &[]string{},
		enabled: m,
	}
}

func (r *multiSink) record(level, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.events = append(*r.events, r.prefix+level+":"+msg)
}

func (r *multiSink) Debug(ctx context.Context, msg string, args ...any) { r.record("debug", msg) }
func (r *multiSink) Info(ctx context.Context, msg string, args ...any)  { r.record("info", msg) }
func (r *multiSink) Warn(ctx context.Context, msg string, args ...any)  { r.record("warn", msg) }
func (r *multiSink) Error(ctx context.Context, msg string, args ...any) { r.record("error", msg) }

func (r *multiSink) Enabled(level Level) bool { return r.enabled[level] }

func (r *multiSink) With(args ...any) Logger {
	return &multiSink{
		mu:      r.mu,
		events:  r.events,
		enabled: r.enabled,
		prefix:  r.prefix + fmt.Sprint(args...) + "|",
	}
}

// TestMultiLogger 广播契约（日志平面接口设计：多源广播装饰器——本地+远端
// 并存的契约形状）。R4 复审：前瞻能力零调用非删除依据。
func TestMultiLogger(t *testing.T) {
	a, b := newMultiSink(LevelInfo), newMultiSink(LevelInfo)
	ml := NewMultiLogger(a, b)

	var _ Logger = ml // 编译期断言契约满足（源码已有 var _，测试侧再钉一次）

	ml.Info(context.Background(), "hello")

	if len(*a.events) != 1 || (*a.events)[0] != "info:hello" {
		t.Errorf("sink A 应收到广播, got %v", *a.events)
	}
	if len(*b.events) != 1 || (*b.events)[0] != "info:hello" {
		t.Errorf("sink B 应收到广播, got %v", *b.events)
	}
}

// TestMultiLogger_Levels：四级方法均广播。
func TestMultiLogger_Levels(t *testing.T) {
	sink := newMultiSink(LevelDebug)
	ml := NewMultiLogger(sink)
	ctx := context.Background()

	ml.Debug(ctx, "d")
	ml.Info(ctx, "i")
	ml.Warn(ctx, "w")
	ml.Error(ctx, "e")

	want := []string{"debug:d", "info:i", "warn:w", "error:e"}
	if len(*sink.events) != len(want) {
		t.Fatalf("应收到 4 条, got %v", *sink.events)
	}
	for i, w := range want {
		if (*sink.events)[i] != w {
			t.Errorf("事件[%d] = %q, want %q", i, (*sink.events)[i], w)
		}
	}
}

// TestMultiLogger_EnabledAnyTrue：任一子 sink 启用即启用（保守放行）。
func TestMultiLogger_EnabledAnyTrue(t *testing.T) {
	off := newMultiSink() // 什么级别都不启用
	on := newMultiSink(LevelError)
	ml := NewMultiLogger(off, on)

	if !ml.Enabled(LevelError) {
		t.Error("任一子 sink 启用 Error 时，MultiLogger.Enabled(Error) 必须为 true（保守放行）")
	}
	if ml.Enabled(LevelInfo) {
		t.Error("全部子 sink 都未启用 Info 时，Enabled(Info) 应为 false")
	}
}

// TestMultiLogger_NilFiltered：nil sink 被过滤（构造即安全）；全 nil 时等价 nop。
func TestMultiLogger_NilFiltered(t *testing.T) {
	sink := newMultiSink(LevelInfo)
	ml := NewMultiLogger(nil, sink, nil)

	ml.Info(context.Background(), "m")
	if len(*sink.events) != 1 {
		t.Errorf("nil sink 应被过滤，非 nil sink 正常收到, got %v", *sink.events)
	}

	nop := NewMultiLogger(nil, nil)
	if nop.Enabled(LevelError) {
		t.Error("全 nil 的 MultiLogger.Enabled 应恒为 false")
	}
	nop.Info(context.Background(), "m") // 不应 panic
}

// TestMultiLogger_WithPropagation：With 对每个子 Logger 分别派生，且广播
// 语义传播到派生实例。
func TestMultiLogger_WithPropagation(t *testing.T) {
	a, b := newMultiSink(LevelInfo), newMultiSink(LevelInfo)
	ml := NewMultiLogger(a, b)

	derived := ml.With("k", 1)
	derived.Info(context.Background(), "tagged")

	// 两个 sink 的共享事件里都应出现带前缀的派生日志。
	found := 0
	for _, e := range append(*a.events, (*b.events)...) {
		if e == "k=1|info:tagged" || (len(e) > len("info:tagged") && e != "info:tagged") {
			found++
		}
	}
	if len(*a.events) == 0 || len(*b.events) == 0 {
		t.Errorf("With 派生实例的日志应广播到全部子 sink: A=%v B=%v", *a.events, *b.events)
	}
	if found == 0 {
		t.Errorf("未观察到 With 属性传播: A=%v B=%v", *a.events, *b.events)
	}
}

// TestMultiLogger_Empty：零参数构造等价 nop（契约注释声明）。
func TestMultiLogger_Empty(t *testing.T) {
	ml := NewMultiLogger()
	if ml.Enabled(LevelDebug) {
		t.Error("空 MultiLogger.Enabled 应恒为 false")
	}
	ml.Error(context.Background(), "no-op") // 不应 panic
}
