// Package datadog provides a [metrics.Metrics] implementation backed by the
// Datadog statsd client (DogStatsD).
//
// Metrics are sent via UDP to a local Datadog Agent (DogStatsD) which forwards
// them to Datadog's backend.
//
// Example:
//
//	m, _ := datadog.New(
//	    datadog.WithAddress("127.0.0.1:8125"),
//	    datadog.WithNamespace("myapp"),
//	)
//	defer m.Close()
//
//	m.Counter(ctx, "requests_total", 1, map[string]string{"method": "GET"})
//	m.Histogram(ctx, "request_duration_seconds", 0.042, map[string]string{"method": "GET"})
package datadog

import (
	"context"
	"fmt"
	"time"

	"github.com/kalandramo/bald/metrics"

	"github.com/DataDog/datadog-go/v5/statsd"
)

var _ metrics.Metrics = (*Provider)(nil)
var _ metrics.Closer = (*Provider)(nil)

// Option configures the Datadog metrics provider.
type Option func(*config)

type config struct {
	address     string
	namespace   string
	bufferSize  int
	flushPeriod time.Duration
	rate        float64 // sample rate (1.0 = all)
}

func defaultConfig() *config {
	return &config{
		address:     "127.0.0.1:8125",
		namespace:   "",
		bufferSize:  0, // 0 = no buffering
		flushPeriod: 100 * time.Millisecond,
		rate:        1.0,
	}
}

// WithAddress sets the DogStatsD agent address (host:port).
func WithAddress(addr string) Option {
	return func(c *config) { c.address = addr }
}

// WithNamespace sets a prefix prepended to all metric names.
func WithNamespace(ns string) Option {
	return func(c *config) { c.namespace = ns }
}

// WithBufferSize sets the UDP buffer size (in bytes) for batched sends.
func WithBufferSize(size int) Option {
	return func(c *config) { c.bufferSize = size }
}

// WithFlushPeriod sets the flush interval for buffered metrics.
func WithFlushPeriod(period time.Duration) Option {
	return func(c *config) { c.flushPeriod = period }
}

// WithSampleRate sets the sample rate (0.0 to 1.0).
func WithSampleRate(rate float64) Option {
	return func(c *config) { c.rate = rate }
}

// Provider implements [metrics.Metrics] using DogStatsD.
type Provider struct {
	client *statsd.Client
	rate   float64
}

// New creates a Datadog statsd-backed metrics provider.
func New(opts ...Option) (*Provider, error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	clientOpts := []statsd.Option{
		statsd.WithNamespace(cfg.namespace),
	}
	if cfg.bufferSize > 0 {
		clientOpts = append(clientOpts,
			statsd.WithMaxMessagesPerPayload(cfg.bufferSize),
		)
	}

	client, err := statsd.New(cfg.address, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create DogStatsD client: %w", err)
	}

	return &Provider{
		client: client,
		rate:   cfg.rate,
	}, nil
}

func toTags(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	tags := make([]string, 0, len(labels))
	for k, v := range labels {
		tags = append(tags, fmt.Sprintf("%s:%s", k, v))
	}
	return tags
}

// Counter implements [metrics.Metrics].
func (p *Provider) Counter(ctx context.Context, name string, value float64, labels map[string]string) {
	_ = ctx
	_ = p.client.Count(name, int64(value), toTags(labels), p.rate)
}

// Histogram implements [metrics.Metrics].
func (p *Provider) Histogram(ctx context.Context, name string, value float64, labels map[string]string) {
	_ = ctx
	_ = p.client.Histogram(name, value, toTags(labels), p.rate)
}

// Gauge implements [metrics.Metrics].
func (p *Provider) Gauge(ctx context.Context, name string, value float64, labels map[string]string) {
	_ = ctx
	_ = p.client.Gauge(name, value, toTags(labels), p.rate)
}

// Close flushes pending metrics and closes the statsd connection.
func (p *Provider) Close() error {
	return p.client.Close()
}
