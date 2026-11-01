package appkit

// §4.2：生命周期阶段预算不再有够不着的常量——端点等待上界优先取调用方 ctx 的
// 截止期，无截止期时从唯一的可调入口（契约 app.stop_timeout / StopTimeout
// Option）派生；钩子/效应/组件同样随 stopTimeout 缩放。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kalandramo/bald/transport"
)

// neverReadySrv 的 Endpoint 恒为 ":0"（模拟慢监听：Start 返回但端口迟迟不定）。
type neverReadySrv struct{}

func (neverReadySrv) Start(context.Context) error { return nil }
func (neverReadySrv) Stop(context.Context) error  { return nil }
func (neverReadySrv) Endpoint() string            { return "http://:0" }

func TestPhaseBudgetFor(t *testing.T) {
	for _, tc := range []struct {
		stopTimeout time.Duration
		want        time.Duration
	}{
		{30 * time.Second, 15 * time.Second}, // 契约默认
		{10 * time.Second, 5 * time.Second},  // 代码层兜底 → 与原常量同值
		{1 * time.Second, 500 * time.Millisecond},
		{0, fallbackPhaseBudget}, // 派生不出预算时的兜底
	} {
		if got := phaseBudgetFor(tc.stopTimeout); got != tc.want {
			t.Errorf("phaseBudgetFor(%v) = %v, want %v", tc.stopTimeout, got, tc.want)
		}
	}
}

// 预算随 stopTimeout 缩放：200ms 停机预算 → 100ms 等待上界即报错。
// 旧实现恒为 5s 常量，调 stop_timeout 也够不着（风险 4.2 的第一半）。
func TestWaitForEndpointsBudgetScalesWithStopTimeout(t *testing.T) {
	a := &App{servers: []transport.Server{neverReadySrv{}}, stopTimeout: 200 * time.Millisecond}

	start := time.Now()
	err := a.waitForEndpoints(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("waitForEndpoints = nil, want 超时错误")
	}
	if elapsed < 80*time.Millisecond {
		t.Fatalf("提前返回（%s）：未等到派生预算 100ms", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("等待上界未随 stopTimeout 缩放：耗时 %s，错误 %v", elapsed, err)
	}
	if !strings.Contains(err.Error(), "100ms") {
		t.Fatalf("错误应带上实际生效的预算，得 %v", err)
	}
}

// 调用方截止期优先于框架预算（护栏，两个方向都锁）：
//   - ctx 200ms 到期而框架预算 10s → 必须立刻结束，不能按预算干等；
//   - ctx 800ms 允许而框架预算 100ms → 必须等到调用方给的预算用完。
func TestWaitForEndpointsCallerDeadlineWins(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stopTimeout time.Duration
		ctxDeadline time.Duration
		wantAtLeast time.Duration
		wantAtMost  time.Duration
	}{
		{"ctx 更早", 20 * time.Second, 200 * time.Millisecond, 0, 3 * time.Second},
		{"ctx 更晚", 200 * time.Millisecond, 800 * time.Millisecond, 600 * time.Millisecond, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{servers: []transport.Server{neverReadySrv{}}, stopTimeout: tc.stopTimeout}
			ctx, cancel := context.WithTimeout(context.Background(), tc.ctxDeadline)
			defer cancel()

			start := time.Now()
			err := a.waitForEndpoints(ctx)
			elapsed := time.Since(start)

			if err == nil {
				t.Fatal("waitForEndpoints = nil, want 超时/取消错误")
			}
			if elapsed < tc.wantAtLeast {
				t.Fatalf("只等了 %s：框架预算压过了调用方的 %s", elapsed, tc.ctxDeadline)
			}
			if elapsed > tc.wantAtMost {
				t.Fatalf("等了 %s：超过调用方 %s 截止期", elapsed, tc.ctxDeadline)
			}
		})
	}
}

// 钩子预算随 stopTimeout 缩放：beforeStop 钩子拿到的 ctx 截止期由停机预算派生，
// 不再是固定 5s（runHook 的独立超时安全网保留）。
func TestStopAllHookBudgetDerivedFromStopTimeout(t *testing.T) {
	a := &App{stopTimeout: time.Second}

	var got time.Duration
	a.beforeStop = []func(context.Context) error{
		func(ctx context.Context) error {
			dl, ok := ctx.Deadline()
			if !ok {
				t.Error("beforeStop ctx 无截止期：runHook 安全网丢失")
				return nil
			}
			got = time.Until(dl)
			return nil
		},
	}
	a.stopAll(context.Background())

	if want := phaseBudgetFor(a.stopTimeout); got <= 0 || got > want {
		t.Fatalf("beforeStop 预算 = %s, want (0, %s]", got, want)
	}
}

// 未显式声明的 EffectTimeout/ComponentTimeout 从 stopTimeout 派生；显式声明优先。
func TestNewFromDeclPhaseTimeouts(t *testing.T) {
	derived := newFromDecl(Decl{StopTimeout: time.Minute})
	if derived.effectTimeout != 30*time.Second || derived.componentTimeout != 30*time.Second {
		t.Fatalf("未声明时未派生自 stopTimeout：effect=%s component=%s",
			derived.effectTimeout, derived.componentTimeout)
	}

	explicit := newFromDecl(Decl{
		StopTimeout:      time.Minute,
		EffectTimeout:    2 * time.Second,
		ComponentTimeout: 3 * time.Second,
	})
	if explicit.effectTimeout != 2*time.Second || explicit.componentTimeout != 3*time.Second {
		t.Fatalf("显式声明被派生值覆盖：effect=%s component=%s",
			explicit.effectTimeout, explicit.componentTimeout)
	}
}
