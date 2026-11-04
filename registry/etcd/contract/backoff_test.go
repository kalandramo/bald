package contract

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

func d(v time.Duration) *durationpb.Duration { return durationpb.New(v) }

// TestRetreat_AbsentKeepsBackendDefault 段缺席必须是两个 nil + 无错：
// nil 曲线与 nil 抖动在 etcd 侧各自落回默认，这是「不配置即行为不变」的实现路径。
func TestRetreat_AbsentKeepsBackendDefault(t *testing.T) {
	b, j, err := retreat(nil)
	if err != nil {
		t.Fatalf("段缺席不该报错: %v", err)
	}
	if b != nil || j != nil {
		t.Fatalf("段缺席应返回 nil, nil，got %v / %v", b, j)
	}
}

// TestRetreat_ExponentialCurve 指数曲线的三个自由度：原点、倍率、上界。
// 未写 curve 与写 EXPONENTIAL 必须同形，未写 factor 必须按 2 取值。
func TestRetreat_ExponentialCurve(t *testing.T) {
	sec := &bootstrapv1.Backoff{Initial: d(time.Second), Factor: 3, Max: d(20 * time.Second)}
	b, j, err := retreat(sec)
	if err != nil {
		t.Fatalf("retreat: %v", err)
	}
	if j != nil {
		t.Errorf("jitter 未写应落回后端默认（nil），got %v", j)
	}
	for attempt, want := range map[int]time.Duration{
		0: time.Second,      // 原点
		1: 3 * time.Second,  // ×3
		2: 9 * time.Second,  // ×3
		3: 20 * time.Second, // 27s 越界 → 截到 max
	} {
		if got := b.Delay(attempt); got != want {
			t.Errorf("factor=3: attempt=%d 得 %v, want %v", attempt, got, want)
		}
	}

	implicit, _, err := retreat(&bootstrapv1.Backoff{Initial: d(2 * time.Second), Max: d(time.Minute)})
	if err != nil {
		t.Fatalf("未写 curve/factor: %v", err)
	}
	for attempt, want := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second} {
		if got := implicit.Delay(attempt); got != want {
			t.Errorf("默认倍率: attempt=%d 得 %v, want %v（0 应按 2 取值）", attempt, got, want)
		}
	}
	if got := implicit.Delay(60); got != time.Minute {
		t.Errorf("默认倍率长尾应停在 max，got %v", got)
	}
}

// TestRetreat_LinearCurve 线性曲线：原点 + 增量 + 上界；step 为 0 是合法形状
// （恒定等待 initial），不是配置错。
func TestRetreat_LinearCurve(t *testing.T) {
	b, _, err := retreat(&bootstrapv1.Backoff{
		Curve:   bootstrapv1.Backoff_LINEAR,
		Initial: d(500 * time.Millisecond),
		Step:    d(250 * time.Millisecond),
		Max:     d(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("retreat: %v", err)
	}
	for attempt, want := range map[int]time.Duration{
		0: 500 * time.Millisecond,
		1: 750 * time.Millisecond,
		6: 2 * time.Second, // 500+6×250=2s，正好到顶
		9: 2 * time.Second, // 2.75s 越界 → 截顶
	} {
		if got := b.Delay(attempt); got != want {
			t.Errorf("linear: attempt=%d 得 %v, want %v", attempt, got, want)
		}
	}

	flat, _, err := retreat(&bootstrapv1.Backoff{
		Curve: bootstrapv1.Backoff_LINEAR, Initial: d(time.Second), Max: d(time.Minute),
	})
	if err != nil {
		t.Fatalf("step 缺席: %v", err)
	}
	if got := flat.Delay(5); got != time.Second {
		t.Errorf("step=0 应恒为 initial，attempt=5 得 %v", got)
	}
}

// TestRetreat_FixedNeedsNoCap FIXED 忽略 max，因此这一档不要求 max——
// 「必填上界」挡住的是无上界的增长曲线，恒定等待没有那个风险。
func TestRetreat_FixedNeedsNoCap(t *testing.T) {
	b, _, err := retreat(&bootstrapv1.Backoff{
		Curve: bootstrapv1.Backoff_FIXED, Initial: d(100 * time.Millisecond),
	})
	if err != nil {
		t.Fatalf("FIXED 无 max 应合法: %v", err)
	}
	for _, attempt := range []int{0, 1, 7, 100} {
		if got := b.Delay(attempt); got != 100*time.Millisecond {
			t.Errorf("fixed: attempt=%d 得 %v, want 100ms", attempt, got)
		}
	}
}

// TestRetreat_JitterMapping 四种抖动的可观测差别，用 rng=0 这个端点区分：
// NONE 保留全值、FULL 归零、EQUAL 保一半。未写必须是 nil（沿用后端默认）。
func TestRetreat_JitterMapping(t *testing.T) {
	const base = 8 * time.Second
	zero := func() float64 { return 0 }

	for _, tc := range []struct {
		name   string
		jitter bootstrapv1.Backoff_Jitter
		want   time.Duration
	}{
		{"NONE", bootstrapv1.Backoff_NONE, base},
		{"FULL", bootstrapv1.Backoff_FULL, 0},
		{"EQUAL", bootstrapv1.Backoff_EQUAL, base / 2},
	} {
		b, j, err := retreat(&bootstrapv1.Backoff{
			Curve: bootstrapv1.Backoff_FIXED, Initial: d(base), Jitter: tc.jitter,
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if j == nil {
			t.Fatalf("%s: 显式写了抖动却返回 nil", tc.name)
		}
		if got := j(b.Delay(0), zero); got != tc.want {
			t.Errorf("%s: rng=0 时得 %v, want %v", tc.name, got, tc.want)
		}
	}

	if _, j, err := retreat(&bootstrapv1.Backoff{
		Curve: bootstrapv1.Backoff_FIXED, Initial: d(base),
	}); err != nil || j != nil {
		t.Errorf("jitter 未写应落回后端默认 nil，got %v / %v", j, err)
	}
}

// TestRetreat_RejectsUnsafeShapes 两条必填挡住的是两个反向失效形态：
// 零原点＝忙轮询，无上界指数＝高位次乘出负等待。判错要带键名，
// 否则运维只看到一层 wrapper。
func TestRetreat_RejectsUnsafeShapes(t *testing.T) {
	exp := bootstrapv1.Backoff_EXPONENTIAL
	for _, tc := range []struct {
		name string
		sec  *bootstrapv1.Backoff
		want string
	}{
		{"initial 缺席", &bootstrapv1.Backoff{Curve: exp, Max: d(time.Minute)}, "initial"},
		{"initial 为 0", &bootstrapv1.Backoff{Curve: exp, Initial: d(0), Max: d(time.Minute)}, "initial"},
		{"initial 为负", &bootstrapv1.Backoff{Curve: exp, Initial: d(-time.Second), Max: d(time.Minute)}, "initial"},
		{"max 缺席", &bootstrapv1.Backoff{Curve: exp, Initial: d(time.Second)}, "max"},
		{"max 为 0", &bootstrapv1.Backoff{Curve: exp, Initial: d(time.Second), Max: d(0)}, "max"},
		{"linear 无 max", &bootstrapv1.Backoff{Curve: bootstrapv1.Backoff_LINEAR, Initial: d(time.Second)}, "max"},
		{"linear 负 step", &bootstrapv1.Backoff{
			Curve: bootstrapv1.Backoff_LINEAR, Initial: d(time.Second), Step: d(-time.Second), Max: d(time.Minute),
		}, "step"},
	} {
		_, _, err := retreat(tc.sec)
		if err == nil {
			t.Fatalf("%s: 应判配置错，却放过了", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: 文案 %q 没点出 %q", tc.name, err, tc.want)
		}
	}
}

// TestProvider_BackoffSectionReachesOptions 判「写了读得到」直到装配那一步：
// 非法段必须在建连之前 fail-fast，合法段不该被退避这一层拦下。
//
// 合法那一半不断「Provider 整体成功」：本机/CI 的短跑没有 etcd，New 之后的
// 建连本来就可能失败。判据只要求「失败也不来自 backoff」，这样两个环境都能
// 给出同一个结论，不靠环境凑绿。
func TestProvider_BackoffSectionReachesOptions(t *testing.T) {
	ctx := context.Background()

	bad := &bootstrapv1.Registry{Type: Type, Etcd: &bootstrapv1.Registry_Etcd{
		Endpoints: []string{endpoint()},
		Backoff:   &bootstrapv1.Backoff{Initial: d(time.Second)}, // 缺 max
	}}
	if _, _, err := Provider(ctx, bad); err == nil {
		t.Fatal("退避段缺 max 时 Provider 应 fail-fast（不是静默用默认曲线）")
	} else if !strings.Contains(err.Error(), "backoff") {
		t.Errorf("错误应点名 backoff 段，got %v", err)
	}

	good := &bootstrapv1.Registry{Type: Type, Etcd: &bootstrapv1.Registry_Etcd{
		Endpoints: []string{endpoint()},
		Backoff: &bootstrapv1.Backoff{
			Curve: bootstrapv1.Backoff_LINEAR, Initial: d(200 * time.Millisecond),
			Step: d(100 * time.Millisecond), Max: d(5 * time.Second),
			Jitter: bootstrapv1.Backoff_EQUAL,
		},
	}}
	if _, _, err := Provider(ctx, good); err != nil && strings.Contains(err.Error(), "backoff") {
		t.Errorf("合法退避段被判错: %v", err)
	}
}
