package log

import (
	"context"
	"sync"
	"testing"
)

// stubLogger 测试替身：记录 Enabled 调用，不产生输出。
// 契约层测试只依赖接口本身，不引入任何适配器实现。
type stubLogger struct{ enabled bool }

func (s *stubLogger) Debug(context.Context, string, ...any) {}
func (s *stubLogger) Info(context.Context, string, ...any)  {}
func (s *stubLogger) Warn(context.Context, string, ...any)  {}
func (s *stubLogger) Error(context.Context, string, ...any) {}
func (s *stubLogger) Enabled(Level) bool                    { return s.enabled }
func (s *stubLogger) With(...any) Logger                    { return s }

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

	l := &stubLogger{enabled: true}
	SetLogger(l)

	if GetLogger() != l {
		t.Fatal("GetLogger should return the injected logger")
	}

	// 注入 nil 回退 nop。
	SetLogger(nil)
	if _, ok := GetLogger().(nopLogger); !ok {
		t.Fatal("SetLogger(nil) should fall back to nop")
	}
}

func TestConcurrentSetGet(t *testing.T) {
	defer SetLogger(nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			SetLogger(&stubLogger{enabled: true})
			_ = GetLogger().Enabled(LevelInfo)
		}()
	}
	wg.Wait()
}
