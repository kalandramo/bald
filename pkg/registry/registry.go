// Package registry 定义 bald 框架的服务注册/发现抽象。
//
// 设计理念（对齐 go-wind 的抽象面）：
//   - Registrar 服务注册 + Discovery 服务发现 + Watcher 实例变更监听，
//     不绑定任何具体注册中心（etcd/consul/nacos/kubernetes...）；
//   - 具体后端以直连 SDK 的 provider 形式放 contrib/registry/<backend>
//     （独立 module，依赖隔离，按需 import）；
//   - 契约装配走 bootstrap.RegistrarRegistry（显式 MustRegister，fail-fast）；
//   - 提供内存实现（inmemory）用于开发/测试。
package registry

import "context"

// ServiceInstance 描述一个注册到服务发现中心的应用实例。
type ServiceInstance struct {
	// ID 实例唯一 ID（如 hostname+uuid）。
	ID string `json:"id"`
	// Name 服务名（逻辑名，如 "bald-demo"）。
	Name string `json:"name"`
	// Version 服务版本。
	Version string `json:"version"`
	// Metadata 附加元数据（scheme、region 等）。
	Metadata map[string]string `json:"metadata"`
	// Endpoints 实际可访问地址列表（来自 Server.Endpoint()，支持 :0 动态端口）。
	Endpoints []string `json:"endpoints"`
	// Kind 实例类型（如 "grpc"、"http"、"mixed"）。
	Kind string `json:"kind"`
}

// Registrar 是服务注册中心的最小契约。
type Registrar interface {
	// Register 注册一个实例。
	Register(ctx context.Context, instance *ServiceInstance) error
	// Deregister 注销一个实例（优雅停机时调用，避免流量打到已停服务）。
	Deregister(ctx context.Context, instance *ServiceInstance) error
}

// Discovery 是服务发现契约（客户端按服务名取实例/订阅变更）。
type Discovery interface {
	// GetService 返回服务名下的全部实例（一次性拉取）。
	GetService(ctx context.Context, name string) ([]*ServiceInstance, error)
	// Watch 订阅某服务名的实例变更。
	Watch(ctx context.Context, name string) (Watcher, error)
}

// Watcher 是实例变更监听器：Next 阻塞返回最新全量实例列表，Stop 释放资源。
type Watcher interface {
	Next(ctx context.Context) ([]*ServiceInstance, error)
	Stop() error
}
