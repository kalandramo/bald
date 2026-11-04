package sres

import (
	"context"
	"errors"
	"testing"

	"github.com/kalandramo/bald/circuitbreaker"
)

// ---------------------------------------------------------------------------
// New / options
// ---------------------------------------------------------------------------

func TestNew_DefaultConfig(t *testing.T) {
	b := New()
	defer b.Close()
	if b == nil {
		t.Fatal("New() returned nil")
	}
	if b.cfg.k != defaultK {
		t.Errorf("default K = %v, want %v", b.cfg.k, defaultK)
	}
	if b.cfg.window != defaultWindow {
		t.Errorf("default window = %v, want %v", b.cfg.window, defaultWindow)
	}
}

func TestNew_WithK(t *testing.T) {
	b := New(WithK(3.0))
	defer b.Close()
	if b.cfg.k != 3.0 {
		t.Errorf("K = %v, want 3.0", b.cfg.k)
	}
}

func TestNew_WithK_InvalidIgnored(t *testing.T) {
	b := New(WithK(-1))
	defer b.Close()
	if b.cfg.k != defaultK {
		t.Errorf("invalid K should be ignored, got %v, want %v", b.cfg.k, defaultK)
	}
}

func TestNew_WithWindow(t *testing.T) {
	d := defaultWindow * 2
	b := New(WithWindow(d))
	defer b.Close()
	if b.cfg.window != d {
		t.Errorf("window = %v, want %v", b.cfg.window, d)
	}
}

func TestNew_WithBucketCount(t *testing.T) {
	b := New(WithBucketCount(20))
	defer b.Close()
	if b.cfg.bucketCount != 20 {
		t.Errorf("bucketCount = %v, want 20", b.cfg.bucketCount)
	}
	if len(b.buckets) != 20 {
		t.Errorf("len(buckets) = %d, want 20", len(b.buckets))
	}
}

// ---------------------------------------------------------------------------
// Allow — initially closed (allows all)
// ---------------------------------------------------------------------------

func TestAllow_InitiallyAllows(t *testing.T) {
	b := New()
	defer b.Close()
	if err := b.Allow(); err != nil {
		t.Errorf("Allow() = %v, want nil on a fresh breaker", err)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestClose_RejectsAfterClose(t *testing.T) {
	b := New()
	_ = b.Close()
	if err := b.Allow(); !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Errorf("Allow() after Close = %v, want ErrCircuitOpen", err)
	}
}

// ---------------------------------------------------------------------------
// Execute — success path
// ---------------------------------------------------------------------------

func TestExecute_Success(t *testing.T) {
	b := New()
	defer b.Close()

	called := false
	err := b.Execute(context.Background(), func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Errorf("Execute() = %v, want nil", err)
	}
	if !called {
		t.Error("fn was not called")
	}
}

func TestExecute_FailureRecorded(t *testing.T) {
	b := New()
	defer b.Close()

	wantErr := errors.New("downstream error")
	err := b.Execute(context.Background(), func() error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("Execute() = %v, want %v", err, wantErr)
	}
}

// ---------------------------------------------------------------------------
// Execute — high failure rate triggers throttling
// ---------------------------------------------------------------------------

func TestExecute_HighFailureRateThrottles(t *testing.T) {
	// Use K=1 so the breaker is very aggressive (trips when error rate ~100%)
	b := New(WithK(1.0), WithWindow(60*1e9), WithBucketCount(10))
	defer b.Close()

	totalReq := 200
	rejected := 0

	for i := 0; i < totalReq; i++ {
		err := b.Execute(context.Background(), func() error {
			return errors.New("always fails")
		})
		if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			rejected++
		}
	}

	if rejected == 0 {
		t.Error("expected some requests to be rejected due to high failure rate, got 0 rejections")
	}
	t.Logf("rejected %d/%d requests under sustained failures", rejected, totalReq)
}

// TestExecute_AllSuccessNotThrottled 钉「全成功不把熔断打开」——即成功必须记成
// 成功，不能被当成错误。
//
// 阈值出处（冷窗口的拒绝是**自我强化**的：被拒不往 requests 里计数，同一个低
// accept 会被重复采样，所以左尾比独立二项式厚得多，不能按「90% 离均值很远」估）：
//   - total=200 时按精确 DP（下面这条注释所述过程，逐步 r→r+1 概率 r/(r+1)）
//     均值 194.16，P(放行 ≤179)＝8.4e-5 ＝ 约每 1.2e4 次红一次。旧判据
//     `allowed < total*9/10` 正落在这个尾巴上：本机 `-race -count=3000` 实测
//     最低 178（2 次红）、均值 194.21（与 DP 的 194.16 吻合），CI 的 race 作业
//     实际红过一次（run 41806067800：`allowed 179/200`）。
//   - 保留「绝大多数放行」这条语义（不降比例）而是加大样本：total=500、比例仍
//     取 90% ⇒ 阈值 450，同一 DP 下 P(放行 ≤449)＝2.0e-13（约 5e12 次跑一红）、
//     均值 493.22。
//
// 牙齿：把 `MarkSuccess` 改成同时 `errors++` 使 accept=(r-2r)/(r+1)<0 ⇒ 放行恒为
// 1（首次 requests=0 的早退分支），红；把分母 `requests+1` 改成 `2*requests+1`
// 使 accept≈1/2 ⇒ 放行约 250/500，红。
func TestExecute_AllSuccessNotThrottled(t *testing.T) {
	const (
		total      = 500
		minAllowed = total * 9 / 10
	)
	b := New()
	defer b.Close()

	allowed := 0
	for i := 0; i < total; i++ {
		err := b.Execute(context.Background(), func() error { return nil })
		if err == nil {
			allowed++
		}
	}
	if allowed < minAllowed {
		t.Errorf("allowed %d/%d — expected at least 90%%", allowed, total)
	}
	t.Logf("allowed %d/%d under all-success", allowed, total)
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

func TestState_NoDataIsClosed(t *testing.T) {
	b := New()
	defer b.Close()
	if s := b.State(); s != circuitbreaker.StateClosed {
		t.Errorf("State() = %v, want StateClosed", s)
	}
}

func TestState_AllFailuresBecomesOpen(t *testing.T) {
	b := New(WithK(1.0))
	defer b.Close()

	// Generate enough failures.
	for i := 0; i < 50; i++ {
		_ = b.Execute(context.Background(), func() error { return errors.New("fail") })
	}

	s := b.State()
	if s != circuitbreaker.StateOpen {
		t.Errorf("State() = %v, want StateOpen after sustained failures", s)
	}
}

// ---------------------------------------------------------------------------
// MarkSuccess / MarkFailure directly
// ---------------------------------------------------------------------------

func TestMarkSuccessAndFailure(t *testing.T) {
	b := New()
	defer b.Close()

	// Call Allow + MarkSuccess repeatedly. Allow may reject probabilistically,
	// so we only MarkSuccess when Allow returns nil.
	for i := 0; i < 10; i++ {
		if err := b.Allow(); err == nil {
			b.MarkSuccess()
		}
	}

	// All-success should never produce StateOpen.
	if s := b.State(); s == circuitbreaker.StateOpen {
		t.Errorf("State() = StateOpen after all successes — should not be open")
	}
}

// ---------------------------------------------------------------------------
// Concurrent access
// ---------------------------------------------------------------------------

func TestConcurrentExecute(t *testing.T) {
	b := New()
	defer b.Close()

	// 先灌满窗口：accept = requests/(requests+1) 里的 requests 是**已 MarkSuccess
	// 的条数**，不 prime 的话早期每一次 Allow 都落在 requests=0..3 上，单次拒绝率
	// 高达 1/2，放行数就变成 Allow 与 Mark 交错次序的函数——实测本机 -count=400
	// 最低放行 87（旧断言「≥90」红 15 次＝3.75%），CI 也因它红过一次
	// （run 41388597082 的 test 作业）。prime 之后单次拒绝率上界恰为 1/1001，且
	// MarkSuccess 只会压低它、窗口轮转清空计数只会把 accept 抬回 1，所以「≥90」
	// 成了一个有界的断言：100 次里要 ≥11 次拒绝才红，按 p=1/1001 的二项尾概率
	// 约 1.3e-19。本机 -count=1000 实测放行分布 98..100（最低 98）。
	for i := 0; i < 1000; i++ {
		b.MarkSuccess()
	}

	ctx := context.Background()
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		go func() {
			errs <- b.Execute(ctx, func() error { return nil })
		}()
	}
	var allowed int
	for i := 0; i < 100; i++ {
		err := <-errs
		switch {
		case err == nil:
			allowed++
		case errors.Is(err, circuitbreaker.ErrCircuitOpen):
			// 概率性拒绝是 SRE 的既定行为
		default:
			t.Errorf("concurrent Execute() 返回非预期错误：%v", err)
		}
	}
	t.Logf("并发放行 = %d/100", allowed)
	if allowed < 90 {
		t.Errorf("健康窗口并发 Execute 放行 = %d/100, want >= 90（单次拒绝率应 ≤ 1/1001）", allowed)
	}
}

// TestConcurrentExecuteRejectedErrorKind 在「必然被拒」的状态下并发跑，钉住错
// 型：K=2 时全失败使 accept=(requests-2*errors)/(requests+1)<0，每个 Execute 都
// 必须以 ErrCircuitOpen 失败。这条不能挂在 TestConcurrentExecute 上——prime 后
// 拒绝率 ≤1/1001，100 个 goroutine 大概率一次都不被拒（变异「拒绝改返回别的错误」
// 实测能溜过去）。
func TestConcurrentExecuteRejectedErrorKind(t *testing.T) {
	b := New()
	defer b.Close()

	for i := 0; i < 10; i++ {
		b.MarkFailure()
	}
	if s := b.State(); s != circuitbreaker.StateOpen {
		t.Fatalf("灌满失败后 State() = %v，期望 Open（accept<=0）", s)
	}

	ctx := context.Background()
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		go func() {
			errs <- b.Execute(ctx, func() error { return nil })
		}()
	}
	for i := 0; i < 100; i++ {
		if err := <-errs; !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			t.Fatalf("并发 Execute 在 accept=0 的窗口返回 %v，want ErrCircuitOpen", err)
		}
	}
}

// TestHealthyWindowAcceptanceNearOne 把「健康窗口几乎全放行」变成可断言的性
// 质。先灌满 prime 条成功：此时单次拒绝概率恰为 1/(prime+1)，而 Allow 不往窗口
// 里计数（prime 不会掉），所以 calls 次调用是独立的 Bernoulli(1/1001)，拒绝数
// R≈Poisson(λ=2000/1001≈2.0)。
// 阈值出处：minPass=1985 即容忍 R≤15，按该分布 P(R≥16)≈4.7e-10。本机
// -count=1000 实测 R 落在 0..7、均值 2.06（分布 164/274/265/166/82/36/7/6），
// 与 λ=2 的 Poisson 吻合；最低放行 1993，样本外余量 8。这条仍然有牙：把公式
// 分母的 +1 改成 +100（单次拒绝率升到 1/11）实测放行 1816/2000，红。
func TestHealthyWindowAcceptanceNearOne(t *testing.T) {
	const (
		prime   = 1000
		calls   = 2000
		minPass = 1985
	)
	b := New()
	defer b.Close()

	for i := 0; i < prime; i++ {
		b.MarkSuccess()
	}
	// SRE 的 accept 对 requests>0 恒 <1，故灌满后必然是 HalfOpen。
	if s := b.State(); s != circuitbreaker.StateHalfOpen {
		t.Fatalf("prime 后 State() = %v，期望 HalfOpen", s)
	}

	var allowed int
	for i := 0; i < calls; i++ {
		if b.Allow() == nil {
			allowed++
		}
	}
	t.Logf("放行 = %d/%d（阈值 %d）", allowed, calls, minPass)
	if allowed < minPass {
		t.Errorf("健康窗口放行 = %d/%d，want >= %d（单次拒绝率应为 1/%d）", allowed, calls, minPass, prime+1)
	}
}
