// Package graphql provides a GraphQL server based on [gqlgen] that implements
// the [transport.Server] interface.
//
// GraphQL runs over HTTP, so this server wraps a standard [http.Server] with
// a [http.ServeMux] for routing. It supports the same [Middleware] type as
// transport/http, allowing middleware (recovery, logging, request-id, etc.)
// to be shared between HTTP and GraphQL servers.
//
// Usage:
//
//	import (
//	    graphqlServer "github.com/kalandramo/bald/transport/graphql"
//	    "github.com/99designs/gqlgen/graphql/handler"
//	)
//
//	srv := graphqlServer.NewServer(":8080")
//	srv.Handle("/query", api.NewExecutableSchema(api.Config{Resolvers: &resolver{}}))
//
//	if err := srv.Start(ctx); err != nil { ... }
package graphql

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"

	"github.com/kalandramo/bald/transport"
)

const KindGraphQL = "graphql"

// 确保 Server 实现了 wind transport.Server 接口。
var _ transport.Server = (*Server)(nil)

// Middleware 是标准 HTTP 中间件类型。
// 使用类型别名（而非命名类型）使得 transport/http/middleware 下的中间件
// 可以直接传给 GraphQL server，无需类型转换：
//
//	srv.Use(recovery.Middleware())   // recovery 返回 httpPlugin.Middleware
//	srv.Use(logging.Middleware())
//	srv.Use(requestid.Middleware())
type Middleware = func(http.Handler) http.Handler

// Server 是基于 gqlgen 的 GraphQL 服务器，实现 transport.Server 接口。
// addr / tlsConfig / mux / middlewares 只在 Start 之前写（Use 与 Handle 的
// 前置约定），listener 与 server 是运行期状态，由 mu 保护。
type Server struct {
	addr        string
	tlsConfig   *tls.Config
	mux         *http.ServeMux
	middlewares []Middleware

	// mu 保护 listener 与 server：Start 在调用方 goroutine 里写入，
	// Stop / Endpoint 由装配面在另一个 goroutine 读取（停机与端点等待
	// 都会并发命中）。
	mu       sync.RWMutex
	listener net.Listener
	server   *http.Server
}

// NewServer 创建一个 GraphQL 服务器实例。
func NewServer(addr string, opts ...Option) *Server {
	srv := &Server{
		addr: addr,
		mux:  http.NewServeMux(),
	}
	for _, opt := range opts {
		opt(srv)
	}
	return srv
}

// Handle 注册一个 GraphQL schema 到指定路径。
func (s *Server) Handle(path string, es graphql.ExecutableSchema) {
	s.mux.Handle(path, handler.New(es))
}

// HandleFunc 注册普通 HTTP 处理器（如 /playground 等辅助页面）。
func (s *Server) HandleFunc(path string, h http.HandlerFunc) {
	s.mux.HandleFunc(path, h)
}

// Use 注册全局中间件，对所有路由生效。
// 中间件按注册顺序执行：先注册的中间件最先被调用。
// 必须在 Start 之前调用。
func (s *Server) Use(middlewares ...Middleware) {
	s.middlewares = append(s.middlewares, middlewares...)
}

// Start 启动 GraphQL 服务器，阻塞直到 ctx 被取消。
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	if s.tlsConfig != nil {
		ln = tls.NewListener(ln, s.tlsConfig)
	}

	// 应用中间件链
	h := http.Handler(s.mux)
	for i := len(s.middlewares) - 1; i >= 0; i-- {
		h = s.middlewares[i](h)
	}

	hs := &http.Server{Handler: h}
	hs.BaseContext = func(net.Listener) context.Context { return ctx }

	s.mu.Lock()
	s.listener = ln
	s.server = hs
	s.mu.Unlock()

	fmt.Printf("[%s] server listening on: %s\n", KindGraphQL, ln.Addr().String())

	errChan := make(chan error, 1)
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
			return
		}
		errChan <- nil
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return hs.Shutdown(context.Background())
	}
}

// Stop 优雅关闭服务器。
func (s *Server) Stop(ctx context.Context) error {
	s.mu.RLock()
	hs := s.server
	s.mu.RUnlock()
	if hs == nil {
		return nil
	}
	return hs.Shutdown(ctx)
}

// Endpoint 返回服务器的访问地址。
func (s *Server) Endpoint() string {
	scheme := "http"
	if s.tlsConfig != nil {
		scheme = "https"
	}
	addr := s.addr
	s.mu.RLock()
	ln := s.listener
	s.mu.RUnlock()
	if ln != nil {
		addr = ln.Addr().String()
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return scheme + "://" + addr
	}
	if host == "" || host == "0.0.0.0" {
		host = "localhost"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// Addr 返回配置的监听地址。
func (s *Server) Addr() string { return s.addr }
