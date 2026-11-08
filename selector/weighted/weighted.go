// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package weighted 提供权重感知的随机选择策略。
package weighted

import (
	"context"
	"errors"
	"math/rand/v2"

	"github.com/kalandramo/bald/selector"
)

// New 构造加权策略实例（装配期显式 selector.MustRegister("weighted", New())）。
func New() selector.Selector { return &selectorImpl{} }

type selectorImpl struct{}

// Select 按权重比例选节点。权重为零或负的节点按 1 处理。
func (s *selectorImpl) Select(_ context.Context, nodes []selector.Node) (selector.Node, selector.DoneFunc, error) {
	if len(nodes) == 0 {
		return nil, nil, errors.New("weighted: no nodes available")
	}

	total := 0
	for _, n := range nodes {
		total += effectiveWeight(n)
	}
	if total <= 0 {
		total = len(nodes)
	}

	r := rand.IntN(total)
	for _, n := range nodes {
		w := effectiveWeight(n)
		if r < w {
			return n, selector.NoopDone, nil
		}
		r -= w
	}
	return nodes[len(nodes)-1], selector.NoopDone, nil
}

func effectiveWeight(n selector.Node) int {
	if w := n.Weight(); w > 0 {
		return w
	}
	return 1
}
