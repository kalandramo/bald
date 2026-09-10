package trace

import (
	"context"
	"fmt"
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

// TestSetup_OTLP_FullOptions 契约全字段（insecure/headers/sampler/ratio）应成功构造。
func TestSetup_OTLP_FullOptions(t *testing.T) {
	shutdown, err := Setup(
		WithOTLPAddr("localhost:4318"),
		WithServiceName("bald-otlp-test"),
		WithInsecure(true),
		WithHeaders(map[string]string{"Authorization": "Bearer test-token"}),
		WithSampler("trace_id_ratio"),
		WithSampleRatio(0.5),
	)
	if err != nil {
		t.Fatalf("Setup() err = %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown err = %v", err)
	}
}

// TestSetup_OTLP_ExplicitTLS 显式 insecure=false 时裸地址也走 TLS（不追加 WithInsecure）。
func TestSetup_OTLP_ExplicitTLS(t *testing.T) {
	shutdown, err := Setup(WithOTLPAddr("collector.internal:4318"), WithInsecure(false))
	if err != nil {
		t.Fatalf("Setup() err = %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown err = %v", err)
	}
}

// TestBuildSampler 采样策略名→采样器映射与退化语义。
func TestBuildSampler(t *testing.T) {
	tests := []struct {
		name    string
		ratio   float64
		wantErr bool
	}{
		{name: ""},                           // 缺省 parent_based
		{name: "parent_based"},               // 显式 parent_based
		{name: "always_on"},                  // 全采样
		{name: "always_off"},                 // 全不采样
		{name: "trace_id_ratio", ratio: 0.5}, // 比率采样
		{name: "trace_id_ratio", ratio: 0},   // 非正比率 → 1.0
		{name: "bogus", wantErr: false},      // 未知值 → 退化 + WARN，不 panic
	}
	for _, tt := range tests {
		t.Run(tt.name+"/"+fmt.Sprintf("%v", tt.ratio), func(t *testing.T) {
			s := buildSampler(tt.name, tt.ratio)
			if s == nil {
				t.Fatal("buildSampler returned nil")
			}
			// 采样器可被 SDK 接受的最弱验证：Description 非空。
			if s.Description() == "" {
				t.Fatalf("sampler description empty for %q", tt.name)
			}
		})
	}
}
