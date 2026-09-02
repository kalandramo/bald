package log

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestNopLoggerIsSilent(t *testing.T) {
	var l Logger = nopLogger{}
	if l.Enabled(LevelDebug) {
		t.Fatal("nop logger should not be enabled")
	}
	// 调用不应 panic。
	l.Debug(context.Background(), "x")
	l.Info(context.Background(), "x")
	l.Warn(context.Background(), "x")
	l.Error(context.Background(), "x")
	if got := l.With("k", "v"); got == nil {
		t.Fatal("With should return non-nil")
	}
}

func TestSetGetLogger(t *testing.T) {
	defer SetLogger(nil) // 清理为默认 nop

	buf := &bytes.Buffer{}
	opt := NewOptions()
	opt.Format = "json"
	l := NewSlogLogger(opt, withWriter(buf))
	SetLogger(l)

	if GetLogger() != l {
		t.Fatal("GetLogger should return the injected logger")
	}

	GetLogger().Info(context.Background(), "hello", "k", "v")
	out := buf.String()
	if !strings.Contains(out, "hello") || !strings.Contains(out, "k") {
		t.Fatalf("unexpected output: %s", out)
	}

	// 注入 nil 回退 nop。
	SetLogger(nil)
	if _, ok := GetLogger().(nopLogger); !ok {
		t.Fatal("SetLogger(nil) should fall back to nop")
	}
}

func TestSlogLoggerLevels(t *testing.T) {
	buf := &bytes.Buffer{}
	l := NewSlogLogger(NewOptions(), withWriter(buf))
	if !l.Enabled(LevelInfo) {
		t.Fatal("info should be enabled by default")
	}
	if l.Enabled(LevelDebug) {
		t.Fatal("debug should be disabled by default")
	}
	l.Debug(context.Background(), "dbg") // 默认不输出
	l.Error(context.Background(), "err", "code", 500)
	out := buf.String()
	if strings.Contains(out, "dbg") {
		t.Fatal("debug should be suppressed at info level")
	}
	if !strings.Contains(out, "err") {
		t.Fatalf("error should be present: %s", out)
	}
}

func TestFilterRedactsSensitiveKey(t *testing.T) {
	buf := &bytes.Buffer{}
	l := NewSlogLogger(NewOptions(), withWriter(buf), WithFilter(FilterKey("password")))
	l.Info(context.Background(), "login", "user", "alice", "password", "secret")

	out := buf.String()
	if strings.Contains(out, "secret") {
		t.Fatalf("password must be redacted: %s", out)
	}
	if !strings.Contains(out, "alice") {
		t.Fatalf("non-sensitive field must remain: %s", out)
	}
}

// TestFilterRedactsViaWith 验证 D2：logger.With 固化的敏感属性同样被脱敏
// （修复前 filterHandler.WithAttrs 不过滤，属性绕过 Handle 直接落盘）。
func TestFilterRedactsViaWith(t *testing.T) {
	buf := &bytes.Buffer{}
	l := NewSlogLogger(NewOptions(), withWriter(buf), WithFilter(FilterKey("password")))
	l.With("password", "secret").Info(context.Background(), "login", "user", "alice")

	out := buf.String()
	if strings.Contains(out, "secret") {
		t.Fatalf("password fixed via With must be redacted: %s", out)
	}
	if !strings.Contains(out, "alice") {
		t.Fatalf("non-sensitive field must remain: %s", out)
	}
}

// TestFilterRedactsViaWithAttrsOption 验证 D2：构造期 WithAttrs Option 固化的
// 敏感属性同样被脱敏。
func TestFilterRedactsViaWithAttrsOption(t *testing.T) {
	buf := &bytes.Buffer{}
	l := NewSlogLogger(NewOptions(), withWriter(buf),
		WithFilter(FilterKey("password")),
		WithAttrs(slog.String("password", "secret")),
	)
	l.Info(context.Background(), "login", "user", "alice")

	out := buf.String()
	if strings.Contains(out, "secret") {
		t.Fatalf("password fixed via WithAttrs option must be redacted: %s", out)
	}
	if !strings.Contains(out, "alice") {
		t.Fatalf("non-sensitive field must remain: %s", out)
	}
}

func TestContextAttrsPropagated(t *testing.T) {
	buf := &bytes.Buffer{}
	l := NewSlogLogger(NewOptions(), withWriter(buf))
	ctx := ContextWithAttrs(context.Background(), slog.String("request_id", "req-123"))
	l.Info(ctx, "handled")
	out := buf.String()
	if !strings.Contains(out, "req-123") {
		t.Fatalf("context attr should propagate: %s", out)
	}
}

func TestConcurrentSetGet(t *testing.T) {
	defer SetLogger(nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := &bytes.Buffer{}
			SetLogger(NewSlogLogger(NewOptions(), withWriter(buf)))
			_ = GetLogger().Enabled(LevelInfo)
		}()
	}
	wg.Wait()
}
