package rocketmq

import (
	"crypto/tls"

	rocketmqOption "github.com/kalandramo/bald/broker/rocketmq/option"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/metrics"
)

type ServerOption func(o *Server)

// WithBrokerOptions MQ代理配置
func WithBrokerOptions(opts ...broker.Option) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, opts...)
	}
}

func WithTLSConfig(c *tls.Config) ServerOption {
	return func(s *Server) {
		if c != nil {
			s.brokerOpts = append(s.brokerOpts, broker.WithEnableSecure(true))
		}
		s.brokerOpts = append(s.brokerOpts, broker.WithTLSConfig(c))
	}
}

func WithEnableTrace() ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, rocketmqOption.WithEnableTrace())
	}
}

func WithNameServer(addrs []string) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, rocketmqOption.WithNameServer(addrs))
	}
}

func WithNameServerDomain(uri string) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, rocketmqOption.WithNameServerDomain(uri))
	}
}

func WithCredentials(accessKey, secretKey, securityToken string) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, rocketmqOption.WithCredentials(accessKey, secretKey, securityToken))
	}
}

func WithNamespace(ns string) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, rocketmqOption.WithNamespace(ns))
	}
}

func WithInstanceName(name string) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, rocketmqOption.WithInstanceName(name))
	}
}

func WithGroupName(name string) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, rocketmqOption.WithGroupName(name))
	}
}

func WithRetryCount(count int) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, rocketmqOption.WithRetryCount(count))
	}
}

func WithCodec(c string) ServerOption {
	return func(s *Server) {
		s.brokerOpts = append(s.brokerOpts, broker.WithCodec(c))
	}
}

// WithMetrics 注入指标监控
func WithMetrics(m metrics.Metrics) ServerOption {
	return func(s *Server) {
		s.m = m
	}
}
