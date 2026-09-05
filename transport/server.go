// Package server 提供统一的服务器抽象，融合 onexstack 的协议层与 Kratos 的运行期治理。
//
// 设计要点：
//   - Server 契约与 Kratos 的 transport.Server（Start/Stop）签名兼容，
//     方法集是其超集（额外 Endpoint()），因此既可由本框架 AppKit 编排，
//     也能直接塞进 Kratos App。
//   - Serve 提供独立的生命周期管理：启动 -> 阻塞等待 ctx 取消 -> 带超时的优雅停机，
//     可作为不使用 AppKit 时的轻量单服务器运行入口。
package transport

import (
	"context"
	"os/signal"
	"syscall"
	"time"
)

// Server 是统一的服务器生命周期契约。
// 兼容 github.com/go-kratos/kratos/v3/transport.Server（Start/Stop），
// 并扩展 Endpoint() 以支持动态端口（":0"）的服务注册。
type Server interface {
	// Start 启动服务器（阻塞直到 ctx 取消或出错）。
	Start(ctx context.Context) error
	// Stop 优雅停止服务器（ctx 携带停机超时，且必须是未取消的 ctx）。
	Stop(ctx context.Context) error
	// Endpoint 返回服务器实际监听地址。对绑定 ":0" 的服务器，
	// 必须在 Start 开始接受连接后返回已解析的地址，以便注册到服务发现。
	Endpoint() string
}

// GracefulTimeout 是默认优雅停机超时时间。
const GracefulTimeout = 10 * time.Second

// Serve 以独立方式运行单个服务器：
//  1. 在独立 goroutine 中 Start；
//  2. 阻塞直到收到 SIGINT/SIGTERM 或传入的 ctx 被取消；
//  3. 在 grace 超时内执行 Stop。
//
// 适用于不想引入 kratos.App 的轻量场景；多服务器并发编排请用 AppKit（pkg/appkit）。
func Serve(ctx context.Context, srv Server, grace time.Duration) error {
	if grace <= 0 {
		grace = GracefulTimeout
	}

	// 监听系统信号，收到后取消 ctx，触发停机。
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		_ = srv.Start(context.Background())
	}()

	<-ctx.Done()

	stopCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

	return srv.Stop(stopCtx)
}
