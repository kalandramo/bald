package appkit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// effectLog 记录 undo 的回放顺序。
type effectLog struct {
	mu    sync.Mutex
	order []string
}

func (l *effectLog) add(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.order = append(l.order, name)
}

func (l *effectLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.order))
	copy(out, l.order)
	return out
}

// TestEffect_UndoReverseOrder 逆序回放契约：后注册的先撤销（与依赖建立顺序对称）。
func TestEffect_UndoReverseOrder(t *testing.T) {
	l := &effectLog{}
	app := New(
		Effect("a", func(context.Context) error { l.add("undo-a"); return nil }),
		Effect("b", func(context.Context) error { l.add("undo-b"); return nil }),
		Effect("c", func(context.Context) error { l.add("undo-c"); return nil }),
	)

	app.UndoEffects(context.Background())

	want := []string{"undo-c", "undo-b", "undo-a"}
	if got := l.snapshot(); !equalStrings(got, want) {
		t.Fatalf("undo order = %v, want %v", got, want)
	}
}

// TestEffect_Idempotent 回放幂等契约：账本回放后清空，重复调用无副作用。
func TestEffect_Idempotent(t *testing.T) {
	l := &effectLog{}
	app := New(
		Effect("a", func(context.Context) error { l.add("undo-a"); return nil }),
	)

	app.UndoEffects(context.Background())
	app.UndoEffects(context.Background()) // 第二次应为无操作

	if got := l.snapshot(); !equalStrings(got, []string{"undo-a"}) {
		t.Fatalf("second undo should be no-op, got %v", got)
	}
}

// TestEffect_PanicIsolated panic 隔离契约：单条 undo panic 不拖垮其余回放。
func TestEffect_PanicIsolated(t *testing.T) {
	l := &effectLog{}
	app := New(
		Effect("a", func(context.Context) error { l.add("undo-a"); return nil }),
		Effect("boom", func(context.Context) error { panic("boom") }),
		Effect("c", func(context.Context) error { l.add("undo-c"); return nil }),
	)

	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("UndoEffects should not propagate panic, got %v", r)
			}
			close(done)
		}()
		app.UndoEffects(context.Background())
	}()
	<-done

	if got := l.snapshot(); !equalStrings(got, []string{"undo-c", "undo-a"}) {
		t.Fatalf("panicking undo must not skip others, got %v", got)
	}
}

// TestEffect_UndoOnErrorReturn undo 返回错误契约：记录日志但继续回放其余效应。
func TestEffect_UndoOnErrorReturn(t *testing.T) {
	l := &effectLog{}
	app := New(
		Effect("a", func(context.Context) error { l.add("undo-a"); return nil }),
		Effect("fail", func(context.Context) error { return errors.New("fail") }),
	)

	app.UndoEffects(context.Background())

	if got := l.snapshot(); !equalStrings(got, []string{"undo-a"}) {
		t.Fatalf("failed undo must not stop others, got %v", got)
	}
}

// TestEffect_RunUndoesOnShutdown 停机回放契约：Run 正常退出（ctx 取消触发
// 优雅停机）时，stopAll 阶段 0 自动逆序回放效应。
func TestEffect_RunUndoesOnShutdown(t *testing.T) {
	l := &effectLog{}
	app := New(
		Servers(newMock("fx-run")),
		Effect("a", func(context.Context) error { l.add("undo-a"); return nil }),
		Effect("b", func(context.Context) error { l.add("undo-b"); return nil }),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := app.Run(ctx); err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	}()
	cancel() // 触发优雅停机：stopAll 阶段 0 逆序回放效应
	<-app.Done()

	want := []string{"undo-b", "undo-a"}
	if got := l.snapshot(); !equalStrings(got, want) {
		t.Fatalf("Run shutdown should undo effects in reverse, got %v want %v", got, want)
	}
}

// TestEffect_UndoOnBeforeStartFailure 启动失败回滚契约：beforeStart 钩子失败
// 的路径（不经过 stopAll）也必须回放账本。
func TestEffect_UndoOnBeforeStartFailure(t *testing.T) {
	l := &effectLog{}
	app := New(
		Servers(newMock("fx-fail")),
		Effect("a", func(context.Context) error { l.add("undo-a"); return nil }),
		BeforeStart(func(context.Context) error { return errors.New("boom") }),
	)

	if err := app.Run(context.Background()); err == nil {
		t.Fatal("Run should fail when beforeStart hook fails")
	}

	if got := l.snapshot(); !equalStrings(got, []string{"undo-a"}) {
		t.Fatalf("startup failure should undo effects, got %v", got)
	}
}

// TestEffect_Timeout 每条 undo 独立超时契约：卡死的 undo 被超时收口，不拖垮后续。
func TestEffect_Timeout(t *testing.T) {
	l := &effectLog{}
	app := New(
		EffectTimeout(50*time.Millisecond),
		Effect("slow", func(ctx context.Context) error {
			<-ctx.Done() // 卡到超时
			return ctx.Err()
		}),
		Effect("b", func(context.Context) error { l.add("undo-b"); return nil }),
	)

	start := time.Now()
	app.UndoEffects(context.Background())

	if got := l.snapshot(); !equalStrings(got, []string{"undo-b"}) {
		t.Fatalf("slow undo should be timed out but others continue, got %v", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("undo took too long: %v", elapsed)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
