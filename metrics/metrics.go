package metrics

import "context"

// Metrics defines the metrics-reporting abstractions for the bald framework.
//
// It provides a minimal, engine-agnostic interface for recording application
// metrics. Concrete implementations (Prometheus, OpenTelemetry, Datadog, etc.)
// implement this interface so that business code depends only on the contract.
//
// The label keys passed for a given metric name MUST stay constant across
// calls: backends that pre-declare label dimensions (e.g. Prometheus) freeze
// the key set on first use and silently drop keys not seen then.
type Metrics interface {
	// Counter increments a monotonically increasing counter by value.
	Counter(ctx context.Context, name string, value float64, labels map[string]string)

	// Histogram records an observation into a histogram distribution.
	Histogram(ctx context.Context, name string, value float64, labels map[string]string)

	// Gauge sets the current value of a gauge (Set semantics: value is the
	// new absolute reading, not a delta). For increment-style gauges such as
	// in-flight counters, use GaugeAdd.
	Gauge(ctx context.Context, name string, value float64, labels map[string]string)

	// GaugeAdd adds delta to a gauge (increment semantics).
	GaugeAdd(ctx context.Context, name string, delta float64, labels map[string]string)
}

// Closer extends [Metrics] with graceful shutdown for providers that hold
// background resources (exporter flush goroutines, network connections, etc.).
type Closer interface {
	Close() error
}
