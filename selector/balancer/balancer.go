// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package balancer 把 bald 的 selector 策略适配成 gRPC balancer，让 gRPC 客户端
// 用框架的负载均衡策略（round_robin/weighted/p2c/...）替代 grpc 内置 balancer。
// 代码自 onexmesh 移植；注册语义改为 bald 显式习语（grpc balancer 全局注册表
// 是生态标准无法避免，但策略注册与 builder 注册都由调用方显式触发）。
package balancer

import (
	"fmt"
	"sync"

	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"

	"github.com/kalandramo/bald/selector"
	"github.com/kalandramo/bald/selector/p2c"
	"github.com/kalandramo/bald/selector/random"
	"github.com/kalandramo/bald/selector/roundrobin"
	"github.com/kalandramo/bald/selector/weighted"
)

// Name 是注册进 grpc 的 balancer 名：客户端 service config 里
// `{"loadBalancingConfig": [{"bald_selector": {...}}]}` 或 Dial 时引用。
const Name = "bald_selector"

// StrategyKey 是 resolver 地址属性键：携带该节点的 selector 策略
// （如 "round_robin"、"weighted"、"p2c"）。
const StrategyKey = "bald.selector.strategy"

// ServiceKey 是 resolver 地址属性键：携带服务名——按 (策略, 服务名) 缓存
// selector 实例，让 p2c 的 EWMA、轮询游标等自适应状态在 grpc 每次地址更新
// 触发的 picker 重建之间存活，且不跨服务泄漏。
const ServiceKey = "bald.selector.service"

// WeightKey 是 resolver 地址属性键：携带节点静态权重，让 weighted 策略在
// gRPC 面（不只 HTTP）生效。
const WeightKey = "bald.selector.weight"

// RegisterDefaultStrategies 把四个内置策略显式注册进 selector 全局注册表
// （bald 习语：显式注册而非 blank import）。与 Register 搭配使用：
//
//	balancer.RegisterDefaultStrategies()
//	balancer.Register()
//
// defaultStrategiesOnce 保证默认策略注册幂等（MustRegister 重复注册 panic，
// 测试与多个调用方共处一个进程时经这里收敛为一次）。
var defaultStrategiesOnce sync.Once

func RegisterDefaultStrategies() {
	defaultStrategiesOnce.Do(func() {
		selector.MustRegister("round_robin", roundrobin.New)
		selector.MustRegister("random", random.New)
		selector.MustRegister("weighted", weighted.New)
		selector.MustRegister("p2c", p2c.New)
	})
}

// Register 把 bald_selector 的 balancer builder 注册进 grpc 全局注册表。
// 重复注册 panic（grpc 语义）——进程内只调一次。
func Register() {
	balancer.Register(base.NewBalancerBuilder(Name, &pickerBuilder{}, base.Config{HealthCheck: false}))
}

// pickerBuilder 构建委托给 selector.Selector 的 Picker。selector 实例按
// (策略, 服务名) 缓存——自适应状态跨 picker 重建存活。
type pickerBuilder struct {
	sel sync.Map // map[string]selector.Selector，键 = strategy + "\x00" + service
}

func (b *pickerBuilder) Build(info base.PickerBuildInfo) balancer.Picker {
	if len(info.ReadySCs) == 0 {
		return base.NewErrPicker(balancer.ErrNoSubConnAvailable)
	}

	strategy := "round_robin"
	service := ""
	nodes := make([]selector.Node, 0, len(info.ReadySCs))
	scByAddr := make(map[string]balancer.SubConn, len(info.ReadySCs))

	for sc, sci := range info.ReadySCs {
		if s, ok := sci.Address.Attributes.Value(StrategyKey).(string); ok && s != "" {
			strategy = s
		}
		if s, ok := sci.Address.Attributes.Value(ServiceKey).(string); ok && s != "" {
			service = s
		}
		weight := 100
		if w, ok := sci.Address.Attributes.Value(WeightKey).(int); ok && w >= 0 {
			weight = w
		}
		addr := sci.Address.Addr
		nodes = append(nodes, selector.NewNode(addr, "", "", weight, nil))
		scByAddr[addr] = sc
	}

	key := strategy + "\x00" + service
	sel, ok := b.sel.Load(key)
	if !ok {
		sel = newSelector(strategy)
		sel, _ = b.sel.LoadOrStore(key, sel)
	}
	if sel == nil {
		return base.NewErrPicker(fmt.Errorf("balancer: no selector for strategy %q", strategy))
	}

	return &picker{sel: sel.(selector.Selector), scByAddr: scByAddr, nodes: nodes}
}

// newSelector 按策略名返回 selector，未注册时回退 round_robin。
func newSelector(strategy string) selector.Selector {
	sel, err := selector.GetSelector(strategy)
	if err != nil {
		sel, _ = selector.GetSelector("round_robin")
	}
	return sel
}

// picker 是基于 selector.Selector 的 balancer.Picker。
type picker struct {
	sel      selector.Selector
	scByAddr map[string]balancer.SubConn
	nodes    []selector.Node
}

func (p *picker) Pick(info balancer.PickInfo) (balancer.PickResult, error) {
	node, done, err := p.sel.Select(info.Ctx, p.nodes)
	if err != nil {
		return balancer.PickResult{}, err
	}
	sc, ok := p.scByAddr[node.Address()]
	if !ok {
		return balancer.PickResult{}, balancer.ErrNoSubConnAvailable
	}
	return balancer.PickResult{
		SubConn: sc,
		Done: func(di balancer.DoneInfo) {
			if done != nil {
				done(info.Ctx, selector.DoneInfo{Err: di.Err})
			}
		},
	}, nil
}
