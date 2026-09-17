package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/transport"
)

// HTTPServer 封装标准库 net/http，实现 transport.Server 契约。
// 基于 proto 的 bootstrapv1.Server_Http：Tls 段非 nil 时为 HTTPS
// （file/config 双模证书来源，见 resolveTLS）。
//
// 本包只做协议：**不注册任何框架路由**（包括健康/就绪探针），路由表完全由
// 业务 handler 拥有——探针由装配层（`appkit.WithHealth`）或业务自行挂载，
// 见《Bald 健康检查装配设计》。
//
// 并发安全：ln 由 mu 保护。Start 在 AppKit 的 errgroup goroutine 中执行，
// 而 Endpoint() 由 appkit 主 goroutine 轮询（waitForEndpoints），二者并发，
// 无保护时构成数据竞争（go test -race 可复现）。
type HTTPServer struct {
	*http.Server
	cfg *bootstrapv1.Server_Http

	mu sync.RWMutex
	ln net.Listener // 实际监听器，用于解析 Endpoint（支持 :0 动态端口）
}

// NewHTTPServer 基于 http.Handler 构造一个 HTTPServer。
// cfg 为 proto 的 Http 配置：cfg.Tls 非 nil 时启用 HTTPS，否则纯 HTTP。
// handler 为业务根 handler；传 nil 时全部路径 404（gateway 包延迟挂载场景）。
func NewHTTPServer(cfg *bootstrapv1.Server_Http, handler http.Handler) *HTTPServer {
	if handler == nil {
		handler = http.NotFoundHandler()
	}
	return &HTTPServer{
		Server: &http.Server{Addr: cfg.GetAddr(), Handler: handler},
		cfg:    cfg,
	}
}

// Options 返回该 server 直消费的 proto 配置（实现 server.Server 契约的 Options()）。
func (s *HTTPServer) Options() any { return s.cfg }

// AttachHandler 替换业务 handler（覆盖构造时传入的占位 handler）。
// 供 gateway 包在 Start 时才拿到转码 handler 的延迟挂载场景使用。
func (s *HTTPServer) AttachHandler(h http.Handler) {
	if h == nil {
		return
	}
	s.Server.Handler = h
}

// Start 启动 HTTP(S) 服务器（阻塞）。
func (s *HTTPServer) Start(ctx context.Context) error {
	lis, err := net.Listen("tcp", s.cfg.GetAddr())
	if err != nil {
		return fmt.Errorf("http listen %s: %w", s.cfg.GetAddr(), err)
	}

	s.mu.Lock()
	s.ln = lis
	s.mu.Unlock()

	if s.cfg.GetTls() != nil {
		tlsConfig, terr := resolveTLS(s.cfg.GetTls())
		if terr != nil {
			// 监听器已创建，构建 TLS 配置失败属于启动失败路径，
			// 必须关闭监听器，否则造成文件描述符泄漏。
			_ = lis.Close()
			return fmt.Errorf("build http tls config: %w", terr)
		}
		s.TLSConfig = tlsConfig
		err = s.ServeTLS(lis, "", "")
	} else {
		err = s.Serve(lis)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop 优雅停止 HTTP 服务器。
func (s *HTTPServer) Stop(ctx context.Context) error {
	return s.Shutdown(ctx)
}

// Endpoint 返回实际监听地址（支持 ":0" 动态端口）。
// 通配符 / 仅端口绑定（如 ":8080"）会被解析为本机可达 IP，确保注册到服务发现的
// endpoint 对其他节点可直连，而非 "0.0.0.0:8080" 这类不可达通配符。
func (s *HTTPServer) Endpoint() string {
	scheme := scheme(s.cfg.GetTls())

	// 先取出快照再解锁：Extract 内部会枚举网卡（net.Interfaces），
	// 是相对耗时的系统调用，不能在持锁期间执行。
	s.mu.RLock()
	ln := s.ln
	s.mu.RUnlock()

	if ln != nil {
		hostPort, err := transport.Extract(s.cfg.GetAddr(), ln)
		if err == nil {
			return scheme + "://" + hostPort
		}
		return scheme + "://" + ln.Addr().String()
	}
	// 未监听（Start 尚未执行）时返回空字符串，供 appkit.waitForEndpoints 正确等待，
	// 避免把未就绪的地址注册到服务发现。
	return ""
}
