package rabbitmq

import (
	"crypto/tls"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/rabbitmq"
	"github.com/kalandramo/bald/metrics"
)

// ServerOption 是 rabbitmq 家族服务器的选项面：全部落点都是 kernel 的
// brokerOpts 收集面/metrics 注入（生命周期在 kernel）。
type ServerOption func(o *Server)

// WithBrokerOptions MQ代理配置
func WithBrokerOptions(opts ...broker.Option) ServerOption {
	return func(s *Server) {
		for _, o := range opts {
			s.AddBrokerOpt(o)
		}
	}
}

func WithAddress(addrs []string) ServerOption {
	return func(s *Server) {
		s.AddBrokerOpt(broker.WithAddress(addrs...))
	}
}

func WithTLSConfig(c *tls.Config) ServerOption {
	return func(s *Server) {
		if c != nil {
			s.AddBrokerOpt(broker.WithEnableSecure(true))
		}
		s.AddBrokerOpt(broker.WithTLSConfig(c))
	}
}

func WithExchange(name string, durable bool) ServerOption {
	return func(s *Server) {
		s.AddBrokerOpt(rabbitmq.WithExchangeName(name))
		if durable {
			s.AddBrokerOpt(rabbitmq.WithDurableExchange())
		}
	}
}

func WithCodec(c string) ServerOption {
	return func(s *Server) {
		s.AddBrokerOpt(broker.WithCodec(c))
	}
}

// WithMetrics 注入指标监控
func WithMetrics(m metrics.Metrics) ServerOption {
	return func(s *Server) {
		s.SetMetrics(m)
	}
}
