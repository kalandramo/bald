// Package transport 提供统一的服务器抽象。
//
// 设计要点：
//   - Server 契约只要求 Start/Stop 两个方法，任何带生命周期的服务器都能满足；
//     在其上扩展 Endpoint() 以支持动态端口（":0"）的服务注册。
//   - 生命周期编排统一由编排层（pkg/appkit）负责：并发启停、优雅停机、
//     Endpoint 动态端口注册。本包只定义契约，不做独立运行入口。
package transport

import "context"

// Server 是统一的服务器生命周期契约。
// Start/Stop 是最小交集，Endpoint() 为服务注册所必需的扩展。
type Server interface {
	// Start 启动服务器（阻塞直到 ctx 取消或出错）。
	Start(ctx context.Context) error
	// Stop 优雅停止服务器（ctx 携带停机超时，且必须是未取消的 ctx）。
	Stop(ctx context.Context) error
	// Endpoint 返回服务器实际监听地址。对绑定 ":0" 的服务器，
	// 必须在 Start 开始接受连接后返回已解析的地址，以便注册到服务发现。
	Endpoint() string
}
