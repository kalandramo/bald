// Package registry 定义 bald 框架的服务注册中心抽象。
//
// 设计理念（对齐 go-lulu 的去耦思路）：
//   - 自带轻量 Registrar 接口，不绑定任何具体注册中心（etcd/consul/nacos...）；
//   - 提供内存实现（inmemory）用于开发/测试；
//   - 通过 kratos.go 桥接适配器兼容 go-kratos 的 registry.Registrar，
//     使既有 kratos 后端（etcd/consul）可即插即用。
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
