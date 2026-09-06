package sse

import (
	"crypto/tls"
	"time"

	"github.com/kalandramo/bald/encoding"
)

// Option 是 SSE 服务器的配置选项。
type Option func(*Server)

// WithTLSConfig 设置 TLS 配置，启用 HTTPS。
func WithTLSConfig(c *tls.Config) Option {
	return func(s *Server) { s.tlsConfig = c }
}

// WithTLS 从证书和私钥文件加载 TLS 配置，启用 HTTPS。
func WithTLS(certFile, keyFile string) Option {
	return func(s *Server) {
		c, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			panic("sse: LoadX509KeyPair: " + err.Error())
		}
		s.tlsConfig = &tls.Config{Certificates: []tls.Certificate{c}}
	}
}

// WithMiddleware 通过选项设置中间件，允许在创建 Server 时直接传入中间件。
func WithMiddleware(middlewares ...Middleware) Option {
	return func(s *Server) { s.middlewares = append(s.middlewares, middlewares...) }
}

// WithCodec 设置编解码器名称（如 "json"、"proto" 等）。
// codec 会通过 encoding.GetCodec(name) 获取。
func WithCodec(name string) Option {
	return func(s *Server) {
		if name != "" {
			s.codec = encoding.GetCodec(name)
		}
	}
}

// WithPath 设置 SSE 的 HTTP 路由路径（默认 "/events"）。
func WithPath(path string) Option {
	return func(s *Server) { s.path = path }
}

// WithStreamIdKey 设置用于读取流 ID 的查询参数键名（默认 "stream"）。
func WithStreamIdKey(key string) Option {
	return func(s *Server) { s.streamIdKey = key }
}

// WithBufferSize 设置每个流事件通道的缓冲大小（默认 1024）。
func WithBufferSize(size int) Option {
	return func(s *Server) { s.bufferSize = size }
}

// WithEncodeBase64 启用或禁用事件数据的 base64 编码。
func WithEncodeBase64(enable bool) Option {
	return func(s *Server) { s.encodeBase64 = enable }
}

// WithSplitData 启用或禁用按换行符分割事件数据。
func WithSplitData(enable bool) Option {
	return func(s *Server) { s.splitData = enable }
}

// WithAutoStream 启用或禁用自动创建流。
// 启用后，客户端订阅不存在的流时会自动创建。
func WithAutoStream(enable bool) Option {
	return func(s *Server) { s.autoStream = enable }
}

// WithAutoReplay 启用或禁用事件重放。
// 启用后，新订阅者会收到之前的历史事件（基于 Last-Event-ID）。
func WithAutoReplay(enable bool) Option {
	return func(s *Server) { s.autoReplay = enable }
}

// WithEventTTL 设置事件的有效期。
// 超过 TTL 的事件在流式传输时会被跳过。
func WithEventTTL(timeout time.Duration) Option {
	return func(s *Server) { s.eventTTL = timeout }
}

// WithHeaders 设置 SSE 响应的额外 HTTP 头。
func WithHeaders(headers map[string]string) Option {
	return func(s *Server) { s.headers = headers }
}

// WithCORSAllowOrigin 设置 Access-Control-Allow-Origin 头的值（默认 "*"）。
func WithCORSAllowOrigin(origin string) Option {
	return func(s *Server) { s.corsAllowOrigin = origin }
}

// WithSubscriberFunction 设置订阅者加入时的回调函数。
func WithSubscriberFunction(fn SubscriberFunction) Option {
	return func(s *Server) { s.subscribeFunc = fn }
}

// WithUnSubscriberFunction 设置订阅者离开时的回调函数。
func WithUnSubscriberFunction(fn SubscriberFunction) Option {
	return func(s *Server) { s.unsubscribeFunc = fn }
}

// WithTokenExtractor 设置从请求中提取认证 token 的函数。
func WithTokenExtractor(extractor TokenExtractor) Option {
	return func(s *Server) { s.tokenExtractor = extractor }
}

// WithAuthorizeFunc 设置请求授权验证函数。
// 返回 ErrForbidden 产生 403 响应；其他错误产生 401 响应。
func WithAuthorizeFunc(fn AuthorizeFunc) Option {
	return func(s *Server) { s.authorizeFunc = fn }
}
