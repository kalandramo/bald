package inmemory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kalandramo/bald/registry"
)

func inst(id, name, ep string) *registry.ServiceInstance {
	return &registry.ServiceInstance{ID: id, Name: name, Endpoints: []string{ep}}
}

func ids(list []*registry.ServiceInstance) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.ID)
	}
	return out
}

func equal(a, b []string) bool {
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

// nextWithin 带界地取一次变更：无界阻塞的 Next 在被测实现退化为「永不通知」时
// 会把测试拖到 go test 的全局超时，判据就没了。
func nextWithin(t *testing.T, w registry.Watcher, d time.Duration) ([]*registry.ServiceInstance, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return w.Next(ctx)
}

func TestGetServiceFiltersByName(t *testing.T) {
	r := New()
	ctx := context.Background()
	for _, in := range []*registry.ServiceInstance{inst("a1", "svc-a", "127.0.0.1:1"), inst("b1", "svc-b", "127.0.0.1:2")} {
		if err := r.Register(ctx, in); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	got, err := r.GetService(ctx, "svc-a")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if !equal(ids(got), []string{"a1"}) {
		t.Fatalf("GetService(svc-a) = %v, want [a1]", ids(got))
	}
	// 未知服务名是空集而非错误：拨号方据此报「无实例」。
	empty, err := r.GetService(ctx, "svc-z")
	if err != nil || len(empty) != 0 {
		t.Fatalf("GetService(svc-z) = %v/%v, want []/nil", ids(empty), err)
	}
	if all, err := r.GetService(ctx, ""); err != nil || len(all) != 2 {
		t.Fatalf("GetService(\"\") = %v/%v, want 2 个实例", ids(all), err)
	}
}

func TestGetServiceDeterministicOrder(t *testing.T) {
	r := New()
	ctx := context.Background()
	// 乱序注册，取回必须按 ID 定序：resolver 用下标切主副本，顺序抖动会让
	// 端点集在无变更时也「变更」。
	for _, id := range []string{"c", "a", "b"} {
		if err := r.Register(ctx, inst(id, "svc", "127.0.0.1:"+id)); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	for range 5 {
		got, err := r.GetService(ctx, "svc")
		if err != nil {
			t.Fatal(err)
		}
		if !equal(ids(got), []string{"a", "b", "c"}) {
			t.Fatalf("order = %v, want [a b c]", ids(got))
		}
	}
}

func TestWatchDeliversInitialSnapshotThenChanges(t *testing.T) {
	r := New()
	ctx := context.Background()
	if err := r.Register(ctx, inst("a1", "svc", "127.0.0.1:1")); err != nil {
		t.Fatal(err)
	}
	w, err := r.Watch(ctx, "svc")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Stop() }()

	// 订阅立刻拿到当前快照——resolver 靠这一次避开「先等变更才有权重」。
	first, err := nextWithin(t, w, time.Second)
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if !equal(ids(first), []string{"a1"}) {
		t.Fatalf("initial snapshot = %v, want [a1]", ids(first))
	}

	if err := r.Register(ctx, inst("a2", "svc", "127.0.0.1:2")); err != nil {
		t.Fatal(err)
	}
	second, err := nextWithin(t, w, time.Second)
	if err != nil {
		t.Fatalf("Next after register: %v", err)
	}
	if !equal(ids(second), []string{"a1", "a2"}) {
		t.Fatalf("after register = %v, want [a1 a2]", ids(second))
	}

	// 别的服务名变更不该惊动本订阅：用超时证「没有通知」。
	if err := r.Register(ctx, inst("b1", "other", "127.0.0.1:3")); err != nil {
		t.Fatal(err)
	}
	if got, err := nextWithin(t, w, 120*time.Millisecond); err == nil {
		t.Fatalf("other-service change leaked into watcher: %v", ids(got))
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}

	if err := r.Deregister(ctx, inst("a1", "svc", "127.0.0.1:1")); err != nil {
		t.Fatal(err)
	}
	third, err := nextWithin(t, w, time.Second)
	if err != nil {
		t.Fatalf("Next after deregister: %v", err)
	}
	if !equal(ids(third), []string{"a2"}) {
		t.Fatalf("after deregister = %v, want [a2]", ids(third))
	}
}

func TestWatchOnlyDeliversLatest(t *testing.T) {
	r := New()
	ctx := context.Background()
	w, err := r.Watch(ctx, "svc")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Stop() }()
	// 订阅者还没读，连开三个实例：只应收到合并后的最新全量，而不是三次抖动。
	for _, id := range []string{"a", "b", "c"} {
		if err := r.Register(ctx, inst(id, "svc", "127.0.0.1:"+id)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := nextWithin(t, w, time.Second)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !equal(ids(got), []string{"a", "b", "c"}) {
		t.Fatalf("latest snapshot = %v, want [a b c]", ids(got))
	}
	if g2, err := nextWithin(t, w, 120*time.Millisecond); err == nil {
		t.Fatalf("陈旧快照被重复投递: %v", ids(g2))
	}
}

func TestStopUnregistersWatcher(t *testing.T) {
	r := New()
	ctx := context.Background()
	w, err := r.Watch(ctx, "svc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nextWithin(t, w, time.Second); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop 二次调用应安全, got %v", err)
	}
	if _, err := w.Next(ctx); !errors.Is(err, errWatcherClosed) {
		t.Fatalf("Next after Stop = %v, want errWatcherClosed", err)
	}
	r.mu.RLock()
	n := len(r.watchers["svc"])
	r.mu.RUnlock()
	if n != 0 {
		t.Fatalf("Stop 后仍挂着 %d 个 watcher（泄漏）", n)
	}
	// 注销后 Register 不再触达已停订阅：此调用不应 panic 也不应留空表。
	if err := r.Register(ctx, inst("a1", "svc", "127.0.0.1:1")); err != nil {
		t.Fatal(err)
	}
	r.mu.RLock()
	_, exists := r.watchers["svc"]
	r.mu.RUnlock()
	if exists {
		t.Fatal("空 watcher 集合未回收")
	}
}
