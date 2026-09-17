package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/mark3labs/mcp-go/server"
)

const (
	DefaultMCPServerName    = "MCP Server"
	DefaultMCPServerVersion = "1.0.0"
	DefaultMCPServerAddress = ":8080"
)

type Server struct {
	mu      sync.RWMutex
	started atomic.Bool

	baseCtx context.Context
	err     error

	serverName    string
	serverVersion string

	mcpServer *server.MCPServer
	endpoint  *url.URL

	mcpOpts []server.ServerOption
	sseOpts []server.SSEOption

	serverType ServerType
	serverAddr string

	// 运行期传输句柄：Start 时填充、Stop 时释放（仅 SSE/HTTP 使用）。
	httpSrv   *http.Server
	sseServer *server.SSEServer
	ln        net.Listener

	// done 在服务实际结束（stdio 退出）或 Stop 完成后关闭，
	// 供上层在 CLI 场景判断"客户端已断开、可以退出进程"。
	done     chan struct{}
	doneOnce sync.Once
}

func NewServer(opts ...ServerOption) *Server {
	srv := &Server{
		baseCtx:       context.Background(),
		started:       atomic.Bool{},
		serverType:    ServerTypeStdio,
		serverVersion: DefaultMCPServerVersion,
		serverName:    DefaultMCPServerName,
		serverAddr:    DefaultMCPServerAddress,
		done:          make(chan struct{}),
	}

	srv.init(opts...)

	return srv
}

func (s *Server) init(opts ...ServerOption) {
	for _, o := range opts {
		o(s)
	}

	switch s.serverType {
	case ServerTypeSSE, ServerTypeHTTP:
		LogInfof("MCP server type set to %s, address: %s", s.serverType, s.serverAddr)
	case ServerTypeInProcess:
		LogInfo("MCP server type set to IN_PROCESS")
	case ServerTypeStdio:
		LogInfo("MCP server type set to STDIO")
	default:
		LogWarnf("Unsupported MCP server type: %s, defaulting to STDIO", s.serverType)
		s.serverType = ServerTypeStdio
	}

	// endpoint 只在 Start 绑定成功后由真实 listener 推导（支持 ":0" 动态端口），
	// 因此 Start 之前 Endpoint() 为空——上层可据此区分"未监听"与"已监听"。

	// Create a new MCP server
	s.mcpServer = server.NewMCPServer(s.serverName, s.serverVersion, s.mcpOpts...)
}

func (s *Server) Name() string {
	return KindMCP
}

func (s *Server) Start(ctx context.Context) error {
	s.mu.RLock()
	if s.err != nil {
		e := s.err
		s.mu.RUnlock()
		return e
	}
	s.mu.RUnlock()

	if s.started.Load() {
		LogWarn("MCP server already started")
		return nil
	}

	s.baseCtx = ctx

	// SSE/HTTP 同步完成端口绑定（绑定失败即刻返回错误，避免"启动成功但没在听"）；
	// stdio 在后台 goroutine 内阻塞服务，in-process 不启动任何传输。
	switch s.serverType {
	case ServerTypeStdio:
		go func() {
			defer s.markDone()
			if serveErr := server.ServeStdio(s.mcpServer); serveErr != nil {
				s.setErr(fmt.Errorf("start MCP stdio server: %w", serveErr))
			}
		}()

	case ServerTypeSSE, ServerTypeHTTP:
		ln, listenErr := net.Listen("tcp", s.serverAddr)
		if listenErr != nil {
			return fmt.Errorf("mcp: listen on %q: %w", s.serverAddr, listenErr)
		}

		var (
			handler http.Handler
			sse     *server.SSEServer
		)
		if s.serverType == ServerTypeSSE {
			sse = server.NewSSEServer(s.mcpServer, s.sseOpts...)
			handler = sse
		} else {
			handler = server.NewStreamableHTTPServer(s.mcpServer)
		}
		httpSrv := &http.Server{Handler: handler}

		s.mu.Lock()
		s.ln = ln
		s.httpSrv = httpSrv
		s.sseServer = sse
		s.endpoint = endpointFromListener(ln, s.serverAddr)
		s.mu.Unlock()

		go func() {
			if serveErr := httpSrv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				s.setErr(fmt.Errorf("serve MCP %s server: %w", s.serverType, serveErr))
			}
		}()

	case ServerTypeInProcess:
	}

	s.started.Store(true)

	LogInfof("MCP server started, [%s][%s][%s] endpoint=%s",
		s.serverName, s.serverVersion, s.serverType, s.Endpoint())

	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	if !s.started.Load() {
		LogWarn("MCP server already stopped")
		return nil
	}

	LogInfof("MCP server stopping, name: %s", s.serverName)

	s.started.Store(false)

	s.mu.Lock()
	httpSrv := s.httpSrv
	sseServer := s.sseServer
	ln := s.ln
	s.httpSrv, s.sseServer, s.ln = nil, nil, nil
	s.mu.Unlock()

	defer s.markDone()

	// SSE 会话是长连接，必须先关闭会话再 Shutdown，否则会一直等到 ctx 超时。
	if sseServer != nil {
		sseServer.CloseSessions()
	}

	// SSE/HTTP 走优雅停机（等待在途请求，超时由 ctx 控制）；
	// stdio 与 in-process 没有监听资源需要释放。
	if httpSrv != nil {
		if shutdownErr := httpSrv.Shutdown(ctx); shutdownErr != nil {
			s.setErr(fmt.Errorf("shutdown MCP server: %w", shutdownErr))
		}
	} else if ln != nil {
		_ = ln.Close()
	}

	s.mu.RLock()
	err := s.err
	s.mu.RUnlock()

	if err != nil {
		LogError("server stopped with error", err)
	} else {
		LogInfo("server stopped.")
	}

	return err
}

func (s *Server) Endpoint() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.endpoint == nil {
		return ""
	}
	return s.endpoint.String()
}

// Done 在服务实际结束（stdio 对端断开）或 Stop 完成后关闭。
// CLI 场景可据此判断"客户端已断开、可以退出进程"。
func (s *Server) Done() <-chan struct{} {
	return s.done
}

func (s *Server) markDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *Server) RegisterHandler(tool mcp.Tool, handler server.ToolHandlerFunc) error {
	if s.mcpServer == nil {
		return errors.New("mcp server is nil")
	}

	s.mcpServer.AddTool(tool, handler)

	return nil
}

func (s *Server) RegisterHandlerWithJsonString(jsonString string, handler server.ToolHandlerFunc) error {
	if s.mcpServer == nil {
		return errors.New("mcp server is nil")
	}

	tool, err := LoadToolFromJsonString(jsonString)
	if err != nil {
		return err
	}

	return s.RegisterHandler(tool, handler)
}

func (s *Server) RegisterHandlerWithJsonSchema(name, description string, jsonSchemaString string, handler server.ToolHandlerFunc) error {
	if s.mcpServer == nil {
		return errors.New("mcp server is nil")
	}

	raw := toRawMessage(jsonSchemaString)

	tool := mcp.NewToolWithRawSchema(name, description, raw)

	return s.RegisterHandler(tool, handler)
}

// endpointFromListener 由真实 listener 推导 endpoint，":0" 动态端口取系统分配端口。
func endpointFromListener(ln net.Listener, addr string) *url.URL {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		host = "localhost"
	}

	port := ""
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		port = strconv.Itoa(tcpAddr.Port)
	}
	if port == "" {
		_, port, _ = net.SplitHostPort(ln.Addr().String())
	}

	return &url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}
}

func (s *Server) setErr(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.err = errors.Join(s.err, err)
	s.mu.Unlock()
}
