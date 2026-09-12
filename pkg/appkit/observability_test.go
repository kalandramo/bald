package appkit

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// stub tracer provider：记录调用并返回可断言的 shutdown。
func stubTracerProvider(called *bool, sd func(context.Context) error) TracerProvider {
	return func(_ context.Context, _ *bootstrapv1.Tracer) (func(context.Context) error, error) {
		*called = true
		return sd, nil
	}
}

func TestTracerRegistry_RegisterValidation(t *testing.T) {
	r := NewTracerRegistry()
	if err := r.Register("", stubTracerProvider(new(bool), nil)); err == nil {
		t.Error("empty type should fail")
	}
	if err := r.Register("otlp", nil); err == nil {
		t.Error("nil provider should fail")
	}
	p := stubTracerProvider(new(bool), nil)
	if err := r.Register("otlp", p); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register("otlp", p); err == nil {
		t.Error("duplicate register should fail")
	}
}

func TestTracerRegistry_Build(t *testing.T) {
	r := NewTracerRegistry()

	// 段缺省：no-op shutdown，不触 provider。
	called := false
	r.MustRegister("otlp", stubTracerProvider(&called, nil))
	sd, err := r.Build(context.Background(), nil)
	if err != nil {
		t.Fatalf("nil section: %v", err)
	}
	if sd == nil {
		t.Fatal("nil section should yield non-nil no-op shutdown")
	}
	if err := sd(context.Background()); err != nil {
		t.Fatalf("no-op shutdown: %v", err)
	}
	if called {
		t.Error("nil section must not invoke provider")
	}

	// 段存在但 type 空：fail-fast（显式主开关契约）。
	if _, err := r.Build(context.Background(), &bootstrapv1.Tracer{}); err == nil {
		t.Error("empty type should fail")
	}

	// type 未注册：fail-fast。
	if _, err := r.Build(context.Background(), &bootstrapv1.Tracer{Type: "zipkin"}); err == nil {
		t.Error("unregistered type should fail")
	}

	// 命中：透传 provider 产物。
	want := errors.New("boom")
	got := errors.New("x")
	r2 := NewTracerRegistry()
	r2.MustRegister("otlp", func(_ context.Context, _ *bootstrapv1.Tracer) (func(context.Context) error, error) {
		called = true
		return func(context.Context) error { return want }, nil
	})
	sd, err = r2.Build(context.Background(), &bootstrapv1.Tracer{Type: "otlp"})
	if err != nil || !called {
		t.Fatalf("hit: err=%v called=%v", err, called)
	}
	if got = sd(context.Background()); !errors.Is(got, want) {
		t.Fatalf("shutdown passthrough: %v", got)
	}
}

func stubMetricsProvider(called *bool) MetricsProvider {
	return func(_ context.Context, _ *bootstrapv1.Metrics) (http.Handler, func(context.Context) error, error) {
		*called = true
		return http.NotFoundHandler(), func(context.Context) error { return nil }, nil
	}
}

func TestMetricsRegistry_Build(t *testing.T) {
	r := NewMetricsRegistry()
	called := false
	r.MustRegister("prometheus", stubMetricsProvider(&called))
	r.MustRegister("otlp", stubMetricsProvider(&called))

	// 段缺省：不装配。
	h, _, err := r.Build(context.Background(), nil)
	if h != nil || err != nil {
		t.Fatalf("nil section: handler=%v err=%v", h != nil, err)
	}

	// type 空 / 未注册：fail-fast。
	if _, _, err := r.Build(context.Background(), &bootstrapv1.Metrics{}); err == nil {
		t.Error("empty type should fail")
	}
	if _, _, err := r.Build(context.Background(), &bootstrapv1.Metrics{Type: "datadog"}); err == nil {
		t.Error("unregistered type should fail")
	}

	// 命中：透传。
	h, _, err = r.Build(context.Background(), &bootstrapv1.Metrics{Type: "prometheus"})
	if err != nil || h == nil || !called {
		t.Fatalf("hit: err=%v h=%v called=%v", err, h, called)
	}
}

func TestMetricsRegistry_RegisterValidation(t *testing.T) {
	r := NewMetricsRegistry()
	if err := r.Register("", stubMetricsProvider(new(bool))); err == nil {
		t.Error("empty type should fail")
	}
	if err := r.Register("prometheus", nil); err == nil {
		t.Error("nil provider should fail")
	}
	if err := r.Register("prometheus", stubMetricsProvider(new(bool))); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register("prometheus", stubMetricsProvider(new(bool))); err == nil {
		t.Error("duplicate register should fail")
	}
}

func TestBuildObservability(t *testing.T) {
	// 段全缺省：no-op state。
	st, err := buildObservability(&bootstrapv1.BootstrapConfig{}, &bootstrapSpec{})
	if err != nil {
		t.Fatalf("no sections: %v", err)
	}
	if st == nil || st.traceShutdown != nil || st.metricsSrv != nil || st.metricsFlush != nil {
		t.Fatalf("no sections should yield empty state: %+v", st)
	}

	// tracer 段存在但未接 registry：fail-fast。
	cfg := &bootstrapv1.BootstrapConfig{}
	cfg.Tracer = &bootstrapv1.Tracer{Type: "otlp"}
	if _, err := buildObservability(cfg, &bootstrapSpec{}); err == nil {
		t.Error("tracer without registry should fail")
	}

	// metrics 段存在但未接 registry：fail-fast。
	cfg2 := &bootstrapv1.BootstrapConfig{}
	cfg2.Metrics = &bootstrapv1.Metrics{Type: "prometheus"}
	if _, err := buildObservability(cfg2, &bootstrapSpec{}); err == nil {
		t.Error("metrics without registry should fail")
	}

	// 双段命中：state 三句柄齐备（暴露端真起 http.Server）。
	spec := &bootstrapSpec{
		tracerRegistry:  NewTracerRegistry(),
		metricsRegistry: NewMetricsRegistry(),
	}
	spec.tracerRegistry.MustRegister("otlp", stubTracerProvider(new(bool), func(context.Context) error { return nil }))
	spec.metricsRegistry.MustRegister("prometheus", stubMetricsProvider(new(bool)))
	st, err = buildObservability(cfg, spec) // cfg 只带 tracer 段
	if err != nil {
		t.Fatalf("tracer hit: %v", err)
	}
	if st.traceShutdown == nil {
		t.Fatal("tracer shutdown should be set")
	}
	st2, err := buildObservability(cfg2, spec) // cfg2 只带 metrics 段
	if err != nil {
		t.Fatalf("metrics hit: %v", err)
	}
	if st2.metricsSrv == nil || st2.metricsFlush == nil {
		t.Fatal("metrics server/flush should be set")
	}
	if err := st2.metricsSrv.Shutdown(context.Background()); err != nil {
		t.Fatalf("metrics server shutdown: %v", err)
	}
}

func TestStartMetricsServer(t *testing.T) {
	// 占随机端口（关闭后复用，竞态窗口可忽略）。
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("metrics-stub"))
	})
	srv := StartMetricsServer(addr, "/custom-metrics", handler)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for {
		resp, err = http.Get("http://" + addr + "/custom-metrics")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server not ready: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// 默认值：空 addr/path 走 :9091 / /metrics（仅断言常量，不起真端口避冲突）。
	if defaultMetricsAddr != ":9091" || defaultMetricsPath != "/metrics" {
		t.Fatalf("defaults changed: %q %q", defaultMetricsAddr, defaultMetricsPath)
	}
}
