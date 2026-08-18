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

// HTTPServer 封装标准库 net/http，实现 Server 契约。
// 支持纯 HTTP 或 HTTPS（基于 options.TLSOptions）。
type HTTPServer struct {
	*http.Server
	opts *baldoptions.HTTPOptions

	ln net.Listener // 实际监听器，用于解析 Endpoint（支持 :0 动态端口）
}

// NewHTTPServer 基于 http.Handler 构造一个 HTTPServer。
func NewHTTPServer(opts *baldoptions.HTTPOptions, handler http.Handler) *HTTPServer {
	return &HTTPServer{
		Server: &http.Server{
			Addr:    opts.Addr,
			Handler: handler,
		},
		opts: opts,
	}
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
