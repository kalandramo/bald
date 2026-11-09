// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package grpcdiscovery

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/resolver"

	"github.com/kalandramo/bald/registry"
)

// fakeDiscovery 返回固定快照；watchErr 控订阅建不起来的情形。
type fakeDiscovery struct {
	instances []*registry.ServiceInstance
	getErr    error
	watchErr  error
	watcher   registry.Watcher
	services  []string
}

func (d *fakeDiscovery) GetService(_ context.Context, name string) ([]*registry.ServiceInstance, error) {
	d.services = append(d.services, name)
	return d.instances, d.getErr
}

func (d *fakeDiscovery) Watch(_ context.Context, name string) (registry.Watcher, error) {
	d.services = append(d.services, name)
	return d.watcher, d.watchErr
}

// fakeWatcher 先吐调用方预置的队列，队列空后按 err 返回或阻塞到 ctx 结束。
type fakeWatcher struct {
	mu      sync.Mutex
	queue   [][]*registry.ServiceInstance
	err     error
	stopErr error

	stopped atomic.Int32
	nextEnd atomic.Int32
}

func (w *fakeWatcher) push(instances ...*registry.ServiceInstance) {
	w.mu.Lock()
	w.queue = append(w.queue, instances)
	w.mu.Unlock()
}

func (w *fakeWatcher) Next(ctx context.Context) ([]*registry.ServiceInstance, error) {
	w.mu.Lock()
	if len(w.queue) > 0 {
		next := w.queue[0]
		w.queue = w.queue[1:]
		w.mu.Unlock()
		return next, nil
	}
	err := w.err
	w.mu.Unlock()
	if err != nil {
		return nil, err
	}
	<-ctx.Done()
	w.nextEnd.Add(1)
	return nil, ctx.Err()
}

func (w *fakeWatcher) Stop() error {
	w.stopped.Add(1)
	return w.stopErr
}

// recordCC 记录 UpdateState/ReportError；updateErr 控推送失败。
type recordCC struct {
	resolver.ClientConn

	mu        sync.Mutex
	states    []resolver.State
	errs      []error
	updateErr error
}

func (c *recordCC) UpdateState(s resolver.State) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.states = append(c.states, s)
	return c.updateErr
}

func (c *recordCC) ReportError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errs = append(c.errs, err)
}

func (c *recordCC) snapshot() ([]resolver.State, []error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]resolver.State(nil), c.states...), append([]error(nil), c.errs...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func errContains(errs []error, substr string) bool {
	for _, err := range errs {
		if strings.Contains(err.Error(), substr) {
			return true
		}
	}
	return false
}

func targetOf(t *testing.T, raw string) resolver.Target {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse target %q: %v", raw, err)
	}
	return resolver.Target{URL: *u}
}

func TestUsesTargetAndScheme(t *testing.T) {
	cases := map[string]bool{
		"discovery:///svc": true,
		"discovery://svc":  true,
		"dns:///svc":       false,
		"Discovery:///svc": false,
		"discovery":        false,
		"":                 false,
	}
	for target, want := range cases {
		if got := UsesTarget(target); got != want {
			t.Errorf("UsesTarget(%q) = %v, want %v", target, got, want)
		}
	}
	if got := New(nil).Scheme(); got != Scheme {
		t.Errorf("Scheme() = %q, want %q", got, Scheme)
	}
	if Scheme != "discovery" {
		t.Errorf("Scheme = %q, want discovery", Scheme)
	}
}

// 服务名为空是配置错误，Build 直接失败（重试也不会变好）。
func TestBuild_ServiceNameRequired(t *testing.T) {
	b := New(&fakeDiscovery{})
	_, err := b.Build(targetOf(t, "discovery:///"), &recordCC{}, resolver.BuildOptions{})
	if err == nil {
		t.Fatal("expected error for empty service name")
	}
	if !strings.Contains(err.Error(), "has no service name") {
		t.Errorf("error = %v, want service-name hint", err)
	}
}

// Watch 建不起来必须把错误交回 gRPC，让它按退避重建 resolver。
func TestBuild_WatchError(t *testing.T) {
	d := &fakeDiscovery{watchErr: errors.New("watch down")}
	b := New(d)
	_, err := b.Build(targetOf(t, "discovery:///svc"), &recordCC{}, resolver.BuildOptions{})
	if err == nil {
		t.Fatal("expected error when watch cannot be established")
	}
	if !strings.Contains(err.Error(), "watch svc: watch down") {
		t.Errorf("error = %v, want wrapped watch error", err)
	}
}

// 首次快照失败只上报原因、保留订阅：等 Watch 推送把实例带回来。
func TestBuild_GetErrorReportsAndKeepsWatch(t *testing.T) {
	w := &fakeWatcher{}
	d := &fakeDiscovery{getErr: errors.New("backend cold"), watcher: w}
	cc := &recordCC{}

	r, err := New(d).Build(targetOf(t, "discovery:///svc"), cc, resolver.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	states, errs := cc.snapshot()
	if len(states) != 0 {
		t.Fatalf("failed discovery must not publish, got %d states", len(states))
	}
	if !errContains(errs, "waiting for watch push") {
		t.Fatalf("reports = %v, want waiting-for-watch hint", errs)
	}

	r.Close()
	waitFor(t, "run goroutine exit", func() bool { return w.nextEnd.Load() == 1 })
}

// publish 直接覆盖：空集上报、属性变更重发、同快照去重、UpdateState 失败上报。
func TestPublish_Branches(t *testing.T) {
	cc := &recordCC{}
	r := &discoveryResolver{service: "svc", cc: cc}

	r.publish(nil)
	r.publish([]*registry.ServiceInstance{instance("i", nil, "http://10.0.0.1:8080")})
	states, errs := cc.snapshot()
	if len(states) != 0 {
		t.Fatalf("empty instance set must not call UpdateState, got %d", len(states))
	}
	if len(errs) != 2 || !errContains(errs, "no instances") {
		t.Fatalf("empty set errors = %v, want two no-instances reports", errs)
	}

	good := []*registry.ServiceInstance{instance("i", map[string]string{"w": "1"}, "10.0.0.1:9000")}
	r.publish(good)
	r.publish(good) // 同快照去重
	states, _ = cc.snapshot()
	if len(states) != 1 {
		t.Fatalf("duplicate snapshot must be skipped, got %d states", len(states))
	}
	if got := states[0].Addresses[0].Addr; got != "10.0.0.1:9000" {
		t.Errorf("published address = %q", got)
	}

	cc.updateErr = errors.New("cc down")
	r.lastKey = "" // 放行重发，验证失败不记指纹
	r.publish(good)
	_, errs = cc.snapshot()
	if !errContains(errs, "publish svc: cc down") {
		t.Fatalf("UpdateState error not reported: %v", errs)
	}
	if r.lastKey != "" {
		t.Errorf("lastKey = %q, want empty after failed UpdateState", r.lastKey)
	}
}

// Build 先同步推首帧，run 后续推送新快照并去重重复帧。
func TestBuild_PublishAndRun(t *testing.T) {
	first := instance("i1", nil, "grpc://10.0.0.1:9000")
	second := instance("i2", nil, "grpc://10.0.0.2:9000")
	w := &fakeWatcher{}
	w.push(first)  // 订阅成功后的重复快照：与 Build 首帧相同
	w.push(second) // 真实变更
	d := &fakeDiscovery{instances: []*registry.ServiceInstance{first}, watcher: w}
	cc := &recordCC{}

	r, err := New(d).Build(targetOf(t, "discovery:///svc"), cc, resolver.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if states, _ := cc.snapshot(); len(states) != 1 {
		t.Fatalf("Build must publish initial snapshot, got %d states", len(states))
	}

	waitFor(t, "second snapshot", func() bool {
		states, _ := cc.snapshot()
		return len(states) == 2
	})
	states, errs := cc.snapshot()
	if got := states[1].Addresses[0].Addr; got != "10.0.0.2:9000" {
		t.Errorf("second snapshot address = %q", got)
	}
	if len(errs) != 0 {
		t.Errorf("unexpected reports: %v", errs)
	}

	r.Close()
}

// Watch 出错时上报一次并退出，把重建交给 gRPC 退避。
func TestRun_WatchErrorReportsAndExits(t *testing.T) {
	w := &fakeWatcher{err: errors.New("watch broke")}
	d := &fakeDiscovery{
		instances: []*registry.ServiceInstance{instance("i", nil, "10.0.0.1:9000")},
		watcher:   w,
	}
	cc := &recordCC{}

	r, err := New(d).Build(targetOf(t, "discovery:///svc"), cc, resolver.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	waitFor(t, "watch error report", func() bool {
		_, errs := cc.snapshot()
		return errContains(errs, "watch svc: watch broke")
	})

	r.ResolveNow(resolver.ResolveNowOptions{}) // 无事可做，不应 panic
	r.Close()
	if w.stopped.Load() != 1 {
		t.Errorf("Stop calls = %d, want 1", w.stopped.Load())
	}
}

// Close 幂等；Close 引起的 Next 返回不算故障，不上报。
func TestClose_IdempotentSilent(t *testing.T) {
	w := &fakeWatcher{}
	d := &fakeDiscovery{
		instances: []*registry.ServiceInstance{instance("i", nil, "10.0.0.1:9000")},
		watcher:   w,
	}
	cc := &recordCC{}
	r, err := New(d).Build(targetOf(t, "discovery:///svc"), cc, resolver.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	r.Close()
	r.Close()
	waitFor(t, "run goroutine exit", func() bool { return w.nextEnd.Load() == 1 })

	if got := w.stopped.Load(); got != 1 {
		t.Errorf("Stop calls = %d, want 1 (Close must be idempotent)", got)
	}
	if _, errs := cc.snapshot(); len(errs) != 0 {
		t.Errorf("Close-triggered Next return must not report: %v", errs)
	}
}

func TestGrpcAddressOf_Edges(t *testing.T) {
	cases := []struct {
		ep   string
		want string
		ok   bool
	}{
		{"", "", false},
		{"grpc://10.0.0.1:9000", "10.0.0.1:9000", true},
		{"grpc://", "", true},
		{"http://10.0.0.1:8080", "", false},
		{"https://10.0.0.1:8443", "", false},
		{"10.0.0.1:9000", "10.0.0.1:9000", true},
	}
	for _, c := range cases {
		got, ok := grpcAddressOf(c.ep)
		if got != c.want || ok != c.ok {
			t.Errorf("grpcAddressOf(%q) = (%q, %v), want (%q, %v)", c.ep, got, ok, c.want, c.ok)
		}
	}
}

// 无属性地址的指纹就是裸地址（历史字节行为）。
func TestAddressFingerprint_NoAttributes(t *testing.T) {
	if got := addressFingerprint(resolver.Address{Addr: "10.0.0.1:9000"}); got != "10.0.0.1:9000" {
		t.Errorf("fingerprint = %q, want bare address", got)
	}
}

// 结构性零覆盖（登记备查）：无。Build/run/publish/Close 的每条语句都由
// 上述用例直接执行。
