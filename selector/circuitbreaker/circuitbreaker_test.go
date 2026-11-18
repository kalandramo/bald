// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package circuitbreaker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kalandramo/bald/berrors"
	cb "github.com/kalandramo/bald/circuitbreaker"
	wbsentinel "github.com/kalandramo/bald/circuitbreaker/sentinel"
	"github.com/kalandramo/bald/selector"
	"github.com/kalandramo/bald/selector/roundrobin"

	sentinelapi "github.com/alibaba/sentinel-golang/api"
	scb "github.com/alibaba/sentinel-golang/core/circuitbreaker"
)

// sentinelOnce 保证 sentinel 运行时只初始化一次（initSentinel 无幂等保护）。
// 资源名按序号派生：sentinel 规则表进程级共享，每个熔断器实例独占一个资源。
var (
	sentinelOnce sync.Once
	sentinelErr  error
	cbResSeq     atomic.Uint32
)

func ensureSentinel(t *testing.T) {
	t.Helper()
	sentinelOnce.Do(func() { sentinelErr = sentinelapi.InitDefault() })
	if sentinelErr != nil {
		t.Fatalf("sentinelapi.InitDefault: %v", sentinelErr)
	}
}

// newSentinelBreaker 造一个挂了错误计数规则的 sentinel 熔断器（每个实例独立
// 资源、独立失败率）。ErrorCount 判据完全由错误数驱动，State() 探针只加
// totalCount 不加错误数，对判定零稀释——测试确定性最好。
func newSentinelBreaker(t *testing.T, errs uint64) cb.CircuitBreaker {
	t.Helper()
	ensureSentinel(t)
	resource := fmt.Sprintf("selector-cb.test.%d", cbResSeq.Add(1))
	if _, err := scb.LoadRules([]*scb.Rule{{
		Resource:         resource,
		Strategy:         scb.ErrorCount,
		Threshold:        float64(errs),
		MinRequestAmount: errs,
		RetryTimeoutMs:   50,
		StatIntervalMs:   10000,
	}}); err != nil {
		t.Fatalf("LoadRules(%s): %v", resource, err)
	}
	return wbsentinel.New(resource)
}

// fakeBreaker 是状态可控、上报可计数的契约实现，供确定性断言用。
type fakeBreaker struct {
	mu      sync.Mutex
	state   cb.State
	success int
	failure int
	closed  bool
}

func (f *fakeBreaker) Allow() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == cb.StateOpen {
		return cb.ErrCircuitOpen
	}
	return nil
}

func (f *fakeBreaker) MarkSuccess() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.success++
}

func (f *fakeBreaker) MarkFailure() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failure++
}

func (f *fakeBreaker) Execute(_ context.Context, fn func() error) error {
	if err := f.Allow(); err != nil {
		return err
	}
	err := fn()
	if err != nil {
		f.MarkFailure()
		return err
	}
	f.MarkSuccess()
	return nil
}

func (f *fakeBreaker) State() cb.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeBreaker) setState(s cb.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = s
}

func (f *fakeBreaker) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeBreaker) counts() (success, failure int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.success, f.failure
}

// firstSelector 是确定性内层策略：总返回候选集首个节点，供硬排除断言用。
type firstSelector struct{}

func (firstSelector) Select(_ context.Context, nodes []selector.Node) (selector.Node, selector.DoneFunc, error) {
	if len(nodes) == 0 {
		return nil, nil, errors.New("firstSelector: no nodes")
	}
	return nodes[0], selector.NoopDone, nil
}

func node(addr string) selector.Node {
	return selector.NewNode(addr, "grpc", "svc", 1, nil)
}

func TestNewRegistryNilFactoryPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil factory 必须 panic（装配期 fail-fast）")
		}
	}()
	NewRegistry(nil)
}

func TestRegistryHealthyUnknownIsHealthy(t *testing.T) {
	r := NewRegistry(func(string) cb.CircuitBreaker { return &fakeBreaker{} })
	if !r.Healthy("never-seen") {
		t.Fatal("未构造熔断器的地址应视为健康（新节点乐观放行）")
	}
	// Healthy 查询不应空建熔断器。
	r.mu.RLock()
	n := len(r.breakers)
	r.mu.RUnlock()
	if n != 0 {
		t.Fatalf("Healthy 查询空建了 %d 个熔断器，want 0", n)
	}
}

func TestRegistryGetLazyAndSingleton(t *testing.T) {
	var calls atomic.Int64
	r := NewRegistry(func(string) cb.CircuitBreaker {
		calls.Add(1)
		return &fakeBreaker{}
	})
	b1 := r.Get("a")
	b2 := r.Get("a")
	if b1 != b2 {
		t.Fatal("同地址应返回同一熔断器实例")
	}
	if calls.Load() != 1 {
		t.Fatalf("工厂被调 %d 次，want 1（懒构造且单例）", calls.Load())
	}
	if r.Get("b") == b1 {
		t.Fatal("不同地址应是不同实例")
	}
}

func TestWithCircuitBreakerHardExcludesOpenNode(t *testing.T) {
	bad := &fakeBreaker{}
	bad.setState(cb.StateOpen)
	r := NewRegistry(func(string) cb.CircuitBreaker { return &fakeBreaker{} })
	r.breakers["bad"] = bad

	// 内层用 round_robin：若硬排除失效，坏节点会按轮询周期性地被选中。
	sel := WithCircuitBreaker(roundrobin.New(), r)
	nodes := []selector.Node{node("bad"), node("good1"), node("good2")}
	for i := 0; i < 100; i++ {
		n, done, err := sel.Select(context.Background(), nodes)
		if err != nil {
			t.Fatalf("第 %d 次 Select 报错: %v", i, err)
		}
		if n.Address() == "bad" {
			t.Fatalf("第 %d 次选中了熔断打开的坏节点——硬排除失效（p2c 降权才是概率性的）", i)
		}
		done(context.Background(), selector.DoneInfo{})
	}
}

func TestWithCircuitBreakerAllOpenReturnsErrCircuitOpen(t *testing.T) {
	r := NewRegistry(func(string) cb.CircuitBreaker {
		b := &fakeBreaker{}
		b.setState(cb.StateOpen)
		return b
	})
	sel := WithCircuitBreaker(firstSelector{}, r)
	nodes := []selector.Node{node("a"), node("b")}
	// 先各选一次以构造熔断器（工厂直接造 Open 态）。
	r.Get("a")
	r.Get("b")
	_, _, err := sel.Select(context.Background(), nodes)
	if !errors.Is(err, cb.ErrCircuitOpen) {
		t.Fatalf("全部节点熔断打开应返回 ErrCircuitOpen，got %v", err)
	}
}

func TestWithCircuitBreakerEmptyNodesReturnsErrCircuitOpen(t *testing.T) {
	r := NewRegistry(func(string) cb.CircuitBreaker { return &fakeBreaker{} })
	sel := WithCircuitBreaker(firstSelector{}, r)
	_, _, err := sel.Select(context.Background(), nil)
	if !errors.Is(err, cb.ErrCircuitOpen) {
		t.Fatalf("空候选集应返回 ErrCircuitOpen，got %v", err)
	}
}

func TestWithCircuitBreakerFeedbackMarks(t *testing.T) {
	fb := &fakeBreaker{}
	r := NewRegistry(func(string) cb.CircuitBreaker { return fb })
	sel := WithCircuitBreaker(firstSelector{}, r)

	_, done, err := sel.Select(context.Background(), []selector.Node{node("a")})
	if err != nil {
		t.Fatal(err)
	}
	done(context.Background(), selector.DoneInfo{Err: errors.New("boom")})
	_, done, err = sel.Select(context.Background(), []selector.Node{node("a")})
	if err != nil {
		t.Fatal(err)
	}
	done(context.Background(), selector.DoneInfo{})

	success, failure := fb.counts()
	if success != 1 || failure != 1 {
		t.Fatalf("反馈计数 success=%d failure=%d，want 1/1（Err 非空记失败，否则记成功）", success, failure)
	}
}

func TestWithCircuitBreakerRecoveryViaStateQuery(t *testing.T) {
	fb := &fakeBreaker{}
	fb.setState(cb.StateOpen)
	r := NewRegistry(func(string) cb.CircuitBreaker { return fb })
	r.Get("a")
	sel := WithCircuitBreaker(firstSelector{}, r)
	nodes := []selector.Node{node("a")}

	if _, _, err := sel.Select(context.Background(), nodes); !errors.Is(err, cb.ErrCircuitOpen) {
		t.Fatalf("Open 态应被排除，got %v", err)
	}
	// 模拟睡窗已过：State 转 HalfOpen（真实现过睡窗后探针放行即此形状）。
	fb.setState(cb.StateHalfOpen)
	n, _, err := sel.Select(context.Background(), nodes)
	if err != nil {
		t.Fatalf("HalfOpen 态应重新可选（恢复由 State 查询驱动），got %v", err)
	}
	if n.Address() != "a" {
		t.Fatalf("恢复后应选回 a，got %s", n.Address())
	}
}

func TestRegistryPruneClosesStale(t *testing.T) {
	stale := &fakeBreaker{}
	r := NewRegistry(func(string) cb.CircuitBreaker { return &fakeBreaker{} })
	r.Get("a")
	r.breakers["stale"] = stale

	r.Prune([]selector.Node{node("a")})

	r.mu.RLock()
	_, ok := r.breakers["stale"]
	n := len(r.breakers)
	r.mu.RUnlock()
	if ok || n != 1 {
		t.Fatalf("剪枝后应只剩 a（n=%d, stale 仍在=%v）", n, ok)
	}
	stale.mu.Lock()
	closed := stale.closed
	stale.mu.Unlock()
	if !closed {
		t.Fatal("被剪枝的熔断器应经 Close 释放")
	}
}

func TestRegistryPruneSteadyStateKeepsAll(t *testing.T) {
	r := NewRegistry(func(string) cb.CircuitBreaker { return &fakeBreaker{} })
	r.Get("a")
	r.Get("b")
	r.Prune([]selector.Node{node("a"), node("b")})
	r.mu.RLock()
	n := len(r.breakers)
	r.mu.RUnlock()
	if n != 2 {
		t.Fatalf("稳态剪枝不应删任何条目，n=%d want 2", n)
	}
}

func TestRegistryCloseReleasesAll(t *testing.T) {
	r := NewRegistry(func(string) cb.CircuitBreaker { return &fakeBreaker{} })
	b1 := r.Get("a").(*fakeBreaker)
	b2 := r.Get("b").(*fakeBreaker)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if !b1.closed || !b2.closed {
		t.Fatal("Close 应释放所有熔断器")
	}
	r.mu.RLock()
	n := len(r.breakers)
	r.mu.RUnlock()
	if n != 0 {
		t.Fatalf("Close 后注册表应清空，n=%d", n)
	}
}

// TestWithCircuitBreakerSentinelIntegration 用真 sentinel 证明 State/Mark*
// 契约端到端可用：喂失败跳闸 → 硬排除 → 睡窗后探针恢复。
func TestWithCircuitBreakerSentinelIntegration(t *testing.T) {
	r := NewRegistry(func(string) cb.CircuitBreaker {
		return newSentinelBreaker(t, 2)
	})
	defer r.Close()
	sel := WithCircuitBreaker(firstSelector{}, r)
	bad := node("bad")
	good := node("good")

	// 只喂 bad 单节点：内层总选它，done 反馈失败直到 sentinel 跳闸；
	// 一旦跳闸 Select 即回 ErrCircuitOpen（bad 被硬排除），据此停手。
	for i := 0; i < 8; i++ {
		_, done, err := sel.Select(context.Background(), []selector.Node{bad})
		if errors.Is(err, cb.ErrCircuitOpen) {
			break
		}
		if err != nil {
			t.Fatalf("跳闸前第 %d 次 Select 报错: %v", i, err)
		}
		done(context.Background(), selector.DoneInfo{Err: errors.New("downstream fail")})
	}
	if r.Healthy("bad") {
		t.Fatal("连续失败后 bad 应已跳闸（State=Open）")
	}

	// 硬排除：bad 跳闸后内层只能选 good。
	n, done, err := sel.Select(context.Background(), []selector.Node{bad, good})
	if err != nil {
		t.Fatal(err)
	}
	if n.Address() != "good" {
		t.Fatalf("bad 跳闸后应选 good，got %s", n.Address())
	}
	done(context.Background(), selector.DoneInfo{})

	// 睡窗过后 sentinel 的探针 Entry 经成功放行愈合（HalfOpen→Closed），bad
	// 重新可选。探针本身计一次成功样本、不加错误数，对恢复判定无干扰。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.Healthy("bad") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !r.Healthy("bad") {
		t.Fatal("睡窗过后 bad 应恢复（探针愈合，重新可选）")
	}
}

// TestWithCircuitBreakerConcurrent 在 -race 下跑并发 Select+done，钉住
// Registry 与装饰器的并发安全。无规则 sentinel 熔断器恒 Closed（State() 对
// 无规则资源直接短路，不发探针），把并发压力集中在 Registry/装饰器本身。
func TestWithCircuitBreakerConcurrent(t *testing.T) {
	ensureSentinel(t)
	r := NewRegistry(func(string) cb.CircuitBreaker {
		return wbsentinel.New(fmt.Sprintf("selector-cb.concurrent.%d", cbResSeq.Add(1)))
	})
	defer r.Close()
	sel := WithCircuitBreaker(roundrobin.New(), r)
	nodes := []selector.Node{node("a"), node("b"), node("c")}

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				n, done, err := sel.Select(context.Background(), nodes)
				if err != nil {
					continue
				}
				_ = n
				done(context.Background(), selector.DoneInfo{Err: errors.New("x")})
			}
		}()
	}
	wg.Wait()
}

// TestWithCircuitBreaker_FailureClassification 复现 AR-18：DoneInfo 的失败
// 判定取契约单一来源（cb.IsFailureError）——此前任何错误都记 MarkFailure，
// 健康节点会被业务 4xx / 客户端撤单硬排除。非失败类错误按「完成」记
// MarkSuccess（统计窗要求每次放行恰有一次 Mark* 归还）。
func TestWithCircuitBreaker_FailureClassification(t *testing.T) {
	for _, c := range []struct {
		name        string
		err         error
		wantFailure bool
	}{
		{"原生未知错误（服务侧）", errors.New("boom"), true},
		{"berrors Unavailable", berrors.New(berrors.CodeUnavailable, "R"), true},
		{"context.Canceled（客户端撤单）", context.Canceled, false},
		{"berrors NotFound（4xx 不算失败）", berrors.New(berrors.CodeNotFound, "R"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fb := &fakeBreaker{}
			r := NewRegistry(func(string) cb.CircuitBreaker { return fb })
			sel := WithCircuitBreaker(firstSelector{}, r)

			_, done, err := sel.Select(context.Background(), []selector.Node{node("a")})
			if err != nil {
				t.Fatal(err)
			}
			done(context.Background(), selector.DoneInfo{Err: c.err})

			success, failure := fb.counts()
			if c.wantFailure && failure != 1 {
				t.Fatalf("failure = %d, want 1（服务侧失败必须记失败）", failure)
			}
			if !c.wantFailure && (failure != 0 || success != 1) {
				t.Fatalf("success=%d failure=%d, want 1/0（非失败类按完成记）", success, failure)
			}
		})
	}
}
