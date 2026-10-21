package appkit

// stop_order_test.go 钉住 stopAll 五阶段次序（appkit.go stopAll 注释的回归锁）：
//
//	0 效应账本逆序回放 → 1 BeforeStop → 2 Server.Stop → 3 AfterStop →
//	4 组件 Dispose（AfterStop 之后——钩子期间组件仍可用，最后收尾）。
//
// AfterStop 在单入口上以裸名导出
// （AfterStop Option，Decl.AfterStop 承载）——本测试同时核验其生效落点
// 恰在 Server.Stop 与组件 Dispose 之间。

import (
	"context"
	"sync"
	"testing"
)

// stopRecorder 汇集停机阶段事件（Run 的停机在主 goroutine，串行安全仍加锁）。
type stopRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *stopRecorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *stopRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

// orderServer 记录 Stop 时序的最小 transport.Server（Start 阻塞到 ctx 取消）。
type orderServer struct{ rec *stopRecorder }

func (s *orderServer) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (s *orderServer) Stop(context.Context) error {
	s.rec.add("server.stop")
	return nil
}

func (s *orderServer) Endpoint() string { return "" }

// orderComp 记录 Dispose 时序的最小组件（不记 Start——只盯停机阶段）。
type orderComp struct{ rec *stopRecorder }

func (c *orderComp) Name() string { return "order-comp" }

func (c *orderComp) Start(context.Context) error { return nil }

func (c *orderComp) Dispose(context.Context) error {
	c.rec.add("component.dispose")
	return nil
}

// TestStopAll_PhaseOrder 契约：效应回放 → BeforeStop → Server.Stop →
// AfterStop → 组件 Dispose 的次序断言（对照 stopAll 五阶段注释）。
func TestStopAll_PhaseOrder(t *testing.T) {
	rec := &stopRecorder{}
	app := NewApp(
		Servers(&orderServer{rec: rec}),
		componentsOpt(&orderComp{rec: rec}),
		effectOpt("order:test-effect", func(context.Context) error {
			rec.add("effect.undo")
			return nil
		}),
		BeforeStop(func(context.Context) error {
			rec.add("beforeStop")
			return nil
		}),
		AfterStop(func(context.Context) error {
			rec.add("afterStop")
			return nil
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = app.Run(ctx) }()
	cancel() // 触发优雅停机
	<-app.Done()
	<-done

	want := []string{
		"effect.undo",       // 阶段 0：效应账本逆序回放
		"beforeStop",        // 阶段 1
		"server.stop",       // 阶段 2
		"afterStop",         // 阶段 3
		"component.dispose", // 阶段 4
	}
	got := rec.snapshot()
	if len(got) != len(want) {
		t.Fatalf("stop events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stop phase order = %v, want %v (divergence at %d)", got, want, i)
		}
	}
}
