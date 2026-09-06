// Package metrics defines the metrics-reporting abstractions for the
// go-wind framework.
//
// It provides a minimal, engine-agnostic interface for recording application
// metrics. Concrete implementations (Prometheus, OpenTelemetry, Datadog, etc.)
// implement this interface so that business code depends only on the contract.
//
// The three core metric types are:
//
//   - Counter — monotonically increasing value (request count, error count).
//   - Histogram — distribution of observations (latency, payload size).
//   - Gauge — arbitrary point-in-time value (queue depth, active connections).
//
// Example usage:
//
//	m, _ := prometheus.New(prometheus.WithNamespace("myapp"))
//	m.Counter("requests_total", 1, map[string]string{"method": "GET"})
//	m.Histogram("request_duration_seconds", 0.042, map[string]string{"method": "GET"})
//	m.Gauge("queue_depth", 42, map[string]string{"queue": "email"})
package metrics
