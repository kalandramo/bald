// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package circuitbreaker 把熔断器接成 selector 的按节点硬排除层：熔断打开
// 的节点直接不进候选集（区别于 p2c 的概率性降权），调用结果经 DoneFunc
// 反馈进选中节点的熔断器。能力自 onexmesh WithCircuitBreaker 吸收；语义
// 改写为 bald 习语——熔断器当「每节点健康谕言」（State 查询）与「结果汇」
// （Mark* 反馈）用，不走 Allow 门控（理由见设计文档「为什么不走 Allow」）。
//
// selector 核心与 circuitbreaker 契约都零改动：本包是桥接子 module，
// 同时 import 两者，装饰器实现 selector.Selector 包裹任意内层策略。
//
// 见 docs/devel/zh-CN/Bald 节点熔断设计.md。
package circuitbreaker

import (
	"context"
	"errors"
	"sync"

	cb "github.com/kalandramo/bald/circuitbreaker"
	"github.com/kalandramo/bald/selector"
)

// Factory 构造一个熔断器实例。Registry 按节点地址各持一个，故工厂每次
// 调用须返回独立实例——自适应状态不得跨节点共享（对齐 selector.Factory）。
type Factory func() cb.CircuitBreaker

// Registry 按节点地址懒构造并持有熔断器，并发安全。
type Registry struct {
	mu       sync.RWMutex
	factory  Factory
	breakers map[string]cb.CircuitBreaker
}

// NewRegistry 构造一个空注册表。factory 为 nil 时 panic——装配期编程错误
// fail-fast（对齐 selector.MustRegister）。
func NewRegistry(factory Factory) *Registry {
	if factory == nil {
		panic("selector/circuitbreaker: factory cannot be nil")
	}
	return &Registry{factory: factory, breakers: make(map[string]cb.CircuitBreaker)}
}

// Get 返回地址 addr 的熔断器，首次使用时经工厂懒构造。热路径走读锁；
// 只有 map miss 才拿写锁，并发 Select 不会被串行化（与 p2c.node 同款）。
func (r *Registry) Get(addr string) cb.CircuitBreaker {
	r.mu.RLock()
	b, ok := r.breakers[addr]
	r.mu.RUnlock()
	if ok {
		return b
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.breakers[addr]; ok {
		return b
	}
	b = r.factory()
	r.breakers[addr] = b
	return b
}

// Healthy 报告节点是否可进入候选集：熔断器处于 StateOpen 即不健康。
// 尚未构造熔断器的地址视为健康——新节点乐观放行，选中后才经 Get 构造，
// 避免为从不被选中的节点空建熔断器。
//
// State() 语义因实现而异（见《Bald 熔断设计》）：hystrix/sres 的 Open
// 对应错误率跳闸，适合按节点硬排除；sentinel 的 State() 每次发探针 Entry
// 会污染统计，不宜驱动本过滤。hystrix 的 State() 带懒转移（Open 过睡窗
// 自动转 HalfOpen），故恢复由本查询自身驱动——睡窗一过节点重新可选。
func (r *Registry) Healthy(addr string) bool {
	r.mu.RLock()
	b, ok := r.breakers[addr]
	r.mu.RUnlock()
	if !ok {
		return true
	}
	return b.State() != cb.StateOpen
}

// Prune 清理不再出现在候选集中的地址熔断器（经 Close 释放），防止实例
// churn 时 map 无界增长（与 p2c.prune 同款）。稳态下是廉价空操作。
func (r *Registry) Prune(nodes []selector.Node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.breakers) <= len(nodes) {
		return
	}

	active := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		active[n.Address()] = struct{}{}
	}
	for addr, b := range r.breakers {
		if _, ok := active[addr]; !ok {
			_ = b.Close()
			delete(r.breakers, addr)
		}
	}
}

// Close 释放所有熔断器并清空注册表。
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for addr, b := range r.breakers {
		if err := b.Close(); err != nil {
			errs = append(errs, err)
		}
		delete(r.breakers, addr)
	}
	return errors.Join(errs...)
}

// WithCircuitBreaker 装饰一个 selector：Select 前硬排除熔断打开的节点，
// 再委托内层策略从健康子集里选；调用结果经 DoneFunc 反馈进选中节点的
// 熔断器（DoneInfo.Err 非空记 MarkFailure，否则 MarkSuccess）。全部节点
// 被排除时返回 cb.ErrCircuitOpen。
//
// 不走 Allow 门控：在负载均衡反馈语境里每个调用都是探针，Mark* 直接驱动
// hystrix 的半开 heal/reopen；而 halfOpenIn 只由 Allow 置位——不调 Allow
// 即无「探针悬着永久锁死」的风险。详见设计文档「为什么不走 Allow」。
func WithCircuitBreaker(inner selector.Selector, reg *Registry) selector.Selector {
	return &cbSelector{inner: inner, reg: reg}
}

// cbSelector 是按节点熔断硬排除的装饰器。
type cbSelector struct {
	inner selector.Selector
	reg   *Registry
}

func (s *cbSelector) Select(ctx context.Context, nodes []selector.Node) (selector.Node, selector.DoneFunc, error) {
	healthy := make([]selector.Node, 0, len(nodes))
	for _, n := range nodes {
		if s.reg.Healthy(n.Address()) {
			healthy = append(healthy, n)
		}
	}
	if len(healthy) == 0 {
		return nil, nil, cb.ErrCircuitOpen
	}

	node, done, err := s.inner.Select(ctx, healthy)
	if err != nil {
		return nil, nil, err
	}
	breaker := s.reg.Get(node.Address())
	s.reg.Prune(nodes)

	return node, func(ctx context.Context, di selector.DoneInfo) {
		if done != nil {
			done(ctx, di)
		}
		if di.Err != nil {
			breaker.MarkFailure()
		} else {
			breaker.MarkSuccess()
		}
	}, nil
}