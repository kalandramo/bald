// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package roundrobin_test

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/selector"
	"github.com/kalandramo/bald/selector/roundrobin"
)

// TestSelectEmptyNodesErrors 覆盖无节点分支：返回错误。
func TestSelectEmptyNodesErrors(t *testing.T) {
	if _, _, err := roundrobin.New().Select(context.Background(), nil); err == nil {
		t.Fatal("roundrobin: 期望空节点集合返回错误，实际返回 nil")
	}
}

// TestSelectCyclesInOrder 断言轮询按序循环：两个节点时序列恒为 a,b,a,b…
// （原子计数器从 0 起，第 i 次 Select 命中 nodes[i%len]）。
func TestSelectCyclesInOrder(t *testing.T) {
	s := roundrobin.New()
	nodes := []selector.Node{
		selector.NewNode("a:1", "grpc", "svc", 100, nil),
		selector.NewNode("b:2", "grpc", "svc", 100, nil),
	}
	want := []string{"a:1", "b:2", "a:1", "b:2"}
	for i, exp := range want {
		got, done, err := s.Select(context.Background(), nodes)
		if err != nil {
			t.Fatalf("第 %d 次 Select 返回错误: %v", i, err)
		}
		if got.Address() != exp {
			t.Fatalf("第 %d 次 Select 期望 %q，实际 %q", i, exp, got.Address())
		}
		done(context.Background(), selector.DoneInfo{})
	}
}
