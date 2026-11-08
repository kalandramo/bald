// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package roundrobin 提供轮询选择策略。
package roundrobin

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/kalandramo/bald/selector"
)

// New 构造轮询策略实例（装配期显式 selector.MustRegister("round_robin", New())）。
func New() selector.Selector { return &selectorImpl{} }

// selectorImpl 用原子计数器按序轮询节点。
type selectorImpl struct {
	counter atomic.Uint64
}

func (s *selectorImpl) Select(_ context.Context, nodes []selector.Node) (selector.Node, selector.DoneFunc, error) {
	if len(nodes) == 0 {
		return nil, nil, errors.New("roundrobin: no nodes available")
	}
	idx := s.counter.Add(1) - 1
	return nodes[idx%uint64(len(nodes))], selector.NoopDone, nil
}
