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

	// middlewares 是 SSE/HTTP 传输层最外层的 HTTP 中间件链
	// （按注册顺序从外到内包裹）。stdio / in-process 不适用。
	middlewares []Middleware

	// prmConfig 非 nil 时，在 SSE / Streamable HTTP 形态下启用 RFC 9728 OAuth
	// 资源元数据端点。
	prmConfig *server.ProtectedResourceMetadataConfig

	serverType ServerType
	serverAddr string

	// 运行期传输句柄：Start 时填充、Stop 时释放（仅 SSE/HTTP 使用）。
	httpSrv          *http.Server
	sseServer        *server.SSEServer
	streamableServer *server.StreamableHTTPServer
	ln               net.Listener

	// lnClosedByStop 标记本次 listener Close 是否出自 Stop 的 listen-only 路径：
	// Serve 收到的 accept「use of closed network connection」是预期停机形状，
	// 不记入 s.err（外部对 listener 的关闭仍照记——TestServer_HTTPServeError 钉住）。
	// 不用区分的话，Stop 读 s.err 与 serve goroutine 的 setErr 存在调度竞态，
	// TestServer_StopClosesListenOnly 在 CI 上间歇判红即此。
	lnClosedByStop atomic.Bool

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
		logInfof("MCP server type set to %s, address: %s", s.serverType, s.serverAddr)
	case ServerTypeInProcess:
		logInfo("MCP server type set to IN_PROCESS")
	case ServerTypeStdio:
		logInfo("MCP server type set to STDIO")
	default:
		logWarnf("Unsupported MCP server type: %s, defaulting to STDIO", s.serverType)
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
		logWarn("MCP server already started")
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
			handler    http.Handler
			sse        *server.SSEServer
			streamable *server.StreamableHTTPServer
		)
		if s.serverType == ServerTypeSSE {
			sseOpts := s.sseOpts
			if s.prmConfig != nil {
				sseOpts = append(sseOpts, server.WithSSEProtectedResourceMetadata(*s.prmConfig))
			}
			sse = server.NewSSEServer(s.mcpServer, sseOpts...)
			handler = sse
		} else {
			var streamableOpts []server.StreamableHTTPOption
			if s.prmConfig != nil {
				streamableOpts = append(streamableOpts, server.WithProtectedResourceMetadata(*s.prmConfig))
			}
			streamable = server.NewStreamableHTTPServer(s.mcpServer, streamableOpts...)
			handler = streamable
		}

		// 应用中间件链：从后往前包裹，使注册顺序 = 执行顺序
		// （与 transport/http3 的 Use 一致）。stdio/in-process 无 HTTP 层，不适用。
		for i := len(s.middlewares) - 1; i >= 0; i-- {
			handler = s.middlewares[i](handler)
		}

		httpSrv := &http.Server{Handler: handler}

		s.mu.Lock()
		s.ln = ln
		s.httpSrv = httpSrv
		s.sseServer = sse
		s.streamableServer = streamable
		s.endpoint = endpointFromListener(ln, s.serverAddr)
		s.mu.Unlock()

		go func() {
			// ErrServerClosed：Shutdown 自己触发，预期停机；net.ErrClosed 仅在
			// lnClosedByStop（Stop 的 listen-only 路径自己关的）时同为预期——
			// 外部关闭 listener 的 accept 错误照记（TestServer_HTTPServeError）。
			if serveErr := httpSrv.Serve(ln); serveErr != nil &&
				!errors.Is(serveErr, http.ErrServerClosed) &&
				!(errors.Is(serveErr, net.ErrClosed) && s.lnClosedByStop.Load()) {
				s.setErr(fmt.Errorf("serve MCP %s server: %w", s.serverType, serveErr))
			}
		}()

	case ServerTypeInProcess:
	}

	s.started.Store(true)

	logInfof("MCP server started, [%s][%s][%s] endpoint=%s",
		s.serverName, s.serverVersion, s.serverType, s.Endpoint())

	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	if !s.started.Load() {
		logWarn("MCP server already stopped")
		return nil
	}

	logInfof("MCP server stopping, name: %s", s.serverName)

	s.started.Store(false)

	s.mu.Lock()
	httpSrv := s.httpSrv
	sseServer := s.sseServer
	streamableServer := s.streamableServer
	ln := s.ln
	s.httpSrv, s.sseServer, s.streamableServer, s.ln = nil, nil, nil, nil
	s.mu.Unlock()

	defer s.markDone()

	// SSE 与 Streamable HTTP 的会话都是长连接，必须先关闭会话再 Shutdown，
	// 否则会一直等到 ctx 超时。
	if sseServer != nil {
		sseServer.CloseSessions()
	}
	if streamableServer != nil {
		streamableServer.CloseSessions(ctx)
	}

	// SSE/HTTP 走优雅停机（等待在途请求，超时由 ctx 控制）；
	// stdio 与 in-process 没有监听资源需要释放。
	if httpSrv != nil {
		if shutdownErr := httpSrv.Shutdown(ctx); shutdownErr != nil {
			s.setErr(fmt.Errorf("shutdown MCP server: %w", shutdownErr))
		}
	} else if ln != nil {
		// listen-only：Stop 自己关 listener——Serve 的 accept 关闭错误由
		// lnClosedByStop 标记豁免（见 Start 的 serve goroutine），Stop 此处
		// 因此确定性返回 s.err 的既有值，不与 goroutine 调度赛跑。
		s.lnClosedByStop.Store(true)
		_ = ln.Close()
	}

	s.mu.RLock()
	err := s.err
	s.mu.RUnlock()

	if err != nil {
		logError("server stopped with error", err)
	} else {
		logInfo("server stopped.")
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

// RegisterResource 在底层 MCP 服务端注册一个资源及其读取处理器。
//
// 与 RegisterHandler 一样是对 mcp-go 的薄封装：resources/list 与
// resources/read 的协议帧、能力声明、分页都由 mcp-go 完成，本层不介入
// 资源内容的语义（URI 编解码、MIME 类型与序列化格式由调用方决定）。
//
// 纯增量：不注册任何资源时，initialize 响应中不会声明 resources 能力，
// 客户端行为与本方法引入之前完全一致。
func (s *Server) RegisterResource(resource mcp.Resource, handler server.ResourceHandlerFunc) error {
	if s.mcpServer == nil {
		return errors.New("mcp server is nil")
	}

	s.mcpServer.AddResource(resource, handler)

	return nil
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
