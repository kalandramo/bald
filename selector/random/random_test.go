// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package random_test

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/selector"
	"github.com/kalandramo/bald/selector/random"
)

func TestSelectEmptyNodesErrors(t *testing.T) {
	_, _, err := random.New().Select(context.Background(), nil)
	if err == nil {
		t.Fatal("空节点集应返回错误，不得静默返回 nil")
	}
}

func TestSelectStaysWithinCandidates(t *testing.T) {
	s := random.New()
	nodes := []selector.Node{
		selector.NewNode("a:1", "grpc", "svc", 100, nil),
		selector.NewNode("b:2", "grpc", "svc", 100, nil),
		selector.NewNode("c:3", "grpc", "svc", 100, nil),
	}
	want := map[string]bool{"a:1": true, "b:2": true, "c:3": true}
	// 随机分布不设精确计数（无状态、幂等）；只断言每次都落在候选集内且 DoneFunc 可用。
	for i := 0; i < 50; i++ {
		got, done, err := s.Select(context.Background(), nodes)
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		if got == nil || !want[got.Address()] {
			t.Fatalf("选中节点不在候选集内: %+v", got)
		}
		done(context.Background(), selector.DoneInfo{})
	}
}
