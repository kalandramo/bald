// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package p2c 实现「两次随机选择」策略：带时间衰减的 EWMA 延迟与成功数跟踪
// （借鉴 go-zero 的 p2c_ewma）。每次选取采样两个节点、选估算负载较低者；
// DoneFunc 把观测到的延迟/错误反馈进节点的 EWMA。失败率高的节点被降权，
// 长期未被选中的陈旧节点会被强制选中——避免刚恢复的节点被饿死。
package p2c

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kalandramo/bald/selector"
)

const (
	// decayTime 是 EWMA 时间常数；越大历史权重越高。
	decayTime = 10 * time.Second
	// forcePick 是「超过这么久未被选中就强制选中」的时长。
	forcePick = time.Second
	// initSuccess 是新节点的乐观初始成功计数。
	initSuccess = 1000
	// throttleSuccess 是「低于此成功计数视为不健康、有备选时避开」的阈值。
	throttleSuccess = initSuccess / 2
	// pickTimes 是寻找一对互异健康节点的尝试次数。
	pickTimes = 3
)

// New 构造 p2c 策略实例（装配期显式 selector.MustRegister("p2c", New())）。
func New() selector.Selector {
	return &p2cSelector{state: make(map[string]*node)}
}

// p2cSelector 按 EWMA 负载做两次随机选择。
type p2cSelector struct {
	mu    sync.RWMutex
	state map[string]*node
}

// node 保存每地址的自适应状态。
type node struct {
	lag      atomic.Int64 // EWMA 延迟（纳秒）
	inflight atomic.Int64
	success  atomic.Int64 // EWMA 成功计数
	pick     atomic.Int64 // 上次选中时间（UnixNano），供 forcePick
	last     atomic.Int64 // 上次完成时间（UnixNano），供时间衰减
}

func (s *p2cSelector) Select(_ context.Context, nodes []selector.Node) (selector.Node, selector.DoneFunc, error) {
	if len(nodes) == 0 {
		return nil, nil, errors.New("p2c: no nodes available")
	}

	chosen := s.pick(nodes)
	st := s.node(chosen.Address())
	st.inflight.Add(1)
	s.prune(nodes)

	start := time.Now()
	return chosen, func(_ context.Context, di selector.DoneInfo) {
		st.done(start, di.Err)
	}, nil
}

// pick 基于估算负载在两个随机节点间抉择。
func (s *p2cSelector) pick(nodes []selector.Node) selector.Node {
	if len(nodes) == 1 {
		return nodes[0]
	}

	var a, b selector.Node
	for i := 0; i < pickTimes; i++ {
		a = nodes[rand.IntN(len(nodes))]
		b = nodes[rand.IntN(len(nodes))]
		if a.Address() == b.Address() {
			continue
		}
		if s.healthy(a) && s.healthy(b) {
			break
		}
	}

	if s.load(a) > s.load(b) {
		a, b = b, a
	}

	now := time.Now().UnixNano()
	na := s.node(a.Address())
	nb := s.node(b.Address())
	// 负载较高的节点若太久未被选中则强制选中——刚恢复的节点不被饿死。
	if pick := nb.pick.Load(); now-pick > int64(forcePick) && nb.pick.CompareAndSwap(pick, now) {
		return b
	}
	na.pick.Store(now)
	return a
}

// healthy 报告节点成功率是否高于降权阈值。
func (s *p2cSelector) healthy(n selector.Node) bool {
	return s.node(n.Address()).success.Load() > throttleSuccess
}

// load 把节点当前负载估算为 sqrt(lag) * (inflight + 1)。
func (s *p2cSelector) load(n selector.Node) int64 {
	st := s.node(n.Address())
	lag := int64(math.Sqrt(float64(st.lag.Load() + 1)))
	return lag * (st.inflight.Load() + 1)
}

// node 返回每地址状态，首次使用时创建。热路径走读锁；只有 map miss 才拿写
// 锁，并发 Select 不会被串行化。初始完成时间在此打点——节点的首次失败不会
// 把乐观成功计数衰减到零。
func (s *p2cSelector) node(addr string) *node {
	s.mu.RLock()
	if n, ok := s.state[addr]; ok {
		s.mu.RUnlock()
		return n
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.state[addr]; ok {
		return n
	}
	n := &node{}
	n.lag.Store(int64(10 * time.Millisecond))
	n.success.Store(initSuccess)
	n.last.Store(time.Now().UnixNano())
	s.state[addr] = n
	return n
}

// prune 清理不再出现在候选集中的地址状态，防止实例churn时 map 无界增长。
// 稳态下是廉价空操作。
func (s *p2cSelector) prune(nodes []selector.Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state) <= len(nodes) {
		return
	}

	active := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		active[n.Address()] = struct{}{}
	}
	for addr := range s.state {
		if _, ok := active[addr]; !ok {
			delete(s.state, addr)
		}
	}
}

// done 从观测结果更新节点的 EWMA 延迟与成功计数。
func (n *node) done(start time.Time, err error) {
	n.inflight.Add(-1)

	now := time.Now().UnixNano()
	last := n.last.Swap(now)
	td := now - last
	if td < 0 {
		td = 0
	}
	// td 越大 w 越小——陈旧历史逐渐衰减。
	w := math.Exp(float64(-td) / float64(decayTime))

	lag := now - start.UnixNano()
	if lag < 0 {
		lag = 0
	}
	if n.lag.Load() == 0 {
		w = 0
	}
	n.updateEWMA(&n.lag, w, float64(lag))

	success := float64(initSuccess)
	if err != nil {
		success = 0
	}
	n.updateEWMA(&n.success, w, success)
}

// updateEWMA 用 CAS 循环原子更新目标 EWMA，并发完成不丢更新。
func (n *node) updateEWMA(target *atomic.Int64, w, sample float64) {
	for {
		old := target.Load()
		newVal := int64(float64(old)*w + sample*(1-w))
		if target.CompareAndSwap(old, newVal) {
			return
		}
	}
}
