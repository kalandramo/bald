package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"google.golang.org/grpc/resolver"

	"github.com/kalandramo/bald/registry"
)

// DiscoveryScheme 是走服务发现拨号时的 URI scheme，目标写作
// `discovery:///<服务名>`（authority 留空，服务名取自 target.Endpoint()，
// 即 URL.Path——避免主机名字符规则把名字里的下划线等字符处理成规范化形式）。
//
// 注入发现来源（`WithDiscovery` 或 `WithDiscoverySource`）后，契约段
// `server.gateway.backend_grpc_addr` 填这个名字即从「配置地址直连」切到
// 「注册中心解析」；填普通 host:port 仍走 gRPC 原生 dns resolver，
// 两种写法在同一个 gateway 里可以并存。
//
// 服务名要按所接注册中心的**真实**名字写。内存与 etcd 用的就是注册时填的
// ServiceInstance.Name；nacos 不是——它把一个实例的多个端点拆成
// `<名字>.<scheme>` 形式的服务（见 registry/nacos/registry.go），故目标得写
// `discovery:///<服务名>.grpc`，少这个后缀解析到的是 HTTP 面或空集。
const DiscoveryScheme = "discovery"

// usesDiscoveryTarget 报告 target 是否要求走服务发现（scheme 前缀判定）。
//
// 与 gRPC 的 target 解析同源：gRPC 只按 URI scheme 选 resolver，前缀在这里是
// 唯一可靠的判据——因此判定与 DiscoveryScheme 定义在同一文件，避免两处漂移。
func usesDiscoveryTarget(target string) bool {
	return strings.HasPrefix(target, DiscoveryScheme+"://")
}

// discoveryBuilder 把 registry.Discovery 适配成 gRPC 的 resolver.Builder。
type discoveryBuilder struct{ discovery registry.Discovery }

// newDiscoveryBuilder 构造按注入 Discovery 解析的 builder。
func newDiscoveryBuilder(d registry.Discovery) resolver.Builder {
	return &discoveryBuilder{discovery: d}
}

func (b *discoveryBuilder) Scheme() string { return DiscoveryScheme }

// Build 同步解析一次并订阅后续变更。
//
// 只有两种情况在这里返回错误：服务名为空（配置写错，重试也不会变好），以及
// Watch 建不起来（没有推送通道，gRPC 的重建 resolver 是唯一恢复路径）。
//
// 首次快照失败**不**返回错误。「后端还没注册」在各注册中心里到底是空集还是错误，
// 是后端的自由：内存与 etcd 返回空集，nacos 直接报 `instance list is empty!`。把它
// 交回 gRPC 等于放弃恢复——resolver 重建只发生在重连退避到期时（实测最长 2 分钟内
// 一次都不会重新解析），实例注册上来了也接不住。所以这里改为「上报原因 + 保留订阅」，
// 由 Watch 的推送把实例带回来，与「解析成功但实例为 0」走同一条路径。
func (b *discoveryBuilder) Build(t resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	service := t.Endpoint()
	if service == "" {
		return nil, fmt.Errorf("gateway: target %s has no service name (want %s:///<name>)", t.URL.String(), DiscoveryScheme)
	}
	ctx, cancel := context.WithCancel(context.Background())
	instances, getErr := b.discovery.GetService(ctx, service)
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
	if getErr != nil {
		r.cc.ReportError(fmt.Errorf("gateway: discovery %s: %w (waiting for watch push)", service, getErr))
	} else {
		r.publish(instances)
	}
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

// addressesOf 展开实例的端点，只留 gRPC 面且剥掉 scheme（见 grpcAddressOf）。
// 一个实例只剩非 gRPC 面时整体出局：与其给出必然拨不通的地址，不如让
// gRPC 报「无实例」并保持订阅。
func addressesOf(instances []*registry.ServiceInstance) []resolver.Address {
	addrs := make([]resolver.Address, 0, len(instances))
	for _, in := range instances {
		for _, ep := range in.Endpoints {
			if addr, ok := grpcAddressOf(ep); ok {
				addrs = append(addrs, resolver.Address{Addr: addr})
			}
		}
	}
	return addrs
}

// grpcAddressOf 把注册端点换成 gRPC 可拨地址。
//
// bald 的 server 按面给端点打 scheme 注册（`transport/grpc` 交 "grpc://host:port"，
// HTTP 面交 "http(s)://host:port"），同一个实例里两类混在一起；gRPC 的拨号器只
// 接受 host:port，带 scheme 的字符串在创建 transport 时直接失败。全仓消费注册端点
// 的地方都先剥前缀（如 `_example` 的 e2e），这里同样不能例外。
func grpcAddressOf(ep string) (string, bool) {
	if ep == "" {
		return "", false
	}
	if rest, ok := strings.CutPrefix(ep, "grpc://"); ok { // "grpc://host:port"
		return rest, true
	}
	// 其余带 scheme 的端点是别的面；裸 host:port（第三方注册中心里手写的实例）照用。
	if strings.Contains(ep, "://") {
		return "", false
	}
	return ep, true
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
