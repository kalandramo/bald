package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kalandramo/bald/pkg/metrics"
)

func TestSetup_ExposesBaldMetrics(t *testing.T) {
	handler, err := Setup(WithServiceName("bald-otlp-test"))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	// 通过真实 Recorder（接入 prometheus exporter）记一条指标。
	rec := Recorder("bald/test")
	rec.Record(context.Background(),
		metrics.Event{Object: "secret", Action: "get", Result: "allow"},
		metrics.TransportGRPC, 0.012)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("get /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := string(body)
	if !strings.Contains(out, "bald_requests_total") {
		t.Errorf("/metrics missing bald_requests_total:\n%s", out[:min(len(out), 500)])
	}
	if !strings.Contains(out, `object="secret"`) {
		t.Errorf("/metrics missing object=\"secret\" label:\n%s", out[:min(len(out), 500)])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestSetup_OTLP_FullOptions 契约全字段（insecure/headers/interval）应成功构造双通道。
func TestSetup_OTLP_FullOptions(t *testing.T) {
	handler, err := Setup(
		WithServiceName("bald-otlp-test"),
		WithOTLPAddr("localhost:4318"),
		WithInsecure(true),
		WithHeaders(map[string]string{"Authorization": "Bearer test-token"}),
		WithInterval(10*time.Second),
	)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if handler == nil {
		t.Fatal("handler 不应为 nil")
	}
}

// TestSetup_OTLP_ExplicitTLS 显式 insecure=false 时裸地址走 TLS。
func TestSetup_OTLP_ExplicitTLS(t *testing.T) {
	handler, err := Setup(
		WithServiceName("bald-otlp-test"),
		WithOTLPAddr("collector.internal:4318"),
		WithInsecure(false),
	)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if handler == nil {
		t.Fatal("handler 不应为 nil")
	}
}

// TestWithInterval_NonPositive 非正间隔被忽略，保留默认值。
func TestWithInterval_NonPositive(t *testing.T) {
	cfg := options{interval: defaultInterval}
	WithInterval(0)(&cfg)
	if cfg.interval != defaultInterval {
		t.Fatalf("WithInterval(0) should keep default, got %v", cfg.interval)
	}
	WithInterval(-5 * time.Second)(&cfg)
	if cfg.interval != defaultInterval {
		t.Fatalf("WithInterval(-5s) should keep default, got %v", cfg.interval)
	}
}
