package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
