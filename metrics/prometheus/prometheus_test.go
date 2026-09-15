package prometheus

import (
	"context"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// hasMetric reports whether g exposes a metric family named name.
func hasMetric(t *testing.T, g prometheus.Gatherer, name string) bool {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return true
		}
	}
	return false
}

// gatherValue returns the value of the single sample of name from g.
func gatherValue(t *testing.T, g prometheus.Gatherer, name string) float64 {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		if len(mf.GetMetric()) != 1 {
			t.Fatalf("metric %q: expected 1 sample, got %d", name, len(mf.GetMetric()))
		}
		m := mf.GetMetric()[0]
		switch {
		case m.GetGauge() != nil:
			return m.GetGauge().GetValue()
		case m.GetCounter() != nil:
			return m.GetCounter().GetValue()
		case m.GetHistogram() != nil:
			return float64(m.GetHistogram().GetSampleCount())
		}
	}
	t.Fatalf("metric %q not found", name)
	return 0
}

func TestNewUsesPrivateRegistry(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	p.Counter(ctx, "priv_total", 1, nil)

	if hasMetric(t, prometheus.DefaultGatherer, "priv_total") {
		t.Fatal("metric leaked into default registry")
	}
	if got := gatherValue(t, p.Registry(), "priv_total"); got != 1 {
		t.Fatalf("priv_total = %v, want 1", got)
	}
}

func TestWithRegistryEffective(t *testing.T) {
	reg := prometheus.NewRegistry()
	p, err := New(WithRegistry(reg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	p.Counter(ctx, "custom_total", 3, nil)
	if got := gatherValue(t, reg, "custom_total"); got != 3 {
		t.Fatalf("custom_total = %v, want 3 (WithRegistry ignored by New)", got)
	}

	reg2 := prometheus.NewRegistry()
	p2, err := NewWithDefaultRegistry(WithRegistry(reg2))
	if err != nil {
		t.Fatalf("NewWithDefaultRegistry: %v", err)
	}
	p2.Gauge(ctx, "custom_gauge", 42, nil)
	if got := gatherValue(t, reg2, "custom_gauge"); got != 42 {
		t.Fatalf("custom_gauge = %v, want 42 (WithRegistry ignored by NewWithDefaultRegistry)", got)
	}
}

func TestGaugeSetSemantics(t *testing.T) {
	p, _ := New()
	ctx := context.Background()
	p.Gauge(ctx, "g", 10, nil)
	p.Gauge(ctx, "g", 25, nil)
	if got := gatherValue(t, p.Registry(), "g"); got != 25 {
		t.Fatalf("gauge = %v, want 25 (Set semantics: last write wins)", got)
	}
}

func TestGaugeAddSemantics(t *testing.T) {
	p, _ := New()
	ctx := context.Background()
	p.GaugeAdd(ctx, "inflight", 1, nil)
	p.GaugeAdd(ctx, "inflight", 1, nil)
	p.GaugeAdd(ctx, "inflight", -1, nil)
	if got := gatherValue(t, p.Registry(), "inflight"); got != 1 {
		t.Fatalf("inflight = %v, want 1 (Add semantics: deltas accumulate)", got)
	}
}

func TestGaugeAndGaugeAddShareInstrument(t *testing.T) {
	p, _ := New()
	ctx := context.Background()
	p.Gauge(ctx, "mixed", 5, map[string]string{"a": "1"})
	p.GaugeAdd(ctx, "mixed", 2, map[string]string{"a": "1"})
	if got := gatherValue(t, p.Registry(), "mixed"); got != 7 {
		t.Fatalf("mixed = %v, want 7 (Set 5 then Add 2)", got)
	}
}

func TestConcurrentUse(t *testing.T) {
	p, _ := New()
	ctx := context.Background()
	const workers = 16
	const iters = 100

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				p.Counter(ctx, "c_total", 1, map[string]string{"w": "x"})
				p.Histogram(ctx, "h_seconds", 0.1, nil)
				p.Gauge(ctx, "g", float64(j), nil)
				p.GaugeAdd(ctx, "inflight", 1, map[string]string{"k": "v"})
			}
		}()
	}
	wg.Wait()

	if got := gatherValue(t, p.Registry(), "c_total"); got != workers*iters {
		t.Fatalf("c_total = %v, want %d", got, workers*iters)
	}
	if got := gatherValue(t, p.Registry(), "inflight"); got != workers*iters {
		t.Fatalf("inflight = %v, want %d", got, workers*iters)
	}
}

func TestLabelKeysFrozenOnFirstUse(t *testing.T) {
	p, _ := New()
	ctx := context.Background()
	p.Counter(ctx, "f_total", 1, map[string]string{"a": "1"})
	p.Counter(ctx, "f_total", 1, map[string]string{"a": "1", "b": "2"}) // b silently dropped

	mfs, err := p.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "f_total" {
			continue
		}
		if len(mf.GetMetric()) != 1 {
			t.Fatalf("f_total samples = %d, want 1 (late label key dropped)", len(mf.GetMetric()))
		}
		labelPairs := mf.GetMetric()[0].GetLabel()
		if len(labelPairs) != 1 || labelPairs[0].GetName() != "a" {
			t.Fatalf("labels = %v, want only [a]", labelPairs)
		}
		if got := gatherValue(t, p.Registry(), "f_total"); got != 2 {
			t.Fatalf("f_total = %v, want 2", got)
		}
		return
	}
	t.Fatal("f_total not found")
}
