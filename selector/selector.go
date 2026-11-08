// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package selector 提供节点集合上的可插拔负载均衡（策略模式 + 结果反馈回路）：
// Select 选一个节点并返回 DoneFunc，调用完成后把结果反馈进策略的自适应状态
// （如 p2c 的 EWMA）。代码自 onexmesh 移植；注册语义改为 bald 习语——显式
// MustRegister（对齐 encoding），不用 init() + blank import。
package selector

import (
	"context"
	"fmt"
	"sync"
)

// Node 抽象一个可选实例。
type Node interface {
	// Address 是 host:port 连接地址。
	Address() string
	// Scheme 是传输 scheme，如 "grpc" 或 "http"。
	Scheme() string
	// ServiceName 是逻辑服务名。
	ServiceName() string
	// Weight 是节点的静态权重（>=0）。
	Weight() int
	// Metadata 携带任意标签。
	Metadata() map[string]string
}

// node 是 Node 的默认实现。
type node struct {
	address     string
	scheme      string
	serviceName string
	weight      int
	metadata    map[string]string
}

// NewNode 从各部分组装一个 Node。
func NewNode(address, scheme, serviceName string, weight int, metadata map[string]string) Node {
	return &node{
		address:     address,
		scheme:      scheme,
		serviceName: serviceName,
		weight:      weight,
		metadata:    metadata,
	}
}

func (n *node) Address() string             { return n.address }
func (n *node) Scheme() string              { return n.scheme }
func (n *node) ServiceName() string         { return n.serviceName }
func (n *node) Weight() int                 { return n.weight }
func (n *node) Metadata() map[string]string { return n.metadata }

// DoneInfo 报告一次已完成调用的结果。
type DoneInfo struct {
	Err           error
	BytesSent     int64
	BytesReceived int64
}

// DoneFunc 在调用完成后被调用，把结果反馈进策略的决策状态。
type DoneFunc func(ctx context.Context, di DoneInfo)

// Selector 从候选节点集合中选择一个节点。
type Selector interface {
	// Select 返回选中的节点与报告调用结果的 DoneFunc；无可用节点时返回错误。
	Select(ctx context.Context, nodes []Node) (Node, DoneFunc, error)
}

// Factory 构造一个 Selector 实例（每次调用返回新实例——策略自带自适应状态，
// 不得跨服务共享）。
type Factory func() Selector

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// MustRegister 按名称注册策略工厂。空名称、nil 工厂、重复名称均 panic——
// 注册期编程错误 fail-fast（对齐 encoding.MustRegister）。各策略子包导出
// New() 构造器，由应用在装配期显式注册（如 selector.MustRegister("p2c", p2c.New())）；
// 不用 init() + blank import——import 关系就是依赖清单。
func MustRegister(name string, f Factory) {
	if name == "" {
		panic("selector: strategy name cannot be empty")
	}
	if f == nil {
		panic("selector: factory cannot be nil")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[name]; dup {
		panic("selector: strategy already registered: " + name)
	}
	factories[name] = f
}

// GetSelector 按名称构造一个新 Selector；未注册返回错误（fail-fast）。
func GetSelector(name string) (Selector, error) {
	mu.RLock()
	f, ok := factories[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("selector %q not registered", name)
	}
	return f(), nil
}

// Registered 报告某策略名是否已注册。
func Registered(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := factories[name]
	return ok
}

// NoopDone 是丢弃反馈的 DoneFunc。
func NoopDone(ctx context.Context, di DoneInfo) {}
