// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package selector_test

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/selector"
	"github.com/kalandramo/bald/selector/random"
	"github.com/kalandramo/bald/selector/roundrobin"
	"github.com/kalandramo/bald/selector/weighted"
)

func init() {
	// 测试装配：显式注册（与生产装配同一习语），不用 blank import。
	selector.MustRegister("round_robin", roundrobin.New)
	selector.MustRegister("random", random.New)
	selector.MustRegister("weighted", weighted.New)
}

func nodes() []selector.Node {
	return []selector.Node{
		selector.NewNode("127.0.0.1:1", "grpc", "svc", 100, nil),
		selector.NewNode("127.0.0.1:2", "grpc", "svc", 100, nil),
		selector.NewNode("127.0.0.1:3", "grpc", "svc", 100, nil),
	}
}

func TestGetSelector(t *testing.T) {
	for _, name := range []string{"round_robin", "random", "weighted"} {
		s, err := selector.GetSelector(name)
		if err != nil {
			t.Fatalf("GetSelector(%q): %v", name, err)
		}
		if s == nil {
			t.Fatalf("GetSelector(%q) returned nil", name)
		}
	}
	if _, err := selector.GetSelector("unknown"); err == nil {
		t.Fatal("expected error for unknown selector")
	}
}

func TestMustRegisterFailsFast(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration should panic")
		}
	}()
	selector.MustRegister("round_robin", roundrobin.New)
}

func TestRoundRobinCycles(t *testing.T) {
	s, _ := selector.GetSelector("round_robin")
	seen := map[string]bool{}
	for i := 0; i < len(nodes()); i++ {
		n, _, err := s.Select(context.Background(), nodes())
		if err != nil {
			t.Fatal(err)
		}
		seen[n.Address()] = true
	}
	if len(seen) != len(nodes()) {
		t.Fatalf("round robin visited %d distinct nodes, want %d", len(seen), len(nodes()))
	}
}

func TestWeightedRespectsZeroWeight(t *testing.T) {
	s, _ := selector.GetSelector("weighted")
	ns := []selector.Node{
		selector.NewNode("127.0.0.1:1", "grpc", "svc", 100, nil),
		selector.NewNode("127.0.0.1:2", "grpc", "svc", 0, nil), // 权重 0 → 按 1 处理
	}
	heavy, light := 0, 0
	for i := 0; i < 200; i++ {
		n, _, err := s.Select(context.Background(), ns)
		if err != nil {
			t.Fatal(err)
		}
		if n.Address() == "127.0.0.1:1" {
			heavy++
		} else {
			light++
		}
	}
	// 100:1 权重比下，重节点几乎必然占绝对多数；断言比例的宽界防回归。
	if heavy < 150 || light > 50 {
		t.Fatalf("weighted distribution off: heavy=%d light=%d", heavy, light)
	}
}

func TestEmptyNodes(t *testing.T) {
	for _, name := range []string{"round_robin", "random", "weighted"} {
		s, _ := selector.GetSelector(name)
		if _, _, err := s.Select(context.Background(), nil); err == nil {
			t.Fatalf("expected error for empty nodes with %s", name)
		}
	}
}
