package metrics

import "context"

// Metrics defines the metrics-reporting abstractions for the bald framework.
//
// It provides a minimal, engine-agnostic interface for recording application
// metrics. Concrete implementations (Prometheus, OpenTelemetry, Datadog, etc.)
// implement this interface so that business code depends only on the contract.
type Metrics interface {
	// Counter increments a monotonically increasing counter by value.
	Counter(ctx context.Context, name string, value float64, labels map[string]string)

	// Histogram records an observation into a histogram distribution.
	Histogram(ctx context.Context, name string, value float64, labels map[string]string)

	// Gauge sets the current value of a gauge.
	Gauge(ctx context.Context, name string, value float64, labels map[string]string)
}

// Closer extends [Metrics] with graceful shutdown for providers that hold
// background resources (exporter flush goroutines, network connections, etc.).
type Closer interface {
	Close() error
}
