// discovery.go —— 段驱动装配把已装配的注册中心接到 gateway 出站发现。
//
// 解决的问题：注册中心通常已经装配进 App（registry 段 Provider 的产物，或显式
// appkit.Registrar(...) 实例），但段 Provider 的产物面只声明 registry.Registrar
// 这半边契约——消费方要用发现能力（registry.Discovery）只能对同一个注册中心
// 再连一次客户端。接线后业务只改一行配置：backend_grpc_addr 写成
// discovery:///<服务名>。
//
// 为什么走「延迟来源」而不是在 Build 里派生好再传 WithDiscovery：
//   - 地址的判定属于拨号侧（gateway 自己有 DiscoveryScheme），契约层再解析一次
//     就是同一规则的两份实现；
//   - scope.App 是活指针：server 段在 late 钩子 Build，业务自己的 BeforeStart
//     之后仍可能 SetRegistrar（NewApp 路径把注册中心交应用自持即如此）。Build
//     时派生会把「那一刻没有」固化成错误，Start 时取到的才是真正在用的实例；
//   - 调用方显式注入的 WithDiscovery 与自动来源同时存在时，谁生效只能由
//     gateway 按「显式实例优先」裁决，选项顺序猜不出来。
//
// HTTPProvider 不接这根线：它的上游是 server.grpc.addr——本进程 gRPC 面自己的
// 监听地址。把监听地址写成 discovery:/// 是配置错误而不是发现意图，报错应停在
// 「没有发现来源」这一层。要让同端口转码面指向远端，显式传 gateway.WithDiscovery。
package contract

import (
	"fmt"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/registry"
	gateway "github.com/kalandramo/bald/transport/gateway"
)

// discoveryScopeOption 返回「按装配作用域取 Discovery」的 gateway 选项。
//
// 无条件附加：地址不是 discovery scheme 时 gateway 不会调用本闭包，零开销。
// 排在调用方 opts 之前（gateway 侧显式 WithDiscovery 优先于本来源）。
func discoveryScopeOption(s appkit.ApplyScope) gateway.Option {
	return gateway.WithDiscoverySource(func() (registry.Discovery, error) {
		return discoveryFromScope(s)
	})
}

// discoveryFromScope 取出已装配注册中心的发现半边。
//
// 先读 App：registry 段的产物只落在 App 上（阶段 B 的 SetRegistrar），
// Decl.Registrar 只承载显式实例那条路；Decl 作回退供无 App 的调用面
// （手工 Build 的测试/编排器）使用。
func discoveryFromScope(s appkit.ApplyScope) (registry.Discovery, error) {
	var r registry.Registrar
	if s.App != nil {
		r = s.App.Registrar()
	}
	if r == nil && s.Decl != nil {
		r = s.Decl.Registrar
	}
	if r == nil {
		return nil, fmt.Errorf("no registry is assembled (declare the registry segment or pass appkit.Registrar(...))")
	}
	d, ok := r.(registry.Discovery)
	if !ok {
		return nil, fmt.Errorf("assembled registrar %T does not implement registry.Discovery", r)
	}
	return d, nil
}
