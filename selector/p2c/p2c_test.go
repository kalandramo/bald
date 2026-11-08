// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package p2c

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/selector"
)

func TestSelect(t *testing.T) {
	s := New()

	nodes := []selector.Node{
		selector.NewNode("a:1", "grpc", "svc", 100, nil),
		selector.NewNode("b:1", "grpc", "svc", 100, nil),
		selector.NewNode("c:1", "grpc", "svc", 100, nil),
	}

	for i := 0; i < 100; i++ {
		n, done, err := s.Select(context.Background(), nodes)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if n == nil {
			t.Fatal("selected nil node")
		}
		done(context.Background(), selector.DoneInfo{})
	}
}

func TestSelectEmpty(t *testing.T) {
	s := New()
	if _, _, err := s.Select(context.Background(), nil); err == nil {
		t.Fatal("expected error selecting from empty nodes")
	}
}

// TestFailureFeedbackDeprioritizes 钉住反馈回路的选择行为：成功计数掉到
// throttleSuccess 之下的节点在存在健康备选时应被避开。EWMA 的衰减动力学
// 依赖真实调用间隔（decayTime=10s，微秒级测试时钟下 w≈1 计数不移动），
// 因此直接置状态而不是模拟连续失败；EWMA 数学本身是 onexmesh 保真移植。
func TestFailureFeedbackDeprioritizes(t *testing.T) {
	s := New().(*p2cSelector)

	bad := selector.NewNode("bad:1", "grpc", "svc", 100, nil)
	good := selector.NewNode("good:1", "grpc", "svc", 100, nil)
	good2 := selector.NewNode("good:2", "grpc", "svc", 100, nil)

	// 把 bad 节点的成功计数打到阈值之下（模拟一段真实失败历史后的状态）。
	st := s.node("bad:1")
	st.success.Store(throttleSuccess - 100)
	if s.healthy(bad) {
		t.Fatal("bad node should be unhealthy below throttle threshold")
	}

	healthyCount := 0
	// 三个节点（一坏两好）：健康对采样占主导后，坏节点只经负载平局/forcePick
	// 偶发被选。注意 p2c 的降权是概率性的（采样到两个健康节点才提前收敛），
	// 硬排除是 WithCircuitBreaker 的职责——本批未移植。
	for i := 0; i < 100; i++ {
		n, _, err := s.Select(context.Background(), []selector.Node{bad, good, good2})
		if err != nil {
			t.Fatal(err)
		}
		if n.Address() == "bad:1" {
			continue
		}
		healthyCount++
	}
	if healthyCount < 70 {
		t.Fatalf("healthy nodes picked too rarely: %d/100", healthyCount)
	}
}
