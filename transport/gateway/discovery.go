package gateway

import (
	"google.golang.org/grpc/resolver"

	"github.com/kalandramo/bald/pkg/grpcdiscovery"
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
// resolver 实现住在 pkg/grpcdiscovery（client 段的 grpc 出站复用同一份），
// 这里只保留 gateway 自己的判定与接线口径。
const DiscoveryScheme = grpcdiscovery.Scheme

// usesDiscoveryTarget 报告 target 是否要求走服务发现（scheme 前缀判定）。
//
// 判定与 Scheme 同源（pkg/grpcdiscovery）——gateway 与 client 出站共用一份，
// 不再各写一条前缀规则。
func usesDiscoveryTarget(target string) bool {
	return grpcdiscovery.UsesTarget(target)
}

// newDiscoveryBuilder 构造按注入 Discovery 解析的 builder。
func newDiscoveryBuilder(d registry.Discovery) resolver.Builder {
	return grpcdiscovery.New(d)
}
