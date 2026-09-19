package sentinel

// sentinel_test.go —— 补零测试：Sentinel 限流适配器。
//
// ## 为何补这个测试（Wave 4.6）
//
// 计划 4.6：「补零测试能力轴的测试：`bald/ratelimit/sentinel/`、
// `bald/registry/{etcd,consul,kubernetes}/`」——这些包此前 **0 测试**。
//
// ## 本测试覆盖的核心语义
//
//  1. **无规则时放行**（Sentinel 默认语义：未配规则的资源不拦截）；
//  2. **QPS 规则生效**：超阈值时 `Allow()` 返回 `ErrLimited`；
//  3. **`Wait` 阻塞后放行**（等待窗口过去）；
//  4. **`Wait` 响应 ctx 取消**（不无限阻塞）；
//  5. **`AllowEntry`/`ReleaseEntry`** 配对（并发型规则的句柄语义）。
//
// **无外部依赖**：Sentinel 是进程内库，规则经 `flow.LoadRules` 注入。

import (
	"context"
	"errors"
	"testing"
	"time"

	sentinelapi "github.com/alibaba/sentinel-golang/api"
	"github.com/alibaba/sentinel-golang/core/flow"

	"github.com/kalandramo/bald/ratelimit"
)

// initSentinel 初始化 Sentinel（幂等，全局一次）。
func initSentinel(t *testing.T) {
	t.Helper()
	if err := sentinelapi.InitDefault(); err != nil {
		// 已初始化时 InitDefault 可能报错——不视为失败。
		t.Logf("InitDefault: %v（已初始化则正常）", err)
	}
}

// loadFlowRule 为资源加载 QPS 规则，测试结束清理。
func loadFlowRule(t *testing.T, resource string, threshold float64, intervalMs uint32) {
	t.Helper()
	rules := []*flow.Rule{{
		Resource:               resource,
		TokenCalculateStrategy: flow.Direct,
		ControlBehavior:        flow.Reject,
		Threshold:              threshold,
		StatIntervalInMs:       intervalMs,
	}}
	if _, err := flow.LoadRules(rules); err != nil {
		t.Fatalf("LoadRules(%s): %v", resource, err)
	}
	t.Cleanup(func() { _, _ = flow.LoadRules(nil) })
}

// TestSentinel_AllowWithoutRule —— 无规则时放行（Sentinel 默认语义）。
func TestSentinel_AllowWithoutRule(t *testing.T) {
	initSentinel(t)
	// 资源名唯一，避免与并发测试的规则冲突。
	lim := New("test-no-rule-" + t.Name())
	defer lim.Close()

	ok, err := lim.Allow()
	if err != nil {
		t.Fatalf("无规则时不应报错: %v", err)
	}
	if !ok {
		t.Fatal("无规则时应放行（Sentinel 默认不拦截未配规则的资源）")
	}
	t.Log("确认：无规则时放行")
}

// TestSentinel_FlowRuleRejects —— QPS 规则超阈值时拒绝（核心语义）。
func TestSentinel_FlowRuleRejects(t *testing.T) {
	initSentinel(t)
	resource := "test-qps-" + t.Name()
	// 阈值 2 QPS，统计窗口 1 秒。
	loadFlowRule(t, resource, 2, 1000)

	lim := New(resource)
	defer lim.Close()

	// 连续请求：前 2 次放行，之后应被拒（窗口内超阈值）。
	allowed, limited := 0, 0
	for i := 0; i < 10; i++ {
		ok, err := lim.Allow()
		if err != nil {
			if errors.Is(err, ratelimit.ErrLimited) {
				limited++
				continue
			}
			t.Fatalf("意外错误: %v", err)
		}
		if ok {
			allowed++
		}
	}
	if allowed == 0 {
		t.Fatal("应至少有请求被放行")
	}
	if limited == 0 {
		t.Fatalf("阈值 2 但 10 次请求全放行（allowed=%d）——限流未生效", allowed)
	}
	t.Logf("确认：QPS 规则生效（allowed=%d limited=%d）", allowed, limited)
}

// TestSentinel_WaitBlocksThenAdmits —— Wait 阻塞至窗口过去后放行。
func TestSentinel_WaitBlocksThenAdmits(t *testing.T) {
	initSentinel(t)
	resource := "test-wait-" + t.Name()
	loadFlowRule(t, resource, 1, 300) // 300ms 窗口，1 QPS

	lim := New(resource, WithWaitInterval(20*time.Millisecond))
	defer lim.Close()

	// 先用掉配额。
	if ok, _ := lim.Allow(); !ok {
		t.Fatal("首次请求应放行")
	}
	// Wait 应阻塞到窗口过去后放行（不报错）。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := lim.Wait(ctx); err != nil {
		t.Fatalf("Wait 应最终放行，实际: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("Wait 未阻塞（%v）——可能配额未被用尽", elapsed)
	}
	t.Logf("确认：Wait 阻塞 %v 后放行", elapsed.Round(time.Millisecond))
}

// TestSentinel_WaitRespectsContextCancel —— Wait 响应 ctx 取消（不无限阻塞）。
func TestSentinel_WaitRespectsContextCancel(t *testing.T) {
	initSentinel(t)
	resource := "test-wait-cancel-" + t.Name()
	// 极低阈值 + 长窗口 → Wait 必然持续被拒。
	loadFlowRule(t, resource, 1, 60000)

	lim := New(resource, WithWaitInterval(50*time.Millisecond))
	defer lim.Close()

	// 用尽配额。
	if ok, _ := lim.Allow(); !ok {
		t.Fatal("首次请求应放行")
	}

	// 短超时 ctx → Wait 应返回 ctx.Err()。
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := lim.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait 应返回 ctx 超时错误，实际: %v", err)
	}
	t.Log("确认：Wait 响应 ctx 取消（不无限阻塞）")
}

// TestSentinel_AllowEntryRelease —— AllowEntry/ReleaseEntry 配对。
func TestSentinel_AllowEntryRelease(t *testing.T) {
	initSentinel(t)
	lim := New("test-entry-" + t.Name())
	defer lim.Close()

	e, err := lim.AllowEntry()
	if err != nil {
		t.Fatalf("AllowEntry 应成功: %v", err)
	}
	if e == nil {
		t.Fatal("AllowEntry 应返回非 nil 句柄")
	}
	// 释放不应 panic。
	lim.ReleaseEntry(e)
	// nil 句柄释放应安全（防御）。
	lim.ReleaseEntry(nil)
	t.Log("确认：AllowEntry/ReleaseEntry 配对正常，nil 释放安全")
}

// TestSentinel_CloseIsNoop —— Close 是 no-op（Sentinel 自管生命周期）。
func TestSentinel_CloseIsNoop(t *testing.T) {
	lim := New("test-close-" + t.Name())
	if err := lim.Close(); err != nil {
		t.Fatalf("Close 应为 no-op 返回 nil，实际: %v", err)
	}
	t.Log("确认：Close 为 no-op")
}
