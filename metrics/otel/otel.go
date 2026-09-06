// Package otel provides a [metrics.Metrics] implementation backed by the
// OpenTelemetry SDK with OTLP export.
//
// Metrics are exported via OTLP (gRPC or HTTP) to any compatible collector
// (Prometheus via OTLP receiver, Datadog Agent, Grafana Cloud, etc.).
//
// Example:
//
//	m, _ := otel.New(
//	    otel.WithEndpoint("localhost:4317"),
//	    otel.WithServiceName("my-service"),
//	    otel.WithInsecure(true),
//	)
//	defer m.Close()
//
//	m.Counter(ctx, "requests_total", 1, map[string]string{"method": "GET"})
//	m.Histogram(ctx, "request_duration_seconds", 0.042, map[string]string{"method": "GET"})
package otel

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kalandramo/bald/metrics"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

var _ metrics.Metrics = (*Provider)(nil)
var _ metrics.Closer = (*Provider)(nil)

// Option configures the OTLP metrics provider.
type Option func(*config)

type config struct {
	endpoint       string
	serviceName    string
	serviceVersion string
	insecure       bool
	useHTTP        bool
	exportInterval time.Duration
}

func defaultConfig() *config {
	return &config{
		endpoint:       "localhost:4317",
		serviceName:    "go-wind-service",
		serviceVersion: "v0.0.1",
		insecure:       false,
		useHTTP:        false,
		exportInterval: 60 * time.Second,
	}
}

// WithEndpoint sets the OTLP collector endpoint.
func WithEndpoint(endpoint string) Option {
	return func(c *config) { c.endpoint = endpoint }
}

// WithServiceName sets the service name.
func WithServiceName(name string) Option {
	return func(c *config) { c.serviceName = name }
}

// WithServiceVersion sets the service version.
func WithServiceVersion(version string) Option {
	return func(c *config) { c.serviceVersion = version }
}

// WithInsecure disables TLS for the OTLP connection.
func WithInsecure(insecure bool) Option {
	return func(c *config) { c.insecure = insecure }
}

// WithHTTP uses HTTP protocol instead of gRPC.
func WithHTTP(useHTTP bool) Option {
	return func(c *config) { c.useHTTP = useHTTP }
}

// WithExportInterval sets the periodic export interval.
func WithExportInterval(interval time.Duration) Option {
	return func(c *config) { c.exportInterval = interval }
}

// Provider implements [metrics.Metrics] using the OpenTelemetry SDK.
type Provider struct {
	meter    metric.Meter
	mp       *sdkmetric.MeterProvider
	exporter sdkmetric.Exporter

	mu         sync.Mutex
	counters   map[string]metric.Float64Counter
	histograms map[string]metric.Float64Histogram
	gauges     map[string]metric.Float64UpDownCounter
}

// New creates an OTLP-backed metrics provider.
func New(opts ...Option) (*Provider, error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	ctx := context.Background()

	// Create OTLP exporter
	var exporter sdkmetric.Exporter
	var err error

	if cfg.useHTTP {
		httpOpts := []otlpmetrichttp.Option{
			otlpmetrichttp.WithEndpoint(cfg.endpoint),
		}
		if cfg.insecure {
			httpOpts = append(httpOpts, otlpmetrichttp.WithInsecure())
		}
		exporter, err = otlpmetrichttp.New(ctx, httpOpts...)
	} else {
		grpcOpts := []otlpmetricgrpc.Option{
			otlpmetricgrpc.WithEndpoint(cfg.endpoint),
		}
		if cfg.insecure {
			grpcOpts = append(grpcOpts, otlpmetricgrpc.WithInsecure())
		}
		exporter, err = otlpmetricgrpc.New(ctx, grpcOpts...)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP metric exporter: %w", err)
	}

	// Create resource
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.serviceName),
			semconv.ServiceVersion(cfg.serviceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Create meter provider with periodic reader
	reader := sdkmetric.NewPeriodicReader(
		exporter,
		sdkmetric.WithInterval(cfg.exportInterval),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)
	otel.SetMeterProvider(mp)

	return &Provider{
		meter:      mp.Meter(cfg.serviceName),
		mp:         mp,
		exporter:   exporter,
		counters:   make(map[string]metric.Float64Counter),
		histograms: make(map[string]metric.Float64Histogram),
		gauges:     make(map[string]metric.Float64UpDownCounter),
	}, nil
}

// Counter implements [metrics.Metrics].
func (p *Provider) Counter(ctx context.Context, name string, value float64, labels map[string]string) {
	p.mu.Lock()
	counter, ok := p.counters[name]
	if !ok {
		c, err := p.meter.Float64Counter(name)
		if err != nil {
			p.mu.Unlock()
			return
		}
		p.counters[name] = c
		counter = c
	}
	p.mu.Unlock()

	counter.Add(ctx, value, metric.WithAttributes(toAttrs(labels)...))
}

// Histogram implements [metrics.Metrics].
func (p *Provider) Histogram(ctx context.Context, name string, value float64, labels map[string]string) {
	p.mu.Lock()
	hist, ok := p.histograms[name]
	if !ok {
		h, err := p.meter.Float64Histogram(name)
		if err != nil {
			p.mu.Unlock()
			return
		}
		p.histograms[name] = h
		hist = h
	}
	p.mu.Unlock()

	hist.Record(ctx, value, metric.WithAttributes(toAttrs(labels)...))
}

// Gauge implements [metrics.Metrics].
// Note: OpenTelemetry uses observable (callback-based) gauges natively.
// For simplicity, we use an UpDownCounter which provides set-like behavior
// when accumulated per unique label set. For true gauge semantics, consider
// using the raw OTel API directly.
func (p *Provider) Gauge(ctx context.Context, name string, value float64, labels map[string]string) {
	p.mu.Lock()
	gauge, ok := p.gauges[name]
	if !ok {
		g, err := p.meter.Float64UpDownCounter(name)
		if err != nil {
			p.mu.Unlock()
			return
		}
		p.gauges[name] = g
		gauge = g
	}
	p.mu.Unlock()

	gauge.Add(ctx, value, metric.WithAttributes(toAttrs(labels)...))
}

// Close flushes pending metrics and shuts down the exporter.
func (p *Provider) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.mp.Shutdown(ctx)
}

// toAttrs converts a map of labels into OTel attribute key-values.
func toAttrs(labels map[string]string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(labels))
	for k, v := range labels {
		attrs = append(attrs, attribute.String(k, v))
	}
	return attrs
}
