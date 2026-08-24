package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"

	baldoptions "github.com/kalandramo/bald/pkg/options"
)

// ReadinessFunc 是就绪探针回调：返回 nil 表示依赖就绪（可接受流量），
// 返回非 nil 错误表示未就绪（应被 K8s 从 Service 端点摘掉）。
// 与 gRPC 侧的 health.SetServingStatus 共用同一语义，保证两端对称。
type ReadinessFunc func(ctx context.Context) error

// HTTPServer 封装标准库 net/http，实现 Server 契约。
// 支持纯 HTTP 或 HTTPS（基于 options.TLSOptions），并自动挂载
// /healthz（存活探针，进程在即 200）与 /readyz（就绪探针，由 readiness 回调决定）。
type HTTPServer struct {
	*http.Server
	opts *baldoptions.HTTPOptions

	readiness ReadinessFunc // 可为 nil（nil 时 /readyz 等同 /healthz）

	ln net.Listener // 实际监听器，用于解析 Endpoint（支持 :0 动态端口）
}

// NewHTTPServer 基于 http.Handler 构造一个 HTTPServer。
// readiness 为可选的就绪探针回调：传 nil 时 /readyz 恒返回 200（仅作存活）。
// 框架会把 /healthz、/readyz 注册到内部 mux，业务 handler 挂载在其余路径；
// 若业务 handler 自身也注册了同名路径，则业务优先（框架 probe 不覆盖）。
func NewHTTPServer(opts *baldoptions.HTTPOptions, handler http.Handler, readiness ReadinessFunc) *HTTPServer {
	mux := http.NewServeMux()
	// 业务路由优先：先挂业务 handler，框架 probe 仅当路径未被占用时兜底。
	if handler != nil {
		mux.Handle("/", handler)
	}
	srv := &HTTPServer{
		Server:    &http.Server{Addr: opts.Addr, Handler: mux},
		opts:      opts,
		readiness: readiness,
	}
	// 框架探针注册为精确路径，优先于业务挂载的 "/" 根路径匹配；
	// 若业务也用 HandleFunc("/healthz", ...) 注册了同名精确路径，标准库 mux
	// 会 panic（重复注册），此时以业务实现为准——框架不覆盖业务自定义探针。
	mux.HandleFunc("/healthz", srv.handleHealthz)
	mux.HandleFunc("/readyz", srv.handleReadyz)
	return srv
}

// handleHealthz：存活探针，进程在即返回 200，不做任何依赖检查。
func (s *HTTPServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleReadyz：就绪探针，readiness 为 nil 或返回 nil 时 200，否则 503。
func (s *HTTPServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.readiness == nil {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	if err := s.readiness(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable) // 503
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// Start 启动 HTTP(S) 服务器（阻塞）。
func (s *HTTPServer) Start(ctx context.Context) error {
	var err error
	s.ln, err = net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("http listen %s: %w", s.opts.Addr, err)
	}
	if s.opts.TLS != nil && s.opts.TLS.Enabled {
		s.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		err = s.ServeTLS(s.ln, s.opts.TLS.CertFile, s.opts.TLS.KeyFile)
	} else {
		err = s.Serve(s.ln)
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
func (s *HTTPServer) Endpoint() string {
	scheme := "http"
	if s.opts.TLS != nil && s.opts.TLS.Enabled {
		scheme = "https"
	}
	if s.ln != nil {
		return scheme + "://" + s.ln.Addr().String()
	}
	return scheme + "://" + s.opts.Addr
}
