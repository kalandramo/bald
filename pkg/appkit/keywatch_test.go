package appkit

import (
	"context"
	"sync"
	"testing"
	"time"
)

// kwMemSource 内存 RemoteSource（同 config module 测试模式），用于驱动远程变更。
type kwMemSource struct {
	mu      sync.Mutex
	data    []byte
	format  string
	watchCh chan struct{}
}

func newKWSource(data string) *kwMemSource {
	return &kwMemSource{data: []byte(data), format: "yaml", watchCh: make(chan struct{}, 1)}
}

func (s *kwMemSource) Read(context.Context) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data, s.format, nil
}

func (s *kwMemSource) Watch(_ context.Context, onChange func([]byte, string)) error {
	go func() {
		for range s.watchCh {
			s.mu.Lock()
			d, f := s.data, s.format
			s.mu.Unlock()
			onChange(d, f)
		}
	}()
	return nil
}

func (s *kwMemSource) update(data string) {
	s.mu.Lock()
	s.data = []byte(data)
	s.mu.Unlock()
	select {
	case s.watchCh <- struct{}{}:
	default:
	}
}

// kwEvent 记录一次 key 触发。
type kwEvent struct{ old, new string }

// kwCollector 收集触发事件（线程安全，测试等待用）。
type kwCollector struct {
	mu     sync.Mutex
	events []kwEvent
}

func (c *kwCollector) record(old, new string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, kwEvent{old, new})
}

func (c *kwCollector) snapshot() []kwEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]kwEvent(nil), c.events...)
}

func (c *kwCollector) waitN(t *testing.T, n int, timeout time.Duration) []kwEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if evs := c.snapshot(); len(evs) >= n {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("wait %d events timeout, got %v", n, c.snapshot())
	return nil
}

// TestOnKeyChange_TriggersOnDiff 契约：key 值变化触发，old/new 正确。
func TestOnKeyChange_TriggersOnDiff(t *testing.T) {
	src := newKWSource("http:\n  addr: \":8080\"\n")
	col := &kwCollector{}
	app := New(
		RemoteConfig(src),
		OnKeyChange("http.addr", col.record),
	)
	if err := app.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	src.update("http:\n  addr: \":9090\"\n")
	evs := col.waitN(t, 1, 2*time.Second)
	if len(evs) != 1 || evs[0].old != ":8080" || evs[0].new != ":9090" {
		t.Fatalf("events = %v, want [{:8080 :9090}]", evs)
	}
}

// TestOnKeyChange_NoTriggerOnSame 契约：同值刷新不触发（增量语义核心）。
func TestOnKeyChange_NoTriggerOnSame(t *testing.T) {
	src := newKWSource("http:\n  addr: \":8080\"\n")
	col := &kwCollector{}
	app := New(
		RemoteConfig(src),
		OnKeyChange("http.addr", col.record),
	)
	if err := app.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	// 变更文件但 addr 值不变（新增其他 key）。
	src.update("http:\n  addr: \":8080\"\n  timeout: \"5s\"\n")
	time.Sleep(300 * time.Millisecond) // 给分发留窗口
	if evs := col.snapshot(); len(evs) != 0 {
		t.Fatalf("same value must not trigger, got %v", evs)
	}
}

// TestOnKeyChange_MultipleKeys 独立订阅：变更 keyB 不波及 keyA 的 watcher。
func TestOnKeyChange_MultipleKeys(t *testing.T) {
	src := newKWSource("http:\n  addr: \":8080\"\ngrpc:\n  addr: \":9080\"\n")
	colA, colB := &kwCollector{}, &kwCollector{}
	app := New(
		RemoteConfig(src),
		OnKeyChange("http.addr", colA.record),
		OnKeyChange("grpc.addr", colB.record),
	)
	if err := app.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	src.update("http:\n  addr: \":8081\"\ngrpc:\n  addr: \":9080\"\n") // 仅 http.addr 变
	colA.waitN(t, 1, 2*time.Second)
	if evs := colB.snapshot(); len(evs) != 0 {
		t.Fatalf("grpc.addr watcher must not fire, got %v", evs)
	}
}

// TestOnKeyChange_WithUserOnChange 契约：全量 OnConfigChange 与 key 订阅共存
// （先全量后分发），全量行为不受影响。
func TestOnKeyChange_WithUserOnChange(t *testing.T) {
	src := newKWSource("http:\n  addr: \":8080\"\n")
	col := &kwCollector{}
	fullRuns := 0
	var mu sync.Mutex
	app := New(
		RemoteConfig(src),
		OnKeyChange("http.addr", col.record),
		OnConfigChange(func(map[string]any) {
			mu.Lock()
			fullRuns++
			mu.Unlock()
		}),
	)
	if err := app.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	src.update("http:\n  addr: \":9090\"\n")
	col.waitN(t, 1, 2*time.Second)
	mu.Lock()
	runs := fullRuns
	mu.Unlock()
	if runs != 1 {
		t.Fatalf("user OnChange runs = %d, want 1", runs)
	}
}

// TestOnKeyChange_MultiUpdate 契约：连续多次变更按序分发，每次 old 是上次新值。
func TestOnKeyChange_MultiUpdate(t *testing.T) {
	src := newKWSource("http:\n  addr: \":8080\"\n")
	col := &kwCollector{}
	app := New(
		RemoteConfig(src),
		OnKeyChange("http.addr", col.record),
	)
	if err := app.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	src.update("http:\n  addr: \":9090\"\n")
	col.waitN(t, 1, 2*time.Second)
	src.update("http:\n  addr: \":7070\"\n")
	evs := col.waitN(t, 2, 2*time.Second)
	if evs[1].old != ":9090" || evs[1].new != ":7070" {
		t.Fatalf("second event = %+v, want old=:9090 new=:7070", evs[1])
	}
}
