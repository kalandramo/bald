// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package p2c

import (
	"context"
	"testing"
	"time"

	"github.com/kalandramo/bald/selector"
)

// TestSelectSingleNode 覆盖单节点快速路径。
func TestSelectSingleNode(t *testing.T) {
	s := New()
	n, done, err := s.Select(context.Background(), []selector.Node{
		selector.NewNode("solo:1", "grpc", "svc", 1, nil),
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if n.Address() != "solo:1" {
		t.Fatalf("selected %q, want solo:1", n.Address())
	}
	done(context.Background(), selector.DoneInfo{})
}

// TestPickPrefersLowerLoad 覆盖负载比较交换分支：两节点负载悬殊时（随机采样
// 顺序各半），绝大多数选择应落在低负载节点。三次采样可能两次都落到同一节点
// （p2c 找不到互异对时直接返回该节点，1/8 概率），故允许少量偏差：200 次下
// 高负载节点被选中的期望约 12 次（1/16），阈值 160 远离随机基线 100。
func TestPickPrefersLowerLoad(t *testing.T) {
	s := New().(*p2cSelector)
	low := selector.NewNode("low:1", "grpc", "svc", 1, nil)
	high := selector.NewNode("high:1", "grpc", "svc", 1, nil)

	stLow, stHigh := s.node(low.Address()), s.node(high.Address())
	stLow.lag.Store(1)
	stHigh.lag.Store(1 << 60)

	lowPicked := 0
	for i := 0; i < 200; i++ {
		now := time.Now().UnixNano()
		stLow.pick.Store(now)
		stHigh.pick.Store(now) // 抑制 forcePick 分支

		if got := s.pick([]selector.Node{low, high}); got.Address() == low.Address() {
			lowPicked++
		}
	}
	if lowPicked < 160 {
		t.Fatalf("low-load node picked %d/200 times, want >=160", lowPicked)
	}
}

// TestPruneRemovesStale 覆盖状态清理：候选集收缩后陈旧地址被删除。
func TestPruneRemovesStale(t *testing.T) {
	s := New().(*p2cSelector)
	keep := selector.NewNode("keep:1", "grpc", "svc", 1, nil)
	s.node(keep.Address())
	s.node("stale:1")

	if _, _, err := s.Select(context.Background(), []selector.Node{keep}); err != nil {
		t.Fatalf("select: %v", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.state["stale:1"]; ok {
		t.Fatal("stale node state must be pruned")
	}
	if _, ok := s.state["keep:1"]; !ok {
		t.Fatal("active node state must be kept")
	}
}

// TestDoneClampsNegativeDurations 覆盖 td/lag 为负的钳制分支，以及 lag=0 时
// 将时间衰减权重归零的分支。
func TestDoneClampsNegativeDurations(t *testing.T) {
	n := &node{}
	n.last.Store(time.Now().Add(time.Hour).UnixNano()) // 时钟回拨 → td < 0
	n.done(time.Now().Add(time.Hour), nil)             // start 在未来 → lag < 0
	n.done(time.Now(), nil)

	zero := &node{}
	zero.lag.Store(0) // 触发 w = 0 分支
	zero.done(time.Now(), nil)

	if got := n.inflight.Load(); got != -2 {
		t.Fatalf("inflight = %d, want -2 (done decrements unconditionally)", got)
	}
}

// 结构性零覆盖（登记备查）：
//   p2c.go:131-132 —— node() 写锁后的二次检查只在「读锁 miss 与写锁获取之间
//   另一 goroutine 先插入同地址」的竞态窗口命中，包内无可注入的调度钩子，
//   无法确定性触发；该分支作为热路径并发正确性护栏保留。
