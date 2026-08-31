package appkit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// compLog 记录组件启停调用序（线程安全）。
type compLog struct {
	mu    sync.Mutex
	order []string
}

func (l *compLog) add(ev string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.order = append(l.order, ev)
}

func (l *compLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.order))
	copy(out, l.order)
	return out
}

// fakeComp 可编程启停的测试组件。
type fakeComp struct {
	name       string
	log        *compLog
	startErr   error
	dispErr    error
	startPanic bool
}

func (f *fakeComp) Name() string { return f.name }
func (f *fakeComp) Start(context.Context) error {
	f.log.add("start:" + f.name)
	if f.startPanic {
		panic("start boom")
	}
	return f.startErr
}
func (f *fakeComp) Dispose(context.Context) error {
	f.log.add("dispose:" + f.name)
	return f.dispErr
}

// TestComponents_StartThenReverseDispose 契约：顺序启动（注册序）、停机逆序销毁。
func TestComponents_StartThenReverseDispose(t *testing.T) {
	l := &compLog{}
	app := New(
		Components(
			&fakeComp{name: "a", log: l},
			&fakeComp{name: "b", log: l},
			&fakeComp{name: "c", log: l},
		),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = app.Run(ctx) }()
	cancel()
	<-app.Done()

	want := []string{"start:a", "start:b", "start:c", "dispose:c", "dispose:b", "dispose:a"}
	if got := l.snapshot(); !equalStrings(got, want) {
		t.Fatalf("lifecycle order = %v, want %v", got, want)
	}
}

// TestComponent_StartFailureRollback 启动失败回滚契约：组件 b Start 失败 →
// 已启动的 a 被逆序 Dispose；b、c 不 Dispose；错误信息含组件名。
func TestComponent_StartFailureRollback(t *testing.T) {
	l := &compLog{}
	app := New(
		Components(
			&fakeComp{name: "a", log: l},
			&fakeComp{name: "b", log: l, startErr: errors.New("no db")},
			&fakeComp{name: "c", log: l},
		),
	)

	err := app.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), `component "b" start`) {
		t.Fatalf("Run should fail with component name, got %v", err)
	}
	want := []string{"start:a", "start:b", "dispose:a"}
	if got := l.snapshot(); !equalStrings(got, want) {
		t.Fatalf("rollback order = %v, want %v", got, want)
	}
}

// TestComponent_StartPanicIsolated 组件 Start panic：转为错误返回，不击穿 Run。
func TestComponent_StartPanicIsolated(t *testing.T) {
	l := &compLog{}
	app := New(
		Components(&fakeComp{name: "boom", log: l, startPanic: true}),
	)
	err := app.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("start panic should convert to error, got %v", err)
	}
}

// TestComponent_DisposeErrorIsolated Dispose 出错不阻断其余销毁，也不影响 Run 正常退出。
func TestComponent_DisposeErrorIsolated(t *testing.T) {
	l := &compLog{}
	app := New(
		Components(
			&fakeComp{name: "a", log: l},
			&fakeComp{name: "bad", log: l, dispErr: errors.New("flush failed")},
		),
	)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = app.Run(ctx) }()
	cancel()
	<-app.Done()

	want := []string{"start:a", "start:bad", "dispose:bad", "dispose:a"}
	if got := l.snapshot(); !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestComponent_DisposeTimeout 每个组件 Dispose 独立超时：卡死的组件被收口，
// 其余组件照常销毁。
func TestComponent_DisposeTimeout(t *testing.T) {
	l := &compLog{}
	app := New(
		ComponentTimeout(50 * time.Millisecond),
	)
	// slow 的 Dispose 卡到 ctx 超时（独立于 fakeComp 的可编程行为）。
	app.components = append(app.components, slowComp{log: l})
	app.components = append(app.components, &fakeComp{name: "quick", log: l})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = app.Run(ctx) }()
	cancel()
	<-app.Done()

	// 逆序：quick 后启动先 dispose（照常完成），slow 先注册后销毁（超时收口不拖垮）。
	want := []string{"start:slow", "start:quick", "dispose:quick", "dispose:slow"}
	if got := l.snapshot(); !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

type slowComp struct{ log *compLog }

func (s slowComp) Name() string { return "slow-comp" }
func (s slowComp) Start(ctx context.Context) error {
	s.log.add("start:slow")
	return nil
}
func (s slowComp) Dispose(ctx context.Context) error {
	s.log.add("dispose:slow")
	<-ctx.Done() // 卡到超时
	return ctx.Err()
}

// TestComponentFunc 纯清理组件：Start no-op，Dispose 调用注入函数；nil dispose 安全。
func TestComponentFunc(t *testing.T) {
	called := false
	c := ComponentFunc("trace.provider", func(context.Context) error {
		called = true
		return nil
	})
	if c.Name() != "trace.provider" {
		t.Errorf("Name = %q", c.Name())
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start should be no-op, got %v", err)
	}
	if err := c.Dispose(context.Background()); err != nil || !called {
		t.Fatalf("Dispose should call fn: called=%v err=%v", called, err)
	}
	if err := ComponentFunc("nil", nil).Dispose(context.Background()); err != nil {
		t.Fatalf("nil dispose should be safe, got %v", err)
	}
}

// TestComponent_DontDisposeUnstarted Run 因 beforeStart 失败退出时，组件尚未启动，
// 不应触发任何 Dispose。
func TestComponent_DontDisposeUnstarted(t *testing.T) {
	l := &compLog{}
	app := New(
		Components(&fakeComp{name: "a", log: l}),
		BeforeStart(func(context.Context) error { return errors.New("boom") }),
	)
	if err := app.Run(context.Background()); err == nil {
		t.Fatal("Run should fail")
	}
	if got := l.snapshot(); len(got) != 0 {
		t.Fatalf("unstarted components must not be started or disposed, got %v", got)
	}
}
