package mcp

import (
	"net/http"

	"github.com/mark3labs/mcp-go/server"
)

type ServerType string

type ServerOption func(o *Server)

// Middleware 是标准 HTTP 中间件类型。
// 与 transport/http3、transport/graphql、transport/sse 的 Middleware 别名一致
// （均为 func(http.Handler) http.Handler），因此这些包的中间件可直接复用。
type Middleware = func(http.Handler) http.Handler

const (
	ServerTypeSSE       ServerType = "SSE"
	ServerTypeHTTP      ServerType = "HTTP"
	ServerTypeStdio     ServerType = "STDIO"
	ServerTypeInProcess ServerType = "IN_PROCESS"
)

func WithServerName(name string) ServerOption {
	return func(s *Server) {
		s.serverName = name
	}
}

func WithServerVersion(version string) ServerOption {
	return func(s *Server) {
		s.serverVersion = version
	}
}

func WithMCPServerOptions(opts ...server.ServerOption) ServerOption {
	return func(s *Server) {
		s.mcpOpts = append(s.mcpOpts, opts...)
	}
}

// WithSSEOptions 追加 SSE 传输选项（如 server.WithBaseURL 指定对外可见基址）。
func WithSSEOptions(opts ...server.SSEOption) ServerOption {
	return func(s *Server) {
		s.sseOpts = append(s.sseOpts, opts...)
	}
}

// WithMiddleware 追加 HTTP 中间件，在 SSE/HTTP 传输层最外层执行。
// 按注册顺序从外到内包裹（先注册的先执行），与 transport/http3 的 Use 语义一致。
//
// 典型用途：认证——校验 Authorization 头，失败时返回 401 并中断请求
// （不调用 next）。stdio / in-process 形态无 HTTP 层，中间件不生效。
func WithMiddleware(mw ...Middleware) ServerOption {
	return func(s *Server) {
		s.middlewares = append(s.middlewares, mw...)
	}
}

// WithSSEProtectedResourceMetadata 配置 RFC 9728 的 OAuth 2.0 资源元数据端点
// （仅 SSE 形态生效），透传给底层 mcp-go 的 SSEServer。元数据在
// ProtectedResourceMetadataPath(cfg.Resource) 派生的 well-known 路径上暴露。
//
// 注意：该端点由 mcp-go 在 SSEServer 内部按路径分发，而 WithMiddleware 的
// 中间件包在其外层——若同时启用认证，中间件必须放行该 well-known 路径
// （公开发现端点，客户端未持 token 时也需访问），否则 OAuth 发现流程死锁。
func WithSSEProtectedResourceMetadata(cfg server.ProtectedResourceMetadataConfig) ServerOption {
	return func(s *Server) {
		s.prmConfig = &cfg
	}
}

func WithMCPServeType(serverType ServerType) ServerOption {
	return func(s *Server) {
		s.serverType = serverType
	}
}

func WithMCPServeAddress(addr string) ServerOption {
	return func(s *Server) {
		s.serverAddr = addr
	}
}
