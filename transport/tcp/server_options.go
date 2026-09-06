package tcp

import (
	"crypto/tls"
	"time"

	"github.com/kalandramo/bald/encoding"
	"github.com/kalandramo/bald/metrics"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// Option 是 TCP 服务器的配置选项。
type Option func(*Server)

func WithAddress(addr string) Option {
	return func(s *Server) {
		s.address = addr
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		s.timeout = timeout
	}
}

func WithTLSConfig(c *tls.Config) Option {
	return func(s *Server) {
		s.tlsConf = c
	}
}

func WithCodec(c string) Option {
	return func(s *Server) {
		s.codec = encoding.GetCodec(c)
	}
}

func WithChannelBufferSize(size int) Option {
	return func(_ *Server) {
		channelBufSize = size
	}
}

func WithReceiveBufferSize(size int) Option {
	return func(_ *Server) {
		recvBufferSize = size
	}
}

func WithMessageMarshaler(m NetPacketMarshaler) Option {
	return func(s *Server) {
		s.netPacketMarshaler = m
	}
}

func WithMessageUnmarshaler(m NetPacketUnmarshaler) Option {
	return func(s *Server) {
		s.netPacketUnmarshaler = m
	}
}

func WithSocketConnectHandler(h SocketConnectHandler) Option {
	return func(s *Server) {
		s.socketConnectHandler = h
	}
}

func WithSocketRawDataHandler(h SocketRawDataHandler) Option {
	return func(s *Server) {
		if h != nil {
			s.socketRawDataHandler = h
		}
	}
}

// WithTracer 设置 OpenTelemetry tracer，启用消息级别的链路追踪。
func WithTracer(t trace.Tracer) Option {
	return func(s *Server) {
		s.tracer = t
	}
}

// WithTracerProvider 从全局 TracerProvider 创建并设置 tracer。
func WithTracerProvider() Option {
	return func(s *Server) {
		s.tracer = otel.GetTracerProvider().Tracer("go-wind/plugins/tcp")
	}
}

// WithMetrics 设置 metrics 实现，启用消息级别的指标采集。
func WithMetrics(m metrics.Metrics) Option {
	return func(s *Server) {
		s.m = m
	}
}
