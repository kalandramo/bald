package shared

import (
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// fakeSpanContext 构造合法 SpanContext（采样位可控）。
func fakeSpanContext(sampled bool) trace.SpanContext {
	cfg := trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:     trace.SpanID{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18},
		TraceFlags: trace.FlagsSampled,
	}
	if !sampled {
		cfg.TraceFlags = 0
	}
	return trace.NewSpanContext(cfg)
}

const (
	wantTraceID = "0102030405060708090a0b0c0d0e0f10"
	wantSpanID  = "1112131415161718"
)

func TestInjectTrace_Modes(t *testing.T) {
	tests := []struct {
		name       string
		mode       TraceInjectionMode
		custom     string
		sampled    bool
		wantKeys   []string
		wantValues map[string]string
	}{
		{
			name:     "W3C sampled",
			mode:     InjectW3CTraceContext,
			sampled:  true,
			wantKeys: []string{"traceparent"},
			wantValues: map[string]string{
				"traceparent": "00-" + wantTraceID + "-" + wantSpanID + "-01",
			},
		},
		{
			name:     "W3C not sampled",
			mode:     InjectW3CTraceContext,
			sampled:  false,
			wantKeys: []string{"traceparent"},
			wantValues: map[string]string{
				"traceparent": "00-" + wantTraceID + "-" + wantSpanID + "-00",
			},
		},
		{
			name:     "trace ID only, default header",
			mode:     InjectTraceIDOnly,
			wantKeys: []string{"X-Trace-Id"},
			wantValues: map[string]string{
				"X-Trace-Id": wantTraceID,
			},
		},
		{
			name:     "trace ID only, custom header",
			mode:     InjectTraceIDOnly,
			custom:   "X-My-Trace",
			wantKeys: []string{"X-My-Trace"},
			wantValues: map[string]string{
				"X-My-Trace": wantTraceID,
			},
		},
		{
			name:       "both, custom header",
			mode:       InjectBoth,
			custom:     "X-My-Trace",
			sampled:    true,
			wantKeys:   []string{"traceparent", "X-My-Trace"},
			wantValues: map[string]string{"traceparent": "00-" + wantTraceID + "-" + wantSpanID + "-01", "X-My-Trace": wantTraceID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotKeys []string
			gotValues := map[string]string{}
			InjectTrace(tt.mode, tt.custom, fakeSpanContext(tt.sampled), func(k, v string) {
				gotKeys = append(gotKeys, k)
				gotValues[k] = v
			})

			if len(gotKeys) != len(tt.wantKeys) {
				t.Fatalf("injected %d headers (%v), want %d (%v)", len(gotKeys), gotKeys, len(tt.wantKeys), tt.wantKeys)
			}
			for i, k := range tt.wantKeys {
				if gotKeys[i] != k {
					t.Fatalf("header[%d] = %q, want %q", i, gotKeys[i], k)
				}
			}
			for k, v := range tt.wantValues {
				if gotValues[k] != v {
					t.Fatalf("header %q = %q, want %q", k, gotValues[k], v)
				}
			}
		})
	}
}

// 无效 SpanContext（no-op tracer，未装配全局 TracerProvider）不注入。
func TestInjectTrace_InvalidSpanContext(t *testing.T) {
	for _, mode := range []TraceInjectionMode{InjectW3CTraceContext, InjectTraceIDOnly, InjectBoth} {
		called := false
		InjectTrace(mode, "", trace.SpanContext{}, func(k, v string) { called = true })
		if called {
			t.Fatalf("mode %d: must not inject for invalid span context", mode)
		}
	}
}

// InjectNone 恒不注入（含合法 spanCtx）。
func TestInjectTrace_NoneMode(t *testing.T) {
	called := false
	InjectTrace(InjectNone, "", fakeSpanContext(true), func(k, v string) { called = true })
	if called {
		t.Fatal("InjectNone must not inject")
	}
}

func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		text, pattern string
		want          bool
	}{
		{"", "*", true},
		{"/metrics", "*", true},
		{"/health/ready", "/health/*", true},
		{"/ready", "/health/*", false},
		{"/api/v1/users", "/api/*", true},
		{"/v2/api", "/api/*", false},
		{"/foo/bar", "*/bar", true},
		{"/foo/baz", "*/bar", false},
		{"/a/b/c", "*b*", true},
		{"/a/c/d", "*b*", false},
		{"/metrics", "/metrics", true},   // 无通配符退化为全等
		{"/metrics", "/metricss", false}, // 前缀只由显式后缀 * 触发
	}
	for _, tt := range tests {
		if got := MatchWildcard(tt.text, tt.pattern); got != tt.want {
			t.Errorf("MatchWildcard(%q, %q) = %v, want %v", tt.text, tt.pattern, got, tt.want)
		}
	}
}
