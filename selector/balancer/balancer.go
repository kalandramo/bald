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
	"strconv"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/resolver"

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
// gRPC 面（不只 HTTP）生效。经 pkg/grpcdiscovery 从实例 Metadata 透传时是
// 字符串（Metadata 是 map[string]string），故 attrsOf 同时接受 int 与可解析
// 为 int 的 string。
const WeightKey = "bald.selector.weight"

// RegisterDefaultStrategies 把四个内置策略显式注册进 selector 全局注册表
// （bald 习语：显式注册而非 blank import）。手动装配时与 Register 搭配：
//
//	balancer.RegisterDefaultStrategies()
//	balancer.Register()
//
// 拨号侧通常直接用 DialOption（内部经 Activate 幂等完成这两步），无需手调。
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
// 重复注册 panic（grpc 语义）——进程内只调一次。多数调用方经 DialOption 使用，
// 不必直接调本函数。
func Register() {
	balancer.Register(base.NewBalancerBuilder(Name, &pickerBuilder{}, base.Config{HealthCheck: false}))
}

// DefaultServiceConfig 让 gRPC 客户端把 bald_selector 选为负载均衡策略。
// resolver 未提供 service config 时作为默认值生效（discovery/dns 都不提供），
// 因此挂它即可覆盖 grpc 内置的 pick_first。
const DefaultServiceConfig = `{"loadBalancingConfig":[{"` + Name + `":{}}]}`

// activateOnce 把「注册默认策略 + 注册 builder」收敛成进程内一次。DialOption
// 每次拨号都会走到这里，once 保证 Register 不会因重复注册而 panic。
var activateOnce sync.Once

// Activate 幂等地注册默认策略与 bald_selector builder。DialOption 内部已调用；
// 只在自行装配 service config（不用 DialOption）时才需要显式调它。
// 直接调用 Register/RegisterDefaultStrategies 的调用方无需再调本函数。
func Activate() {
	activateOnce.Do(func() {
		RegisterDefaultStrategies()
		Register()
	})
}

// DialOption 产出激活 bald_selector 的拨号选项：先幂等注册，再返回带上默认
// service config 的 grpc.WithDefaultServiceConfig。与 resiliency 同款「调用侧
// 显式组合」——契约段只描述拓扑，负载均衡策略由调用方挂上：
//
//	clientcontract.WithGRPCDialOptions(balancer.DialOption())
//
// 策略/权重按节点从实例 Metadata 透传（见 pkg/grpcdiscovery.attributesOf）：
// 运维给实例打 `bald.selector.strategy`/`bald.selector.weight`，picker 即按此
// 选策略与加权。不带这些键即 round_robin、权重 100。
func DialOption() grpc.DialOption {
	Activate()
	return grpc.WithDefaultServiceConfig(DefaultServiceConfig)
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
		attrs := attrsOf(sci.Address)
		if attrs.strategy != "" {
			strategy = attrs.strategy
		}
		if attrs.service != "" {
			service = attrs.service
		}
		weight := 100
		if attrs.hasWeight {
			weight = attrs.weight
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

// nodeAttrs 是从一个 resolver.Address 读出的选择参数。strategy/service 空串
// 表示该地址未携带（由调用方决定回退值）；hasWeight 区分「权重 0」与「无权重」。
type nodeAttrs struct {
	strategy, service string
	weight            int
	hasWeight         bool
}

// attrsOf 读一个地址的属性表（nil 安全）。策略/服务名按字符串读；权重同时
// 接受 int 与字符串——经 pkg/grpcdiscovery 从实例 Metadata 透传时是字符串，
// 直接构造属性表的调用方可给 int。非正或不可解析的权重视为缺失（hasWeight=false），
// 交回默认 100。
func attrsOf(a resolver.Address) nodeAttrs {
	var na nodeAttrs
	attrs := a.Attributes
	if attrs == nil {
		return na
	}
	if s, ok := attrs.Value(StrategyKey).(string); ok {
		na.strategy = s
	}
	if s, ok := attrs.Value(ServiceKey).(string); ok {
		na.service = s
	}
	switch w := attrs.Value(WeightKey).(type) {
	case int:
		if w >= 0 {
			na.weight, na.hasWeight = w, true
		}
	case string:
		if n, err := strconv.Atoi(w); err == nil && n >= 0 {
			na.weight, na.hasWeight = n, true
		}
	}
	return na
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
