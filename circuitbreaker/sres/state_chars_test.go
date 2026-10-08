package sres

import (
	"context"
	"errors"
	"testing"

	"github.com/kalandramo/bald/circuitbreaker"
)

// ---------------------------------------------------------------------------
// D4：State() 的 StateClosed 分支不可达性 —— **特征化测试**
//
// 缺陷（D4，报告结论成立）：SRE 算法的接受率
//
//	accept = (requests - k*errors) / (requests + 1)
//
// 分母恒比分子大 1，故对任何 requests>0 都有 accept < 1 —— `State()` 里
// `accept >= 1 → StateClosed` 的分支**永不可达**；`accept < 1 → StateHalfOpen`
// 则恒真。即：只要有过请求，State 就只可能是 HalfOpen 或 Open，绝不返回 Closed。
//
// **本组测试不改变语义**（改算法会破坏既有调用方对三态的使用假设，超出缺陷
// 修复范围）——它把「已存在但不可达的状态」钉住为**可知事实**，并防止后人
// 误以为 StateClosed 会在统计上出现而写出依赖它的代码。
//
// 若要真正的三态语义，应改用阈值式实现（circuitbreaker/hystrix）——
// 见《框架缺陷报告》D4 的「应用层绕行：Wave 1b 改用 hystrix」。
// ---------------------------------------------------------------------------

// TestState_ClosedOnlyWhenNoRequests 唯一可达 StateClosed 的路径：零请求。
func TestState_ClosedOnlyWhenNoRequests(t *testing.T) {
	b := New()
	defer b.Close()

	if s := b.State(); s != circuitbreaker.StateClosed {
		t.Fatalf("零请求时 State() = %v, want StateClosed", s)
	}
}

// TestState_ClosedUnreachableOnceRequestsExist 一旦产生请求（哪怕全成功），
// StateClosed 即不可达——这是 D4 的核心事实。
func TestState_ClosedUnreachableOnceRequestsExist(t *testing.T) {
	b := New()
	defer b.Close()

	// 累积若干成功请求（Allow 可能概率性拒绝，故只在放行时 MarkSuccess）。
	for i := 0; i < 20; i++ {
		if err := b.Allow(); err == nil {
			b.MarkSuccess()
		}
	}

	// 必然已产生请求。
	s := b.State()
	if s == circuitbreaker.StateClosed {
		t.Fatal("D4：一旦有请求，accept=(r-k*e)/(r+1) < 1，StateClosed 应不可达")
	}
	// 全成功场景下应落到 HalfOpen（accept<1 且 >0）。
	if s != circuitbreaker.StateHalfOpen {
		t.Fatalf("全成功累计请求后 State() = %v, want StateHalfOpen（SRE 的常态）", s)
	}
}

// TestState_NeverClosedAfterErrors 有错误时更不可能 Closed（accept 更低）。
func TestState_NeverClosedAfterErrors(t *testing.T) {
	b := New()
	defer b.Close()

	for i := 0; i < 10; i++ {
		_ = b.Execute(context.Background(), func() error { return errors.New("boom") })
	}
	if s := b.State(); s == circuitbreaker.StateClosed {
		t.Fatal("持续失败后 StateClosed 绝不应出现")
	}
}

// TestAcceptFormula_NeverReachesOne 直接钉住公式性质：对 requests>0，
// accept 恒 < 1 —— 这是「StateClosed 不可达」的根因，用数值性质而非
// 状态间接断言，使根因显式化。
func TestAcceptFormula_NeverReachesOne(t *testing.T) {
	const k = 2.0
	for _, requests := range []float64{1, 2, 10, 100, 1000, 1e6} {
		for _, errors := range []float64{0, 1, requests / 2, requests} {
			if errors > requests {
				continue
			}
			accept := (requests - k*errors) / (requests + 1)
			if accept >= 1 {
				t.Fatalf("accept(%v req, %v err) = %v — 若 >=1 则 StateClosed 可达，D4 结论被推翻", requests, errors, accept)
			}
		}
	}
}
