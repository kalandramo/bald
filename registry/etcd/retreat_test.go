package etcd

import (
	"testing"
	"time"
)

// TestRetreatDelay_CurveAndCap 钉住退避曲线本身：rng=1 时 FullJitter 恰好返回
// 入参，所以这一组断言测的是「抖动前的目标时长」——1s 起、每次翻倍、截到 cap。
//
// 后两条（attempt 33/40/63）是这次改动真正的位置：原先是 1<<attempt 秒直接乘
// time.Second，attempt=33 算出 272 年、≥34 起溢出成**负** duration（time.After
// 立刻返回，退避反转成忙轮询）。两条都由契约的 registry.etcd.max_retry 可达。
func TestRetreatDelay_CurveAndCap(t *testing.T) {
	upper := func() float64 { return 1 }

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Second},
		{1, 2 * time.Second},
		{4, 16 * time.Second},
		{8, 256 * time.Second},
		// 2^9=512s 越过 5min，此后一律停在 cap
		{9, defaultRetreatMax},
		{20, defaultRetreatMax},
		{33, defaultRetreatMax},
		{34, defaultRetreatMax}, // 旧写法在这条上溢出成负数
		{63, defaultRetreatMax},
		{1000, defaultRetreatMax},
	}
	for _, c := range cases {
		if got := retreatDelay(c.attempt, upper); got != c.want {
			t.Fatalf("attempt=%d: retreatDelay=%v, want %v", c.attempt, got, c.want)
		}
	}
}

// TestRetreatDelay_ZeroAtLowerBound 下界：rng=0 时 FullJitter 给 0。
// 这是刻意选择的口径（打散恢复风暴），代价是低 attempt 可能近乎立刻再试一次，
// 上限仍由 maxRetry 决定，不会变成无限忙轮询。
func TestRetreatDelay_ZeroAtLowerBound(t *testing.T) {
	if got := retreatDelay(4, func() float64 { return 0 }); got != 0 {
		t.Fatalf("rng=0: retreatDelay=%v, want 0", got)
	}
}

// TestRetreatDelay_StaysWithinCurve 扫 attempt × 一组固定 rng，断言
// 0 ≤ delay ≤ min(2^attempt·1s, cap)，且永不为负。期望区间独立算一遍，
// 不复用实现里的曲线，曲线被换掉时能判红。
func TestRetreatDelay_StaysWithinCurve(t *testing.T) {
	rngs := []func() float64{
		func() float64 { return 0 },
		func() float64 { return 0.25 },
		func() float64 { return 0.5 },
		func() float64 { return 0.999999 },
	}
	for attempt := 0; attempt < 40; attempt++ {
		// 先在「秒」域判顶，再乘 time.Second：反过来会在高位数上溢出成一个
		// 看起来合法的小正数，把这条判据 itself 跑成假绿。
		sec := int64(1) << uint(attempt)
		want := defaultRetreatMax
		if sec <= int64(defaultRetreatMax/time.Second) {
			want = time.Duration(sec) * time.Second
		}
		for _, rng := range rngs {
			got := retreatDelay(attempt, rng)
			if got < 0 || got > want {
				t.Fatalf("attempt=%d: retreatDelay=%v 不在 [0, %v] 内", attempt, got, want)
			}
		}
	}
}

// TestRetreatDelay_MonotonicUntilCap 抖动前的曲线单调不减：退避不会在
// 连续失败中途「松一口」，那是档位均匀取样原有的形状（每次等概率可取到 1s）。
func TestRetreatDelay_MonotonicUntilCap(t *testing.T) {
	upper := func() float64 { return 1 }
	prev := time.Duration(0)
	for attempt := 0; attempt < 15; attempt++ {
		got := retreatDelay(attempt, upper)
		if got < prev {
			t.Fatalf("attempt=%d: 退避 %v 小于上一档 %v", attempt, got, prev)
		}
		prev = got
	}
}
