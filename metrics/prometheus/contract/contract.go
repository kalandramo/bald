// Package contract 提供 Prometheus 埋点后端的契约装配：
// bootstrapv1.Instrumentation_Prometheus 段 → metrics.Metrics 实例 +
// 段驱动 Provider（AppProvider，见 provider.go）。
//
// 单独成包的原因（对齐 registry/etcd/contract 模式）：根包保持零契约依赖
// （纯 client_golang 实现），只有本包 import bconf + appkit。
//
// 与契约 metrics 段的分工：metrics 段是请求级 Recorder（中间件自动 emit，
// 维度固定）；本段是传输级埋点（名字与标签由埋点方自定，服务 TCP/MQ 这类
// 没有「请求」概念的路径）。两者可共存于一个进程。
package contract

import (
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	prom "github.com/kalandramo/bald/metrics/prometheus"
)

// Type 是段路径 instrumentation.<本后端> 上的子段名。
const Type = "prometheus"

// Provider 按契约 instrumentation.prometheus 子段构造 Prometheus 埋点后端。
//
// 三行映射，无必给项：namespace/subsystem 留空是合法语义（指标名不加前缀），
// use_default_registry 是 false 时也没有「漏配」——私有 Registry 是默认姿势，
// 消费侧经 Registry() 自行挂载 promhttp.HandlerFor。
//
// cleanup 恒为 nil：client_golang 的 Registry 是进程生命周期对象，没有可关闭
// 的东西；埋点实例随 App 停机直接丢弃。
func Provider(sec *bootstrapv1.Instrumentation_Prometheus) (*prom.Provider, func(), error) {
	if sec == nil {
		return nil, nil, fmt.Errorf("instrumentation.prometheus: 子段为 nil（Provider 按段路径命中，请作为缺陷上报）")
	}

	var opts []prom.Option
	if ns := sec.GetNamespace(); ns != "" {
		opts = append(opts, prom.WithNamespace(ns))
	}
	if sub := sec.GetSubsystem(); sub != "" {
		opts = append(opts, prom.WithSubsystem(sub))
	}

	var (
		p   *prom.Provider
		err error
	)
	// 落点轴：true 走进程默认注册表（标准 promhttp.Handler() 直接可见），
	// false 走实例私有 Registry。这是构造器选择，不是 Option——值本身不可
	// 能映射成「一个 Option」。
	if sec.GetUseDefaultRegistry() {
		p, err = prom.NewWithDefaultRegistry(opts...)
	} else {
		p, err = prom.New(opts...)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("instrumentation.prometheus: %w", err)
	}
	return p, nil, nil
}
