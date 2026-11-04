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

// --- 滑窗记账：旋转按桶号差算，不按「距上次旋转的时长」算 ---

// TestSlidingWindow_SameBucketSamplesAreNotLost 钉住 rotateLocked 的记账基准。
//
// 缺陷形状（修之前这条判据红）：桶边界用「距上一次旋转的时长 / 桶长」来数，而
// 桶号本身是绝对时间取模——两个基准在「创建时刻落在桶中间」的相位下会错位。于是
// 两次样本明明落在同一个桶里，第二发仍算出 steps>=1，清掉的正是第一发刚写进去的
// 那个桶：样本被抹掉，滑窗系统性少计，min_requests 永远凑不满——**该开窗时不开**。
// 低流量服务（样本间隔≈桶长）首当其冲，而它正是 min_requests 这道地板要保护的。
//
// 时间轴（bucket = window/bucketCount = 500ms，创建时刻 c 落在桶 B-1 的中点）：
//
//	B-1: ………………c(1/2)
//	B  : t1(1/4) …… t2(3/4)
//
//	t1 - c = 3/4·bucket <  bucket ⇒ 第一发不旋转，样本写进 B
//	t2 - c = 5/4·bucket >= bucket ⇒ 旧实现 steps=1，清掉 B（连同第一发）
//
// 修完后按桶号差算：t1、t2 同在 B ⇒ steps=0 ⇒ 不旋转 ⇒ 两发都在 ⇒ totalReqs=2
// 达到门槛 ⇒ 开窗。落点锚在绝对格点上（本机 time.Sleep 过冲实测约 2ms，两侧各留
// 1/4 桶 = 125ms），并且下面显式自检相位——相位没摆上就报「构造失败」，不许它
// 假装通过。
func TestSlidingWindow_SameBucketSamplesAreNotLost(t *testing.T) {
	const (
		window      = 5 * time.Second
		bucketCount = 10
		bucket      = window / bucketCount // 500ms
	)

	// 先睡到格中点、**再**创建：lastRotate 锚在 c 才构成「创建落在桶中间」这个缺陷
	// 相位。先创建再对齐会让第一发自己吸收旋转，判据退化成假绿——实测踩过。
	time.Sleep(time.Duration(int64(bucket) - time.Now().UnixNano()%int64(bucket) + int64(bucket)/2))
	created := time.Now()

	b := New(
		WithRequestVolumeThreshold(2),
		WithErrorThreshold(0.5),
		WithSleepWindow(time.Hour), // 判据期间不许过半开
		WithWindow(window),
		WithBucketCount(bucketCount),
	)
	defer b.Close()

	nextStart := ((created.UnixNano() / int64(bucket)) + 1) * int64(bucket)
	time.Sleep(time.Until(time.Unix(0, nextStart+int64(bucket)/4)))
	t1 := time.Now()
	b.MarkFailure()

	time.Sleep(time.Until(time.Unix(0, nextStart+3*int64(bucket)/4)))
	t2 := time.Now()

	// 相位自检：这条测的是「同桶两发」，落点不对就没有结论可言。
	if t1.Sub(created) >= bucket || t2.Sub(created) < bucket {
		t.Fatalf("相位没摆上：t1-c=%v 需 <bucket、t2-c=%v 需 >=bucket（bucket=%v）——构造失败，不是实现通过",
			t1.Sub(created), t2.Sub(created), bucket)
	}
	if t1.UnixNano()/int64(bucket) != t2.UnixNano()/int64(bucket) {
		t.Fatalf("相位没摆上：两发落在不同的桶（%d vs %d）——构造失败，不是实现通过",
			t1.UnixNano()/int64(bucket), t2.UnixNano()/int64(bucket))
	}

	b.MarkFailure()

	if got := b.State(); got != circuitbreaker.StateOpen {
		t.Fatalf("同一桶内两次失败（错误率 100%%、min_requests=2）没有开窗，state=%v —— "+
			"第二发的旋转按「距上次旋转的时长」数边界，抹掉了同桶内第一发的样本", got)
	}
}

// TestSlidingWindow_BucketAgingIsGradual 钉住旋转清桶的范围是 (lastIdx, cur]：
// 跨过的格清空，上一发样本所在的格要等它自己出窗才清。
//
// 3 发失败（未达门槛 4）→ 隔 350ms（窗 1s / 10 桶 ⇒ 跨 3 格，仍在一窗内）报一发
// 成功：此时总数 4、错误率 3/4 ⇒ **应当**开窗。若实现把整段历史一起清掉，这里会
// 读到 totalReqs=1 而不开窗；若它一格都不清，则 1s 后那条也清不掉（见下条）。
func TestSlidingWindow_BucketAgingIsGradual(t *testing.T) {
	b := New(
		WithRequestVolumeThreshold(4),
		WithErrorThreshold(0.5),
		WithSleepWindow(time.Hour),
		WithWindow(time.Second), // 桶 = 100ms
		WithBucketCount(10),
	)
	defer b.Close()

	for i := 0; i < 3; i++ {
		b.MarkFailure()
	}
	time.Sleep(350 * time.Millisecond)
	b.MarkSuccess()

	if got := b.State(); got != circuitbreaker.StateOpen {
		t.Fatalf("3 失败 + 1 成功（350ms 前）没有开窗，state=%v —— 一窗内的样本被旋转提前清掉了", got)
	}
}

// TestSlidingWindow_FullWindowGapResets 钉住另一头：整窗没有样本时，旧桶全部过期，
// 一发成功不该把熔断判开。
//
// 上一条的 3 发失败如果不清，这里就是 4 样本 / 75% 错误率 ⇒ 开窗；隔过一整个窗
// （1.05s > 1s）再报成功，正确结果是总数 1、保持 Closed。
func TestSlidingWindow_FullWindowGapResets(t *testing.T) {
	b := New(
		WithRequestVolumeThreshold(4),
		WithErrorThreshold(0.5),
		WithSleepWindow(time.Hour),
		WithWindow(time.Second),
		WithBucketCount(10),
	)
	defer b.Close()

	for i := 0; i < 3; i++ {
		b.MarkFailure()
	}
	time.Sleep(1050 * time.Millisecond)
	b.MarkSuccess()

	if got := b.State(); got != circuitbreaker.StateClosed {
		t.Fatalf("空了一整个窗之后一发成功仍判开窗，state=%v —— 过期样本没被清掉", got)
	}
}

// TestSlidingWindow_ExpiredBucketLeavesOnPartialRotation 钉住 (lastIdx, cur] 这段
// **真的在清**：样本一格一格推进时，走满一圈的那个格必须离开窗口。
//
// 上面两条各管一头（同桶不丢、整窗过期清空），中间这段「不足一窗但跨过一圈」是
// 空档：把清桶循环改成什么都不清（steps=0）或少清一格，两条都照样绿——
// FullWindowGapResets 走的是 >= window 的整窗 reset 分支，根本不经过这里。
//
// 时间轴（bucket=200ms、一圈 6 格；落点全在格中点）：
//
//	格0：4 发失败            ← 这批就是要过期的
//	格1..格5：各 1 发成功     ← 把 lastIdx 推到格0 的前一格；总数 5..9 < 10 不触发
//	格6：1 发失败            ← 格6 ≡ 格0：旋转必须清掉格0 那 4 发 ⇒ 总数 6 < 10 ⇒ Closed
//	格6：再 4 发失败          ← 同格 steps=0 不清 ⇒ 总数 10、错误率 5/10 ⇒ Open
//
// 两道断言方向相反，一条变异杀不掉：
//   - 少清/不清（steps=0、off-by-one）⇒ 格0 的 4 发陈年失败仍算 ⇒ 格6 第一发就是
//     10 样本 / 5 错误 ⇒ **提前开窗**，第一条红。
//   - 清得过火（每次旋转清空整窗）⇒ 格1..格5 的 5 发成功被顺手抹掉 ⇒ 格6 攒不满
//     门槛 ⇒ 第二条红。
func TestSlidingWindow_ExpiredBucketLeavesOnPartialRotation(t *testing.T) {
	const (
		window      = 1200 * time.Millisecond
		bucketCount = 6
		bucket      = window / bucketCount // 200ms
		staleFails  = 4                    // 格0：注定要过期的一批
		inWindowOKs = 5                    // 格1..格5：推进 lastIdx，同时留在窗内
		volume      = staleFails + inWindowOKs + 1
	)

	// 先对齐到格中点再创建，与 SameBucketSamplesAreNotLost 同因：创建时刻的相位
	// 决定第一发会不会自己吸收旋转。
	time.Sleep(time.Duration(int64(bucket) - time.Now().UnixNano()%int64(bucket) + int64(bucket)/2))
	grid := time.Now().UnixNano() / int64(bucket)

	b := New(
		WithRequestVolumeThreshold(volume),
		WithErrorThreshold(0.5),
		WithSleepWindow(time.Hour), // 判据期间不许过半开
		WithWindow(window),
		WithBucketCount(bucketCount),
	)
	defer b.Close()

	// sleepTo 睡到「第 i 格的中点」，并核对确实落在那一格——落点不对就没有结论可言。
	sleepTo := func(i int) {
		t.Helper()
		time.Sleep(time.Until(time.Unix(0, (grid+int64(i))*int64(bucket)+int64(bucket)/2)))
		if got := time.Now().UnixNano() / int64(bucket); got != grid+int64(i) {
			t.Fatalf("相位没摆上：第 %d 发落在绝对桶 %d，期望 %d（bucket=%v）——构造失败，不是实现通过",
				i, got, grid+int64(i), bucket)
		}
	}

	sleepTo(0)
	for i := 0; i < staleFails; i++ {
		b.MarkFailure()
	}
	for i := 1; i <= inWindowOKs; i++ {
		sleepTo(i)
		b.MarkSuccess()
	}

	// 走满一圈：这一发的旋转要清掉格0（≡ 本格的绝对桶）。
	sleepTo(6)
	b.MarkFailure()
	if got := b.State(); got != circuitbreaker.StateClosed {
		t.Fatalf("转满一圈后格 0 的 %d 发陈旧失败仍算在窗内（总量 %d、错误率 5/10）⇒ 提前开窗，"+
			"state=%v —— (lastIdx, cur] 这段没在清桶", staleFails, staleFails+inWindowOKs+1, got)
	}

	// 同一格再补 4 发：总数正好踩到门槛 10、错误率 5/10 —— 窗内那 5 发成功必须还在。
	for i := 0; i < staleFails; i++ {
		b.MarkFailure()
	}
	if got := b.State(); got != circuitbreaker.StateOpen {
		t.Fatalf("窗内明明有 %d 发成功 + %d 发失败（总量 %d = 门槛、错误率 50%%）却没开窗，"+
			"state=%v —— 旋转把一窗内未过期的格一起清了", inWindowOKs, inWindowOKs, volume, got)
	}
}

// --- 半开态：转移与探针（fallthrough 自拒会永久锁死）---

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
