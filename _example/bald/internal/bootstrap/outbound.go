// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// 出站智能路由接线：client 段的 gRPC 出站挂 bald_selector 负载均衡，并在
// picker 装配 selector 时套上按节点熔断（sentinel，错误计数策略）。这是
// 《Bald 节点熔断设计》「接线」一节调用侧三行组合的实例化：
//
//	clientcontract.WithGRPCDialOptions(balancer.DialOption())
//	+ balancer.UseDecorator(套 selector/circuitbreaker)
//
// 语义边界：
//   - client 段缺席时整组接线是空转（Provider 无产物，DialOption 不生效）；
//     有段即生效，调用方不改代码。
//   - 装饰器槽是进程级的（gRPC balancer builder 走全局注册表，Name 固定），
//     必须在首次拨号前设置——本文件在 Options() 求值期完成，先于任何 Serve。
//   - Registry 为进程生命周期：sentinel 后端的 Close 是幂等 no-op，每地址
//     熔断器随进程存活即可；显式 Close 的纪律属于持有可关闭后端的调用方。
//   - 熔断阈值是调用策略，刻意不进契约段（client.proto 的段注释言明拓扑
//     与策略分离）——要改判据改这里，或经 Factory 注入自己的规则表。
package bootstrap

import (
	"fmt"
	"os"
	"strconv"

	scb "github.com/alibaba/sentinel-golang/core/circuitbreaker"

	windcb "github.com/kalandramo/bald/circuitbreaker"
	"github.com/kalandramo/bald/circuitbreaker/sentinel"
	"github.com/kalandramo/bald/selector"
	"github.com/kalandramo/bald/selector/balancer"
	selectorcb "github.com/kalandramo/bald/selector/circuitbreaker"
	clientcontract "github.com/kalandramo/bald/transport/client/contract"
)

// outboundClientOptions 返回 client 段 Provider 的出站策略选项。
//
// 按节点熔断用 sentinel 错误计数策略：10s 统计窗内错误数 ≥3 即跳闸，开窗
// 默认 30s。参数取「演示可观察」的量级——真实服务按 SLO 自己定，契约段
// 不背书。开窗时长可经 OUTBOUND_CB_RETRY_MS 覆盖（与其它演示旋钮
// 同款 env 惯例），端到端测试用它把愈合腿压进亚秒窗口。
func outboundClientOptions() []clientcontract.Option {
	retryMs := uint32(30000)
	if raw := os.Getenv("OUTBOUND_CB_RETRY_MS"); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 32); err == nil && n > 0 {
			retryMs = uint32(n)
		}
	}
	// makeRegistry 构造一份节点熔断 Registry。AR-7：**每次装饰调用各建一份**，
	// 不在闭包外共享——picker 缓存键是 (策略, 服务)，共享 Registry 时 A 服务的
	// Prune 会把 B 服务所有节点的熔断器 Close 并删除，计数反复清零，熔断永不
	// 跳闸。按服务隔离后：Prune 只见本服务候选集；同一下游地址在两个服务各自
	// 的 Registry 里是两个熔断器实例，但 sentinel 资源名按地址派生（全局资源
	// 表），底层失败统计仍按端点共享——簿记隔离、判据一致。
	makeRegistry := func() *selectorcb.Registry {
		return selectorcb.NewRegistry(func(addr string) windcb.CircuitBreaker {
			res := "bald.client.cb." + addr // 资源名按节点派生：规则表不跨节点互相覆盖
			if _, err := scb.LoadRulesOfResource(res, []*scb.Rule{{
				Resource:         res,
				Strategy:         scb.ErrorCount,
				Threshold:        3,
				MinRequestAmount: 1,
				StatIntervalMs:   10000,
				RetryTimeoutMs:   retryMs,
			}}); err != nil {
				// 规则装载失败=这个节点的熔断器没有判据（永不开窗），不是可以
				// 带病上路的形态——演示装配直接 panic，真实服务换成返回错误。
				panic(fmt.Sprintf("outbound: load circuit breaker rules for %s: %v", res, err))
			}
			return sentinel.New(res)
		})
	}
	// 进程级装饰槽：UseDecorator 之后所有 bald_selector picker 的 selector
	// 都经按节点硬排除——跳闸节点直接不进候选集，Done 反馈进 Mark* 结果汇。
	balancer.UseDecorator(func(sel selector.Selector) selector.Selector {
		return selectorcb.WithCircuitBreaker(sel, makeRegistry())
	})
	return []clientcontract.Option{
		// 全部 grpc 出站条目走 bald_selector（策略/权重经实例 Metadata 透传，
		// 见 pkg/grpcdiscovery）。WithEntryGRPCDialOptions 可再按条目覆盖。
		clientcontract.WithGRPCDialOptions(balancer.DialOption()),
	}
}
