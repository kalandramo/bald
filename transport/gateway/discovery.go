package gateway

import (
	"context"
	"fmt"
	"sync/atomic"

	"google.golang.org/grpc/resolver"

	"github.com/kalandramo/bald/registry"
)

// DiscoveryScheme 是走服务发现拨号时的 URI scheme，目标写作
// `discovery:///<服务名>`（authority 留空，服务名取自 target.Endpoint()，
// 即 URL.Path——避免主机名字符规则把名字里的下划线等字符处理成规范化形式）。
//
// 注入 `WithDiscovery` 后，契约段 `server.gateway.backend_grpc_addr` 填这个名字
// 即从「配置地址直连」切到「注册中心解析」；填普通 host:port 仍走 gRPC 原生
// dns resolver，两种写法在同一个 gateway 里可以并存。
const DiscoveryScheme = "discovery"

// discoveryBuilder 把 registry.Discovery 适配成 gRPC 的 resolver.Builder。
type discoveryBuilder struct{ discovery registry.Discovery }

// newDiscoveryBuilder 构造按注入 Discovery 解析的 builder。
func newDiscoveryBuilder(d registry.Discovery) resolver.Builder {
	return &discoveryBuilder{discovery: d}
}

func (b *discoveryBuilder) Scheme() string { return DiscoveryScheme }

// Build 同步解析一次并订阅后续变更。
//
// 服务名为空或首次 GetService 失败在这里返回错误：拨号方（gRPC）会把 ClientConn
// 置为 TransientFailure 并按退避重建 resolver，等价于「稍后重试解析」。
// 而「解析成功但实例为 0」不报错——后端尚未注册是启动期的正常状态，报错会让
// 首次 RPC 直接失败；这里改为 ReportError 并保持订阅，等实例出现时自动生效。
func (b *discoveryBuilder) Build(t resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	service := t.Endpoint()
	if service == "" {
		return nil, fmt.Errorf("gateway: target %s has no service name (want %s:///<name>)", t.URL.String(), DiscoveryScheme)
	}
	ctx, cancel := context.WithCancel(context.Background())
	instances, err := b.discovery.GetService(ctx, service)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("gateway: discovery %s: %w", service, err)
	}
	watch, err := b.discovery.Watch(ctx, service)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("gateway: watch %s: %w", service, err)
	}
	r := &discoveryResolver{
		service: service,
		cc:      cc,
		watch:   watch,
		ctx:     ctx,
		cancel:  cancel,
	}
	r.publish(instances)
	go r.run()
	return r, nil
}

// discoveryResolver 是 registry.Discovery 的 gRPC resolver 适配。
type discoveryResolver struct {
	service string
	cc      resolver.ClientConn
	watch   registry.Watcher
	// ctx/cancel 结束阻塞中的 Next；后端 watcher 是否尊重 ctx 属实现自由，
	// 因此 run 还看 closed 才决定是否上报错误。
	ctx    context.Context
	cancel context.CancelFunc
	closed atomic.Bool
}

// publish 把实例列表换成 resolver 状态。空实例集不调用 UpdateState：
// gRPC 对空地址集会回 ErrBadResolverState，且会把已有的健康地址一起清掉。
func (r *discoveryResolver) publish(instances []*registry.ServiceInstance) {
	addrs := addressesOf(instances)
	if len(addrs) == 0 {
		r.cc.ReportError(fmt.Errorf("gateway: discovery %s: no instances", r.service))
		return
	}
	// 本实现是 watch-based：UpdateState 失败不需要自己重解析，下一次变更会再发。
	// 但错误必须上报，否则「解析对了却推不进 ClientConn」会变成静默故障。
	if err := r.cc.UpdateState(resolver.State{Addresses: addrs}); err != nil {
		r.cc.ReportError(fmt.Errorf("gateway: publish %s: %w", r.service, err))
	}
}

// addressesOf 展开实例的端点。空端点跳过：实例存在但没写出可拨地址，
// 与其产出一个必然失败的地址，不如让 gRPC 报「无实例」。
func addressesOf(instances []*registry.ServiceInstance) []resolver.Address {
	addrs := make([]resolver.Address, 0, len(instances))
	for _, in := range instances {
		for _, ep := range in.Endpoints {
			if ep == "" {
				continue
			}
			addrs = append(addrs, resolver.Address{Addr: ep})
		}
	}
	return addrs
}

// run 把 Watch 的每次全量快照转成 UpdateState，直到 Close 或订阅出错。
func (r *discoveryResolver) run() {
	for {
		instances, err := r.watch.Next(r.ctx)
		if r.closed.Load() {
			return // Close 触发的返回不是故障，不该污染 ClientConn 状态
		}
		if err != nil {
			// 订阅侧错误不等于「没有后端」：报告一次并退出，让 gRPC 按
			// TransientFailure 重建 resolver，而不是在这里空转打满 CPU。
			r.cc.ReportError(fmt.Errorf("gateway: watch %s: %w", r.service, err))
			return
		}
		r.publish(instances)
	}
}

// ResolveNow 无需做事：变更由 Watch 推送，不存在「被动刷新」的时刻。
func (r *discoveryResolver) ResolveNow(resolver.ResolveNowOptions) {}

// Close 结束订阅并释放 watcher。Close 后 run 因 Next 返回错误自行退出。
func (r *discoveryResolver) Close() {
	if !r.closed.CompareAndSwap(false, true) {
		return
	}
	r.cancel()
	_ = r.watch.Stop()
}
