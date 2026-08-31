package trace

import (
	"context"
	"testing"
)

// TestSetup_NoOTLP_ReturnsNoop 未提供 OTLP 地址时返回 no-op shutdown，不挂全局 Provider。
func TestSetup_NoOTLP_ReturnsNoop(t *testing.T) {
	shutdown, err := Setup()
	if err != nil {
		t.Fatalf("Setup() err = %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown func 不应为 nil")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown err = %v", err)
	}
}

// TestSetup_OTLP_BareAddr 裸 host:port 应成功构造 exporter + shutdown（不真正连网）。
func TestSetup_OTLP_BareAddr(t *testing.T) {
	shutdown, err := Setup(WithOTLPAddr("localhost:4318"), WithServiceName("bald-otlp-test"))
	if err != nil {
		t.Fatalf("Setup() err = %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown err = %v", err)
	}
}

// TestSetup_OTLP_URL 带 http:// 前缀应按完整 EndpointURL 解析。
func TestSetup_OTLP_URL(t *testing.T) {
	shutdown, err := Setup(WithOTLPAddr("http://otel-collector:4318"))
	if err != nil {
		t.Fatalf("Setup() err = %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown err = %v", err)
	}
}
