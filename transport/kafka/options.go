package kafka

import (
	"crypto/tls"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/kafka"
	"github.com/kalandramo/bald/metrics"
)

type ServerOption func(o *Server)

// WithBrokerOptions MQ代理配置
func WithBrokerOptions(opts ...broker.Option) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, opts...)
	}
}

// WithAddress MQ代理地址
func WithAddress(addrs []string) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, broker.WithAddress(addrs...))
	}
}

// WithTLSConfig TLS配置
func WithTLSConfig(c *tls.Config) ServerOption {
	return func(s *Server) {
		if c != nil {
			s.brokerOpts = append(s.brokerOpts, broker.WithEnableSecure(true))
		}
		s.brokerOpts = append(s.brokerOpts, broker.WithTLSConfig(c))
	}
}

// WithCodec 编解码器
func WithCodec(c string) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, broker.WithCodec(c))
	}
}

// WithPlainMechanism PLAIN认证信息
func WithPlainMechanism(username, password string) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, kafka.WithPlainMechanism(username, password))
	}
}

// WithScramMechanism SCRAM认证信息
func WithScramMechanism(algo string, username, password string) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, kafka.WithScramMechanism(kafka.ScramAlgorithm(algo), username, password))
	}
}

// WithMetrics 注入指标监控
func WithMetrics(m metrics.Metrics) ServerOption {
	return func(s *Server) {
		s.m = m
	}
}
