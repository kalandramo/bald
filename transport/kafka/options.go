package kafka

import (
	"fmt"

	"crypto/tls"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/kafka"
	"github.com/kalandramo/bald/metrics"
)

// ServerOption 是 kafka 家族服务器的选项面：全部落点都是 kernel 的
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

// WithAddress MQ代理地址
func WithAddress(addrs []string) ServerOption {
	return func(s *Server) {
		s.AddBrokerOpt(broker.WithAddress(addrs...))
	}
}

// WithTLSConfig TLS配置
func WithTLSConfig(c *tls.Config) ServerOption {
	return func(s *Server) {
		if c != nil {
			s.AddBrokerOpt(broker.WithEnableSecure(true))
		}
		s.AddBrokerOpt(broker.WithTLSConfig(c))
	}
}

// WithCodec 编解码器
func WithCodec(c string) ServerOption {
	return func(s *Server) {
		s.AddBrokerOpt(broker.WithCodec(c))
	}
}

// WithPlainMechanism PLAIN认证信息
func WithPlainMechanism(username, password string) ServerOption {
	return func(s *Server) {
		s.AddBrokerOpt(kafka.WithPlainMechanism(username, password))
	}
}

// WithScramMechanism SCRAM认证信息
//
// 未知算法名在 option 构造期即 fail-fast（panic 点名非法值）：透传给
// broker 层会在 xdg-go/scram 内部 nil-deref panic——栈指向库内部而不是
// 配置错误点（"sha256" 小写这类自然笔误当场炸进程且看不出原因）。
func WithScramMechanism(algo string, username, password string) ServerOption {
	switch algo {
	case "SHA256", "SHA512":
	default:
		panic(fmt.Sprintf("kafka: unknown SCRAM algorithm %q (want SHA256 or SHA512; the value is case-sensitive)", algo))
	}
	return func(s *Server) {
		s.AddBrokerOpt(kafka.WithScramMechanism(kafka.ScramAlgorithm(algo), username, password))
	}
}

// WithMetrics 注入指标监控
func WithMetrics(m metrics.Metrics) ServerOption {
	return func(s *Server) {
		s.SetMetrics(m)
	}
}
