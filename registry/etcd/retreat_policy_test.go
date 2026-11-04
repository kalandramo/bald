package etcd

import (
	"testing"
	"time"

	"github.com/kalandramo/bald/retry"
)

// TestRetreatPolicy_DefaultMatchesRetreatDelay 钉住「默认策略」这件事：
// retreatDelay 与 defaultRetreatPolicy 必须是同一条曲线，否则 heartBeat 里
// 实际生效的等待和 retreat_test.go 断言的等待会长成两样。
func TestRetreatPolicy_DefaultMatchesRetreatDelay(t *testing.T) {
	rngs := []func() float64{
		func() float64 { return 0 },
		func() float64 { return 0.37 },
		func() float64 { return 1 },
	}
	for attempt := 0; attempt < 20; attempt++ {
		for _, rng := range rngs {
			def := defaultRetreatPolicy().delay(attempt, rng)
			want := retreatDelay(attempt, rng)
			if def != want {
				t.Fatalf("attempt=%d: 默认策略给 %v, retreatDelay 给 %v", attempt, def, want)
			}
		}
	}
}

// TestRetreatPolicy_NilHalvesFallBackToDefault 「只覆盖一半」的形状：
// WithRetreat 传 nil 曲线/nil 抖动时，被覆盖的那一半照常生效、没覆盖的那一半
// 仍是默认。契约段里只写 curve、或只写 jitter，落下来的就是这两种组合。
func TestRetreatPolicy_NilHalvesFallBackToDefault(t *testing.T) {
	upper := func() float64 { return 1 }
	curveOnly := newOptions(WithRetreat(retry.FixedBackoff(250*time.Millisecond), nil))
	jitterOnly := newOptions(WithRetreat(nil, retry.EqualJitter))

	if got := curveOnly.retreat.delay(4, upper); got != 250*time.Millisecond {
		t.Errorf("只覆盖曲线：attempt=4 得 %v, want 250ms（恒定）", got)
	}
	// 曲线仍是默认（1s 起翻倍 → attempt=4 是 16s），抖动换成 Equal：
	// EqualJitter 保一半下界，rng=0 时应恰好是 8s，而默认 FullJitter 会给 0。
	if got := jitterOnly.retreat.delay(4, func() float64 { return 0 }); got != 8*time.Second {
		t.Errorf("只覆盖抖动：rng=0 时得 %v, want 8s（默认 FullJitter 会给 0）", got)
	}
	// 不传任何退避 Option 时必须仍是默认形状，而不是塌成 nil 曲线。
	if got := newOptions().retreat.delay(0, upper); got != time.Second {
		t.Errorf("未配置退避：attempt=0 得 %v, want 1s", got)
	}
}
