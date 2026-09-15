package otel

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// newTestProvider builds a Provider backed by a ManualReader. New sets the
// global MeterProvider, so the previous one is saved and restored on cleanup.
func newTestProvider(t *testing.T) (*Provider, *sdkmetric.ManualReader) {
	t.Helper()

	prev := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	reader := sdkmetric.NewManualReader()
	p, err := New(withReader(reader))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, reader
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) []metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	var out []metricdata.Metrics
	for _, sm := range rm.ScopeMetrics {
		out = append(out, sm.Metrics...)
	}
	return out
}

func findMetric(t *testing.T, ms []metricdata.Metrics, name string) metricdata.Metrics {
	t.Helper()
	for _, m := range ms {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("metric %q not found", name)
	return metricdata.Metrics{}
}

func TestGaugeSetSemantics(t *testing.T) {
	p, reader := newTestProvider(t)
	ctx := context.Background()

	p.Gauge(ctx, "g", 10, nil)
	p.Gauge(ctx, "g", 25, nil) // Record overwrites the previous value

	m := findMetric(t, collect(t, reader), "g")
	g, ok := m.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("g: data type %T, want Gauge", m.Data)
	}
	if len(g.DataPoints) != 1 {
		t.Fatalf("g: %d data points, want 1", len(g.DataPoints))
	}
	if got := g.DataPoints[0].Value; got != 25 {
		t.Fatalf("g = %v, want 25 (Set semantics: last write wins)", got)
	}
}

func TestGaugeAddSemantics(t *testing.T) {
	p, reader := newTestProvider(t)
	ctx := context.Background()

	p.GaugeAdd(ctx, "inflight", 1, nil)
	p.GaugeAdd(ctx, "inflight", 1, nil)
	p.GaugeAdd(ctx, "inflight", -1, nil)

	m := findMetric(t, collect(t, reader), "inflight")
	sum, ok := m.Data.(metricdata.Sum[float64])
	if !ok {
		t.Fatalf("inflight: data type %T, want Sum", m.Data)
	}
	if len(sum.DataPoints) != 1 {
		t.Fatalf("inflight: %d data points, want 1", len(sum.DataPoints))
	}
	if got := sum.DataPoints[0].Value; got != 1 {
		t.Fatalf("inflight = %v, want 1 (Add semantics: deltas accumulate)", got)
	}
}

func TestCounterAccumulates(t *testing.T) {
	p, reader := newTestProvider(t)
	ctx := context.Background()

	p.Counter(ctx, "c", 1, nil)
	p.Counter(ctx, "c", 2, nil)

	m := findMetric(t, collect(t, reader), "c")
	sum, ok := m.Data.(metricdata.Sum[float64])
	if !ok {
		t.Fatalf("c: data type %T, want Sum", m.Data)
	}
	if got := sum.DataPoints[0].Value; got != 3 {
		t.Fatalf("c = %v, want 3", got)
	}
}

func TestHistogramRecords(t *testing.T) {
	p, reader := newTestProvider(t)
	ctx := context.Background()

	p.Histogram(ctx, "h", 0.5, nil)
	p.Histogram(ctx, "h", 1.5, nil)

	m := findMetric(t, collect(t, reader), "h")
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("h: data type %T, want Histogram", m.Data)
	}
	if got := h.DataPoints[0].Count; got != 2 {
		t.Fatalf("h count = %v, want 2", got)
	}
}

func TestGaugeWithLabels(t *testing.T) {
	p, reader := newTestProvider(t)
	ctx := context.Background()

	p.Gauge(ctx, "g", 5, map[string]string{"a": "1"})
	p.Gauge(ctx, "g", 9, map[string]string{"a": "2"})

	m := findMetric(t, collect(t, reader), "g")
	g, ok := m.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("g: data type %T, want Gauge", m.Data)
	}
	if len(g.DataPoints) != 2 {
		t.Fatalf("g: %d data points, want 2 (one per label set)", len(g.DataPoints))
	}
}
