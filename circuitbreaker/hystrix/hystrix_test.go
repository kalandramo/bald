package hystrix

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kalandramo/bald/circuitbreaker"
)

// 测试用的小窗口参数：sleepWindow 取 50ms（Windows 下 time.Sleep 粒度约 15ms，
// 留足余量）；window 取 10s 保证测试期间的桶不旋转、计数不丢。
const (
	testVolume      = 5
	testSleepWindow = 50 * time.Millisecond
	testWindow      = 10 * time.Second
)

func newTestBreaker(opts ...Option) *Breaker {
	base := []Option{
		WithRequestVolumeThreshold(testVolume),
		WithErrorThreshold(0.5),
		WithSleepWindow(testSleepWindow),
		WithWindow(testWindow),
	}
	return New(append(base, opts...)...)
}

// tripToOpen 连续上报失败直到跳闸。返回是否成功进入 Open。
func tripToOpen(t *testing.T, b *Breaker) {
	t.Helper()
	for i := 0; i < testVolume*2; i++ {
		if err := b.Allow(); err != nil {
			if b.State() == circuitbreaker.StateOpen {
				return
			}
			t.Fatalf("Allow rejected before reaching Open: %v", err)
		}
		b.MarkFailure()
	}
	t.Fatalf("breaker did not trip after %d failures (state=%v)", testVolume*2, b.State())
}

// --- 构造与配置 ---

func TestNew_Defaults(t *testing.T) {
	b := New()
	defer b.Close()
	if b.cfg.errorThreshold != defaultErrorThreshold {
		t.Errorf("errorThreshold = %v, want %v", b.cfg.errorThreshold, defaultErrorThreshold)
	}
	if b.cfg.requestVolumeThreshold != defaultRequestVolumeThreshold {
		t.Errorf("requestVolumeThreshold = %v, want %v", b.cfg.requestVolumeThreshold, defaultRequestVolumeThreshold)
	}
	if b.cfg.sleepWindow != defaultSleepWindow {
		t.Errorf("sleepWindow = %v, want %v", b.cfg.sleepWindow, defaultSleepWindow)
	}
	if s := b.State(); s != circuitbreaker.StateClosed {
		t.Errorf("fresh breaker State() = %v, want Closed", s)
	}
}

func TestNew_InvalidOptionsIgnored(t *testing.T) {
	b := New(
		WithErrorThreshold(0),   // 非法（须 > 0）
		WithErrorThreshold(1.5), // 非法（须 <= 1）
		WithRequestVolumeThreshold(0),
		WithSleepWindow(0),
		WithWindow(0),
		WithBucketCount(0),
	)
	defer b.Close()
	if b.cfg.errorThreshold != defaultErrorThreshold {
		t.Errorf("errorThreshold = %v, want default %v", b.cfg.errorThreshold, defaultErrorThreshold)
	}
	if b.cfg.requestVolumeThreshold != defaultRequestVolumeThreshold {
		t.Errorf("requestVolumeThreshold = %v, want default", b.cfg.requestVolumeThreshold)
	}
	if b.cfg.sleepWindow != defaultSleepWindow {
		t.Errorf("sleepWindow = %v, want default", b.cfg.sleepWindow)
	}
}

// --- Closed 态 ---

func TestAllow_InitiallyClosed(t *testing.T) {
	b := newTestBreaker()
	defer b.Close()
	if err := b.Allow(); err != nil {
		t.Errorf("Allow() on fresh breaker = %v, want nil", err)
	}
}

func TestExecute_Success(t *testing.T) {
	b := newTestBreaker()
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
	b := newTestBreaker()
	defer b.Close()

	want := errors.New("downstream")
	if err := b.Execute(context.Background(), func() error { return want }); !errors.Is(err, want) {
		t.Errorf("Execute() = %v, want %v", err, want)
	}
}

// --- 跳闸条件 ---

func TestTripsOnErrorRate(t *testing.T) {
	b := newTestBreaker()
	defer b.Close()
	tripToOpen(t, b)
}

func TestRequestVolumeThreshold_PreventsEarlyTrip(t *testing.T) {
	// 门槛设 10，只报 3 次失败 —— 不应跳闸。
	b := newTestBreaker(WithRequestVolumeThreshold(10))
	defer b.Close()

	for i := 0; i < 3; i++ {
		if err := b.Allow(); err != nil {
			t.Fatalf("Allow() = %v, want nil below volume threshold", err)
		}
		b.MarkFailure()
	}
	if s := b.State(); s != circuitbreaker.StateClosed {
		t.Errorf("State() = %v, want Closed (below volume threshold)", s)
	}
}

func TestErrorRateBelowThreshold_DoesNotTrip(t *testing.T) {
	// 5 请求里 2 失败 3 成功 = 40% < 50% 门槛，且量达到门槛 —— 不应跳闸。
	b := newTestBreaker(WithErrorThreshold(0.5))
	defer b.Close()

	for i := 0; i < testVolume; i++ {
		if err := b.Allow(); err != nil {
			t.Fatalf("Allow() = %v, want nil", err)
		}
		if i < 2 {
			b.MarkFailure() // 2 失败
		} else {
			b.MarkSuccess() // 3 成功
		}
	}
	if s := b.State(); s != circuitbreaker.StateClosed {
		t.Errorf("State() = %v, want Closed (2/5 = 40%% error rate, below 50%% threshold)", s)
	}
}

// --- 半开态：转移与探针（回归：曾因 fallthrough 自拒而永久锁死）---

func TestAllow_HalfOpenAdmitsProbe(t *testing.T) {
	b := newTestBreaker()
	defer b.Close()
	tripToOpen(t, b)

	time.Sleep(testSleepWindow * 3)

	// 转移点的第一个请求必须被放行（它就是要发出去的探针）。
	if err := b.Allow(); err != nil {
		t.Fatalf("first Allow after sleepWindow must be admitted as the probe, got %v", err)
	}
	if s := b.State(); s != circuitbreaker.StateHalfOpen {
		t.Errorf("State() = %v, want HalfOpen while probe in flight", s)
	}
	// 探针在飞期间，后续请求必须被拒（只放一个探针）。
	if err := b.Allow(); err == nil {
		t.Error("second Allow while probe in flight must be rejected")
	}
}

func TestExecute_RecoversAfterDownstreamHeals(t *testing.T) {
	b := newTestBreaker()
	defer b.Close()
	tripToOpen(t, b)

	time.Sleep(testSleepWindow * 3)

	// 走 Execute 路径（真实中间件用法，不主动轮询 State）。
	recovered := false
	for i := 0; i < 5; i++ {
		if err := b.Execute(context.Background(), func() error { return nil }); err == nil {
			recovered = true
			break
		}
	}
	if !recovered {
		t.Fatal("breaker must recover via Execute once the downstream heals")
	}
	if s := b.State(); s != circuitbreaker.StateClosed {
		t.Errorf("State() = %v, want Closed after successful probe", s)
	}
}

func TestMarkFailure_HalfOpenReopens(t *testing.T) {
	b := newTestBreaker()
	defer b.Close()
	tripToOpen(t, b)

	time.Sleep(testSleepWindow * 3)

	if err := b.Allow(); err != nil {
		t.Fatalf("probe Allow() = %v, want nil", err)
	}
	b.MarkFailure() // 探针失败 → 重新 Open

	if s := b.State(); s != circuitbreaker.StateOpen {
		t.Fatalf("State() = %v, want Open after failed probe", s)
	}
	// 重新 Open 后睡窗重置：立刻再 Allow 应被拒。
	if err := b.Allow(); err == nil {
		t.Error("Allow() immediately after re-open must be rejected (sleepWindow restarted)")
	}
}

// 回归核心：State() 的 lazy 转移与 Allow() 的转移必须一致，
// 不能出现「先调 State() 就放行、不调就锁死」的分叉。
func TestStateAndAllow_TransitionConsistently(t *testing.T) {
	// 路径 A：先 State() 触发 lazy 转移，再 Allow。
	a := newTestBreaker()
	defer a.Close()
	tripToOpen(t, a)
	time.Sleep(testSleepWindow * 3)
	_ = a.State() // lazy 转移到 HalfOpen
	errA := a.Allow()

	// 路径 B：直接 Allow（不调 State）。
	bb := newTestBreaker()
	defer bb.Close()
	tripToOpen(t, bb)
	time.Sleep(testSleepWindow * 3)
	errB := bb.Allow()

	if (errA == nil) != (errB == nil) {
		t.Errorf("transition diverged: State()-first Allow=%v, direct Allow=%v", errA, errB)
	}
	if errB != nil {
		t.Errorf("direct Allow() after sleepWindow = %v, want nil", errB)
	}
}

// --- Close ---

func TestClose_RejectsAfterClose(t *testing.T) {
	b := newTestBreaker()
	_ = b.Close()
	if err := b.Allow(); !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Errorf("Allow() after Close = %v, want ErrCircuitOpen", err)
	}
}

// --- 并发 ---

func TestConcurrentAllowMark(t *testing.T) {
	b := newTestBreaker(WithRequestVolumeThreshold(1000)) // 门槛拉高，避免跳闸干扰
	defer b.Close()

	done := make(chan struct{}, 50)
	for i := 0; i < 50; i++ {
		go func() {
			if err := b.Allow(); err == nil {
				b.MarkSuccess()
			}
			_ = b.State()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 50; i++ {
		<-done
	}
}
