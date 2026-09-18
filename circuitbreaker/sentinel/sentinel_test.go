package sentinel

import (
	"context"
	"errors"
	"sync"
	"testing"

	sentinelapi "github.com/alibaba/sentinel-golang/api"
	scb "github.com/alibaba/sentinel-golang/core/circuitbreaker"

	windcb "github.com/kalandramo/bald/circuitbreaker"
)

// 测试资源名各自独立，避免 Sentinel 的全局规则表互相干扰。
// Sentinel 的 InitDefault 是进程级单例，用 sync.Once 保证只初始化一次。
var initOnce sync.Once

func ensureInit(t *testing.T) {
	t.Helper()
	var err error
	initOnce.Do(func() { err = sentinelapi.InitDefault() })
	if err != nil {
		t.Fatalf("sentinelapi.InitDefault: %v", err)
	}
}

// loadErrorRatioRule 为一个资源装载「错误比例」熔断规则。
func loadErrorRatioRule(t *testing.T, resource string, threshold float64) {
	t.Helper()
	if _, err := scb.LoadRules([]*scb.Rule{{
		Resource:         resource,
		Strategy:         scb.ErrorRatio,
		Threshold:        threshold,
		RetryTimeoutMs:   1000,
		MinRequestAmount: 5,
		StatIntervalMs:   1000,
	}}); err != nil {
		t.Fatalf("LoadRules(%s): %v", resource, err)
	}
}

// --- 契约形状 ---

func TestImplementsCircuitBreaker(t *testing.T) {
	var _ windcb.CircuitBreaker = New("shape-check")
}

// --- 构造与配置 ---

func TestNew_DefaultTrafficType(t *testing.T) {
	b := New("cfg-default")
	// 熔断保护出站调用，默认应为 Outbound（与 ratelimit/sentinel 的 Inbound 相反）。
	if b.cfg.trafficType.String() != "Outbound" {
		t.Errorf("default trafficType = %v, want Outbound", b.cfg.trafficType)
	}
}

func TestNew_WithTrafficType(t *testing.T) {
	tt := New("cfg-tt", WithTrafficType(0)) // 0 == base.Inbound
	if tt.cfg.trafficType.String() != "Inbound" {
		t.Errorf("trafficType = %v, want Inbound", tt.cfg.trafficType)
	}
}

// --- 无规则时全放行 ---

func TestAllow_NoRulesAllows(t *testing.T) {
	ensureInit(t)
	b := New("no-rules-resource")
	defer b.Close()

	if err := b.Allow(); err != nil {
		t.Fatalf("Allow() without rules = %v, want nil", err)
	}
	b.MarkSuccess()
}

func TestState_NoRulesIsClosed(t *testing.T) {
	ensureInit(t)
	b := New("state-no-rules")
	defer b.Close()
	if s := b.State(); s != windcb.StateClosed {
		t.Errorf("State() without rules = %v, want Closed", s)
	}
}

// --- Execute 生命周期 ---

func TestExecute_Success(t *testing.T) {
	ensureInit(t)
	b := New("exec-success")
	defer b.Close()

	called := false
	if err := b.Execute(context.Background(), func() error { called = true; return nil }); err != nil {
		t.Errorf("Execute() = %v, want nil", err)
	}
	if !called {
		t.Error("fn was not called")
	}
}

func TestExecute_FailurePropagated(t *testing.T) {
	ensureInit(t)
	b := New("exec-failure")
	defer b.Close()

	want := errors.New("downstream")
	if err := b.Execute(context.Background(), func() error { return want }); !errors.Is(err, want) {
		t.Errorf("Execute() = %v, want %v", err, want)
	}
}

// --- 熔断生效（装载规则后持续失败应被拦）---

func TestExecute_TripsOnSustainedFailures(t *testing.T) {
	ensureInit(t)
	const resource = "trip-resource"
	loadErrorRatioRule(t, resource, 0.5)
	b := New(resource)
	defer b.Close()

	blocked := false
	for i := 0; i < 100; i++ {
		err := b.Execute(context.Background(), func() error { return errors.New("fail") })
		if errors.Is(err, windcb.ErrCircuitOpen) {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatal("circuit must trip after sustained failures once a rule is loaded")
	}
}

// --- entry 配对不变量（回归：entry 单字段在并发下被覆盖而泄漏）---

// 每个 Allow 成功的请求都必须恰好释放一个 entry：并发配对后待处理栈必须清空。
func TestEntryPairing_NoLeakUnderConcurrency(t *testing.T) {
	ensureInit(t)
	b := New("pairing-resource")
	defer b.Close()

	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := b.Allow(); err != nil {
				return
			}
			if i%2 == 0 {
				b.MarkSuccess()
			} else {
				b.MarkFailure()
			}
		}(i)
	}
	wg.Wait()

	b.mu.Lock()
	leaked := len(b.pending)
	b.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("pending entries leaked after paired Allow/Mark under concurrency: %d (each leak holds a Sentinel slot forever)", leaked)
	}
}

// Mark* 在没有待处理 entry 时必须是安全的 no-op（不能 panic、不能误关别人的 entry）。
func TestMark_WithoutAllowIsSafe(t *testing.T) {
	ensureInit(t)
	b := New("mark-without-allow")
	defer b.Close()

	b.MarkSuccess() // 不应 panic
	b.MarkFailure() // 不应 panic

	b.mu.Lock()
	n := len(b.pending)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("Mark* without Allow must not fabricate pending entries, got %d", n)
	}
}

// --- Close ---

func TestClose_IsNoOp(t *testing.T) {
	ensureInit(t)
	b := New("close-noop")
	if err := b.Close(); err != nil {
		t.Errorf("Close() = %v, want nil (Sentinel manages its own lifecycle)", err)
	}
	// Close 是 no-op —— 之后 Allow 仍工作（文档已声明该语义）。
	if err := b.Allow(); err != nil {
		t.Errorf("Allow() after Close = %v, want nil (Close is a documented no-op)", err)
	}
	b.MarkSuccess()
}
