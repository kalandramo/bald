// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package random 提供均匀随机选择策略。
package random

import (
	"context"
	"errors"
	"math/rand/v2"

	"github.com/kalandramo/bald/selector"
)

// New 构造随机策略实例（装配期显式 selector.MustRegister("random", New())）。
func New() selector.Selector { return &selectorImpl{} }

type selectorImpl struct{}

func (s *selectorImpl) Select(_ context.Context, nodes []selector.Node) (selector.Node, selector.DoneFunc, error) {
	if len(nodes) == 0 {
		return nil, nil, errors.New("random: no nodes available")
	}
	return nodes[rand.IntN(len(nodes))], selector.NoopDone, nil
}
