package log

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

// filterSink 记录日志调用（模拟远端后端行为：合并顺序 prefix(With 派生) →
// ctx 属性流 → 调用参数——与 loki/aliyun 等后端一致），供脱敏断言。
type filterSink struct {
	prefix []any
	calls  *[]filterCall
	mu     *sync.Mutex
}

type filterCall struct {
	level string
	msg   string
	args  []any
}

func (s *filterSink) Debug(ctx context.Context, msg string, args ...any) {
	s.append(ctx, "debug", msg, args)
}

func (s *filterSink) Info(ctx context.Context, msg string, args ...any) {
	s.append(ctx, "info", msg, args)
}

func (s *filterSink) Warn(ctx context.Context, msg string, args ...any) {
	s.append(ctx, "warn", msg, args)
}

func (s *filterSink) Error(ctx context.Context, msg string, args ...any) {
	s.append(ctx, "error", msg, args)
}

func (s *filterSink) Enabled(level Level) bool { return true }

func (s *filterSink) With(args ...any) Logger {
	return &filterSink{
		prefix: append(append([]any{}, s.prefix...), args...),
		calls:  s.calls,
		mu:     s.mu,
	}
}

func (s *filterSink) append(ctx context.Context, level, msg string, args []any) {
	full := append(append([]any{}, s.prefix...), ContextAttrsToArgs(ctx)...)
	full = append(full, args...)
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.calls = append(*s.calls, filterCall{level, msg, full})
}

func newFilterSink() *filterSink {
	calls := &[]filterCall{}
	return &filterSink{calls: calls, mu: &sync.Mutex{}}
}

func lastArgs(calls []filterCall) []any {
	return calls[len(calls)-1].args
}

func argsContains(args []any, k, v string) bool {
	for i := 0; i+1 < len(args); i += 2 {
		if ks, ok := args[i].(string); ok && ks == k {
			vs, ok2 := args[i+1].(string)
			return ok2 && vs == v
		}
	}
	return false
}

// TestFilterLoggerArgs 验证调用参数来源：命中 key 值掩码、未命中保留。
func TestFilterLoggerArgs(t *testing.T) {
	sink := newFilterSink()
	fl := NewFilterLogger(sink, "password", "token")

	fl.Info(context.Background(), "login",
		"user", "alice", "password", "secret123", "token", "abc")

	args := lastArgs(*sink.calls)
	if !argsContains(args, "user", "alice") {
		t.Errorf("未命中 key 应保留原值: %v", args)
	}
	if !argsContains(args, "password", "***") {
		t.Errorf("password 应被掩码: %v", args)
	}
	if !argsContains(args, "token", "***") {
		t.Errorf("token 应被掩码: %v", args)
	}
}

// TestFilterLoggerWithDerived 验证 With 派生来源：入口过滤后下沉，且派生
// 实例继续过滤后续调用参数。
func TestFilterLoggerWithDerived(t *testing.T) {
	sink := newFilterSink()
	fl := NewFilterLogger(sink, "password")

	derived := fl.With("module", "api", "password", "should-mask")
	derived.Info(context.Background(), "req", "password", "also-mask", "path", "/v1")

	args := lastArgs(*sink.calls)
	if !argsContains(args, "module", "api") {
		t.Errorf("With 未命中 key 应保留: %v", args)
	}
	if !argsContains(args, "password", "***") {
		t.Errorf("With 命中 key 应掩码: %v", args)
	}
	if !argsContains(args, "path", "/v1") {
		t.Errorf("调用参数未命中 key 应保留: %v", args)
	}
}

// TestFilterLoggerCtxAttrs 验证 ctx 属性流来源：命中项掩码、未命中保留——
// 内层经 ContextAttrsToArgs 读到的即掩码值（模拟远端后端提取路径）。
func TestFilterLoggerCtxAttrs(t *testing.T) {
	sink := newFilterSink()
	fl := NewFilterLogger(sink, "password")

	ctx := ContextWithAttrs(context.Background(),
		slog.String("trace_id", "t-123"), slog.String("password", "ctx-secret"))
	fl.Info(ctx, "req", "user", "bob")

	args := lastArgs(*sink.calls)
	if !argsContains(args, "trace_id", "t-123") {
		t.Errorf("ctx 未命中 key 应保留: %v", args)
	}
	if !argsContains(args, "password", "***") {
		t.Errorf("ctx 命中 key 应被掩码: %v", args)
	}
}

// TestFilterLoggerCtxNil 验证 nil ctx 安全直通。
func TestFilterLoggerCtxNil(t *testing.T) {
	sink := newFilterSink()
	fl := NewFilterLogger(sink, "password")
	fl.Info(nil, "msg", "password", "x") // 不应 panic
	if !argsContains(lastArgs(*sink.calls), "password", "***") {
		t.Errorf("nil ctx 下调用参数仍应过滤")
	}
}

// TestFilterLoggerEnabledPassthrough 验证 Enabled 透传内层语义。
func TestFilterLoggerEnabledPassthrough(t *testing.T) {
	sink := newFilterSink()
	fl := NewFilterLogger(sink, "password")
	if !fl.Enabled(LevelDebug) {
		t.Errorf("Enabled 应透传内层（sink 恒 true）")
	}
}

// TestFilterLoggerEmptyKeysPassthrough 验证空 keys 零开销直通（返回原实例）。
func TestFilterLoggerEmptyKeysPassthrough(t *testing.T) {
	sink := newFilterSink()
	if got := NewFilterLogger(sink); got != Logger(sink) {
		t.Errorf("空 keys 应原样返回")
	}
	if got := NewFilterLogger(nil, "password"); got != nil {
		t.Errorf("nil Logger 应原样返回")
	}
}

// TestFilterLoggerOverMultiLogger 验证与 MultiLogger 嵌套（backends 装配
// 路径形状：出口包装在 MultiLogger 之外）——全部子后端收到掩码值。
func TestFilterLoggerOverMultiLogger(t *testing.T) {
	a, b := newFilterSink(), newFilterSink()
	ml := NewMultiLogger(a, b)
	fl := NewFilterLogger(ml, "password")

	fl.Info(context.Background(), "req", "password", "secret", "user", "carol")

	for name, sink := range map[string]*filterSink{"a": a, "b": b} {
		if len(*sink.calls) != 1 {
			t.Fatalf("子后端 %s 应收到 1 条, got %d", name, len(*sink.calls))
		}
		args := lastArgs(*sink.calls)
		if !argsContains(args, "password", "***") {
			t.Errorf("子后端 %s 应收到掩码值: %v", name, args)
		}
		if !argsContains(args, "user", "carol") {
			t.Errorf("子后端 %s 未命中 key 应保留: %v", name, args)
		}
	}
}

// TestFilterLoggerNonStringKey 验证非 string key 不参与匹配（类型安全），
// 且奇数长度尾部（缺 value）不越界。
func TestFilterLoggerNonStringKey(t *testing.T) {
	sink := newFilterSink()
	fl := NewFilterLogger(sink, "password")

	fl.Info(context.Background(), "odd", 42, "value", "password", "x", "trailing")
	args := lastArgs(*sink.calls)
	if len(args) != 5 {
		t.Fatalf("参数应原样透传（非 string key 不命中）, got %v", args)
	}
	if args[1] != "value" {
		t.Errorf("非 string key 后的 value 应保留: %v", args)
	}
	if !argsContains(args, "password", "***") {
		t.Errorf("string key 命中仍应掩码: %v", args)
	}
}
