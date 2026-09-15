// Package prometheus provides a [metrics.Metrics] implementation backed by the
// Prometheus client_golang library.
//
// Metrics are lazily registered with the default Prometheus registry on first
// use. A /metrics HTTP endpoint can be exposed by mounting the standard
// promhttp.Handler().
//
// Example:
//
//	m, _ := prometheus.New(prometheus.WithNamespace("myapp"))
//	m.Counter(ctx, "requests_total", 1, map[string]string{"method": "GET"})
//
//	// Expose /metrics endpoint
//	http.Handle("/metrics", promhttp.Handler())
//	http.ListenAndServe(":9090", nil)
package prometheus

import (
	"context"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/kalandramo/bald/metrics"
)

var _ metrics.Metrics = (*Provider)(nil)

// Option configures the Prometheus metrics provider.
type Option func(*config)

type config struct {
	namespace string
	subsystem string
	registry  prometheus.Registerer
}

func defaultConfig() *config {
	return &config{
		registry: nil, // resolved by the constructor
	}
}

// WithNamespace sets the metric namespace prefix.
func WithNamespace(ns string) Option {
	return func(c *config) { c.namespace = ns }
}

// WithSubsystem sets the metric subsystem prefix.
func WithSubsystem(sub string) Option {
	return func(c *config) { c.subsystem = sub }
}

// WithRegistry sets a custom Prometheus registerer (useful for testing).
// It applies to both New and NewWithDefaultRegistry.
func WithRegistry(r prometheus.Registerer) Option {
	return func(c *config) { c.registry = r }
}

// Provider implements [metrics.Metrics] using the Prometheus client library.
// Metrics are created lazily and cached so that subsequent calls with the same
// name/label-set reuse the existing instrument. All methods are safe for
// concurrent use.
type Provider struct {
	cfg     *config
	factory promauto.Factory

	mu            sync.Mutex
	counters      map[string]prometheus.Counter
	counterVecs   map[string]*prometheus.CounterVec
	histograms    map[string]prometheus.Histogram
	histogramVecs map[string]*prometheus.HistogramVec
	gauges        map[string]prometheus.Gauge
	gaugeVecs     map[string]*prometheus.GaugeVec
	labelNames    map[string][]string
}

// New creates a Prometheus-backed metrics provider with its own private
// registry (isolated from prometheus.DefaultRegisterer). Pass WithRegistry to
// override, e.g. for tests.
func New(opts ...Option) (*Provider, error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.registry == nil {
		cfg.registry = prometheus.NewRegistry()
	}
	return newProvider(cfg), nil
}

// NewWithDefaultRegistry creates a provider using the default Prometheus
// registry (prometheus.DefaultRegisterer). Use this when you want the metrics
// to be exposed via the standard promhttp.Handler().
func NewWithDefaultRegistry(opts ...Option) (*Provider, error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.registry == nil {
		cfg.registry = prometheus.DefaultRegisterer
	}
	return newProvider(cfg), nil
}

func newProvider(cfg *config) *Provider {
	return &Provider{
		cfg:           cfg,
		factory:       promauto.With(cfg.registry),
		counters:      make(map[string]prometheus.Counter),
		counterVecs:   make(map[string]*prometheus.CounterVec),
		histograms:    make(map[string]prometheus.Histogram),
		histogramVecs: make(map[string]*prometheus.HistogramVec),
		gauges:        make(map[string]prometheus.Gauge),
		gaugeVecs:     make(map[string]*prometheus.GaugeVec),
		labelNames:    make(map[string][]string),
	}
}

// Registry returns the Prometheus gatherer used by this provider.
// Useful for passing to promhttp.HandlerFor().
func (p *Provider) Registry() prometheus.Gatherer {
	if r, ok := p.cfg.registry.(*prometheus.Registry); ok {
		return r
	}
	return prometheus.DefaultGatherer
}

func (p *Provider) labelKeys(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	return keys
}

func (p *Provider) labelValues(labels map[string]string, keys []string) []string {
	vals := make([]string, len(keys))
	for i, k := range keys {
		vals[i] = labels[k]
	}
	return vals
}

// cachedLabelKeys returns the label keys registered for name. The key set is
// frozen on first use: Prometheus requires a stable label dimension per metric
// name, so keys seen later that were absent on first use are silently dropped.
// Callers must keep label keys constant for a given name.
//
// Must be called with p.mu held.
func (p *Provider) cachedLabelKeys(name string, labels map[string]string) []string {
	if keys, ok := p.labelNames[name]; ok {
		return keys
	}
	keys := p.labelKeys(labels)
	p.labelNames[name] = keys
	return keys
}

// Counter implements [metrics.Metrics].
func (p *Provider) Counter(ctx context.Context, name string, value float64, labels map[string]string) {
	_ = ctx
	if len(labels) == 0 {
		p.mu.Lock()
		c, ok := p.counters[name]
		if !ok {
			c = p.factory.NewCounter(prometheus.CounterOpts{
				Namespace: p.cfg.namespace,
				Subsystem: p.cfg.subsystem,
				Name:      name,
			})
			p.counters[name] = c
		}
		p.mu.Unlock()
		c.Add(value)
		return
	}

	p.mu.Lock()
	keys := p.cachedLabelKeys(name, labels)
	cv, ok := p.counterVecs[name]
	if !ok {
		cv = p.factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: p.cfg.namespace,
			Subsystem: p.cfg.subsystem,
			Name:      name,
		}, keys)
		p.counterVecs[name] = cv
	}
	p.mu.Unlock()
	cv.WithLabelValues(p.labelValues(labels, keys)...).Add(value)
}

// Histogram implements [metrics.Metrics].
func (p *Provider) Histogram(ctx context.Context, name string, value float64, labels map[string]string) {
	_ = ctx
	if len(labels) == 0 {
		p.mu.Lock()
		h, ok := p.histograms[name]
		if !ok {
			h = p.factory.NewHistogram(prometheus.HistogramOpts{
				Namespace: p.cfg.namespace,
				Subsystem: p.cfg.subsystem,
				Name:      name,
			})
			p.histograms[name] = h
		}
		p.mu.Unlock()
		h.Observe(value)
		return
	}

	p.mu.Lock()
	keys := p.cachedLabelKeys(name, labels)
	hv, ok := p.histogramVecs[name]
	if !ok {
		hv = p.factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: p.cfg.namespace,
			Subsystem: p.cfg.subsystem,
			Name:      name,
		}, keys)
		p.histogramVecs[name] = hv
	}
	p.mu.Unlock()
	hv.WithLabelValues(p.labelValues(labels, keys)...).Observe(value)
}

// gauge returns the cached single gauge for name, creating it if needed.
func (p *Provider) gauge(name string) prometheus.Gauge {
	p.mu.Lock()
	defer p.mu.Unlock()
	g, ok := p.gauges[name]
	if !ok {
		g = p.factory.NewGauge(prometheus.GaugeOpts{
			Namespace: p.cfg.namespace,
			Subsystem: p.cfg.subsystem,
			Name:      name,
		})
		p.gauges[name] = g
	}
	return g
}

// gaugeVecWith returns the cached gauge vec for name together with the frozen
// label keys, creating the vec if needed.
func (p *Provider) gaugeVecWith(name string, labels map[string]string) (*prometheus.GaugeVec, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys := p.cachedLabelKeys(name, labels)
	gv, ok := p.gaugeVecs[name]
	if !ok {
		gv = p.factory.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: p.cfg.namespace,
			Subsystem: p.cfg.subsystem,
			Name:      name,
		}, keys)
		p.gaugeVecs[name] = gv
	}
	return gv, keys
}

// Gauge implements [metrics.Metrics]. It sets the absolute value.
func (p *Provider) Gauge(ctx context.Context, name string, value float64, labels map[string]string) {
	_ = ctx
	if len(labels) == 0 {
		p.gauge(name).Set(value)
		return
	}
	gv, keys := p.gaugeVecWith(name, labels)
	gv.WithLabelValues(p.labelValues(labels, keys)...).Set(value)
}

// GaugeAdd implements [metrics.Metrics]. It adds delta to the current value.
func (p *Provider) GaugeAdd(ctx context.Context, name string, delta float64, labels map[string]string) {
	_ = ctx
	if len(labels) == 0 {
		p.gauge(name).Add(delta)
		return
	}
	gv, keys := p.gaugeVecWith(name, labels)
	gv.WithLabelValues(p.labelValues(labels, keys)...).Add(delta)
}
