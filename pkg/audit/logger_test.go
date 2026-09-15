package audit

import (
	"context"
	"sync"
	"testing"

	"github.com/kalandramo/bald/log"
)

// logSink 是测试用日志桩：捕获经全局 Logger 输出的调用（保存/恢复全局）。
type logSink struct {
	mu     sync.Mutex
	level  log.Level
	msg    string
	args   []any
	called bool
}

func (s *logSink) Debug(ctx context.Context, msg string, args ...any) {
	s.record(log.LevelDebug, msg, args)
}
func (s *logSink) Info(ctx context.Context, msg string, args ...any) {
	s.record(log.LevelInfo, msg, args)
}
func (s *logSink) Warn(ctx context.Context, msg string, args ...any) {
	s.record(log.LevelWarn, msg, args)
}
func (s *logSink) Error(ctx context.Context, msg string, args ...any) {
	s.record(log.LevelError, msg, args)
}

func (s *logSink) Enabled(level log.Level) bool { return true }

func (s *logSink) With(args ...any) log.Logger { return s }

func (s *logSink) record(level log.Level, msg string, args []any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.level, s.msg, s.args, s.called = level, msg, args, true
}

func (s *logSink) snapshot() (log.Level, string, []any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.level, s.msg, s.args, s.called
}

// withLogSink 注入日志桩并注册恢复，返回桩实例。
func withLogSink(t *testing.T) *logSink {
	t.Helper()
	sink := &logSink{}
	old := log.GetLogger()
	log.SetLogger(sink)
	t.Cleanup(func() { log.SetLogger(old) })
	return sink
}

// hasPair 报告 args 中是否存在 (key, value) 对。
func hasPair(args []any, key, value string) bool {
	for i := 0; i+1 < len(args); i += 2 {
		if k, ok := args[i].(string); ok && k == key {
			if v, ok := args[i+1].(string); ok && v == value {
				return true
			}
		}
	}
	return false
}

// TestLoggerAuditor_AllowEmitsInfo 契约：非 error 结果走 Info 级，
// 固定字段（subject/object/action/result）与 Meta 均输出。
func TestLoggerAuditor_AllowEmitsInfo(t *testing.T) {
	sink := withLogSink(t)

	LoggerAuditor{}.Record(context.Background(), AuditEvent{
		Subject:  "u1",
		TenantID: "t1",
		Object:   "secret",
		Action:   "get",
		Result:   ResultAllow,
		Meta:     map[string]any{"request_id": "req-1"},
	})

	level, msg, args, called := sink.snapshot()
	if !called || level != log.LevelInfo || msg != "audit" {
		t.Fatalf("call = (called=%v, level=%v, msg=%q), want Info/audit", called, level, msg)
	}
	for _, kv := range [][2]string{
		{"subject", "u1"}, {"tenant_id", "t1"},
		{"object", "secret"}, {"action", "get"},
		{"result", "allow"}, {"request_id", "req-1"},
	} {
		if !hasPair(args, kv[0], kv[1]) {
			t.Errorf("args missing pair %s=%s: %v", kv[0], kv[1], args)
		}
	}
}

// TestLoggerAuditor_ErrorEmitsErrorLevel 契约：Result=error 升为 Error 级并附 error 字段。
func TestLoggerAuditor_ErrorEmitsErrorLevel(t *testing.T) {
	sink := withLogSink(t)

	LoggerAuditor{}.Record(context.Background(), AuditEvent{
		Subject: "u2",
		Object:  "secret",
		Action:  "delete",
		Result:  ResultError,
		Error:   "boom",
	})

	level, _, args, called := sink.snapshot()
	if !called || level != log.LevelError {
		t.Fatalf("level = %v (called=%v), want LevelError", level, called)
	}
	if !hasPair(args, "error", "boom") {
		t.Errorf("args missing error=boom: %v", args)
	}
}

// TestNewLoggerAuditor_ReturnsInstance 契约：构造器返回可用实例（接口满足）。
func TestNewLoggerAuditor_ReturnsInstance(t *testing.T) {
	var a Auditor = NewLoggerAuditor()
	if a == nil {
		t.Fatal("NewLoggerAuditor returned nil")
	}
	a.Record(context.Background(), AuditEvent{Result: ResultDeny})
}
