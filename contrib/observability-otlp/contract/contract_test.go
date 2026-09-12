package contract

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

func TestNewTracerProvider(t *testing.T) {
	p := NewTracerProvider("contract-test")

	// endpoint 缺失：fail-fast。
	if _, err := p(context.Background(), &bootstrapv1.Tracer{Type: TracerType}); err == nil {
		t.Error("missing endpoint should fail")
	}

	// endpoint 有值：构造成功（exporter 构造不连网），shutdown 非 nil。
	sd, err := p(context.Background(), &bootstrapv1.Tracer{
		Type: TracerType,
		Otlp: &bootstrapv1.Tracer_Otlp{
			Endpoint: "127.0.0.1:4318",
			Insecure: true,
		},
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if sd == nil {
		t.Fatal("shutdown should not be nil")
	}
	if err := sd(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestNewPrometheusProvider(t *testing.T) {
	p := NewPrometheusProvider("contract-test")

	// otlp endpoint 配置了但 type=prometheus：矛盾声明 fail-fast。
	if _, _, err := p(context.Background(), &bootstrapv1.Metrics{
		Type: TypePrometheus,
		Otlp: &bootstrapv1.Metrics_Otlp{Endpoint: "127.0.0.1:4318"},
	}); err == nil {
		t.Error("conflicting otlp endpoint should fail")
	}

	// 干净声明：handler + shutdown 非 nil。
	h, shutdown, err := p(context.Background(), &bootstrapv1.Metrics{Type: TypePrometheus})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if h == nil || shutdown == nil {
		t.Fatal("handler/shutdown should not be nil")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestNewOTLPProvider(t *testing.T) {
	p := NewOTLPProvider("contract-test")

	// endpoint 缺失：fail-fast。
	if _, _, err := p(context.Background(), &bootstrapv1.Metrics{Type: TypeOTLP}); err == nil {
		t.Error("missing endpoint should fail")
	}

	// endpoint 有值：双通道构造成功（exporter 构造不连网；shutdown 会强制
	// flush 到远端，本地无 collector 故只断言非 nil，不真调）。
	h, shutdown, err := p(context.Background(), &bootstrapv1.Metrics{
		Type: TypeOTLP,
		Otlp: &bootstrapv1.Metrics_Otlp{
			Endpoint:     "127.0.0.1:4318",
			Insecure:     true,
			PushInterval: 15,
		},
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if h == nil || shutdown == nil {
		t.Fatal("handler/shutdown should not be nil")
	}
}
