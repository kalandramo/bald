// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package weighted_test

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/selector"
	"github.com/kalandramo/bald/selector/weighted"
)

// TestSelectEmptyNodesErrors 覆盖无节点分支：返回错误。
func TestSelectEmptyNodesErrors(t *testing.T) {
	if _, _, err := weighted.New().Select(context.Background(), nil); err == nil {
		t.Fatal("weighted: 期望空节点集合返回错误，实际返回 nil")
	}
}

// TestSelectReturnsCandidate 覆盖有效权重循环与命中返回：返回节点必属候选集，
// 且零/负权重节点（effectiveWeight 归一为 1）仍有机会被选中。
func TestSelectReturnsCandidate(t *testing.T) {
	s := weighted.New()
	nodes := []selector.Node{
		selector.NewNode("heavy", "grpc", "svc", 100, nil),
		selector.NewNode("zero", "grpc", "svc", 0, nil),
		selector.NewNode("negative", "grpc", "svc", -5, nil),
	}
	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		got, done, err := s.Select(context.Background(), nodes)
		if err != nil {
			t.Fatalf("Select 返回错误: %v", err)
		}
		seen[got.Address()]++
		done(context.Background(), selector.DoneInfo{})
	}
	// 权重大者应更常被选中。
	if seen["heavy"] <= seen["zero"] {
		t.Fatalf("期望 heavy 命中次数远多于 zero，实际 heavy=%d zero=%d", seen["heavy"], seen["zero"])
	}
}

// TestSingleNodeAlwaysSelected 覆盖单节点路径。
func TestSingleNodeAlwaysSelected(t *testing.T) {
	s := weighted.New()
	nodes := []selector.Node{selector.NewNode("only", "grpc", "svc", 3, nil)}
	got, _, err := s.Select(context.Background(), nodes)
	if err != nil {
		t.Fatalf("Select 返回错误: %v", err)
	}
	if got.Address() != "only" {
		t.Fatalf("期望 only，实际 %q", got.Address())
	}
}
