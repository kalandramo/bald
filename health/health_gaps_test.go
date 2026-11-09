package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// staticChecker 返回预设 Result，用于触达聚合器的 Details 合并与无消息 Down 分支。
type staticChecker struct{ res Result }

func (s staticChecker) Check(context.Context) Result { return s.res }

// ---------------------------------------------------------------------------
// 默认超时回退
// ---------------------------------------------------------------------------

func TestTCPChecker_DefaultTimeout(t *testing.T) {
	// timeout=0 走 3s 默认；127.0.0.1:1 立即拒绝，不会真等 3s。
	r := TCP("127.0.0.1:1", 0).Check(context.Background())
	if r.Status != StatusDown {
		t.Errorf("expected StatusDown, got %v", r.Status)
	}
}

func TestHTTPChecker_DefaultTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// timeout=0 走 3s 默认。
	r := HTTP(srv.URL+"/healthz", 0).Check(context.Background())
	if r.Status != StatusUp {
		t.Errorf("expected StatusUp, got %v (msg: %s)", r.Status, r.Message)
	}
}

// ---------------------------------------------------------------------------
// HTTP 请求构建错误
// ---------------------------------------------------------------------------

func TestHTTPChecker_BuildRequestError(t *testing.T) {
	// "://invalid" 缺协议 scheme，newHTTPRequest 报错 → Down。
	r := HTTP("://invalid", time.Second).Check(context.Background())
	if r.Status != StatusDown {
		t.Errorf("expected StatusDown, got %v", r.Status)
	}
	if !strings.Contains(r.Message, "http request build") {
		t.Errorf("expected build-error message, got %q", r.Message)
	}
}

func TestNewHTTPRequest_Error(t *testing.T) {
	if _, err := newHTTPRequest(context.Background(), "://invalid"); err == nil {
		t.Fatal("newHTTPRequest with scheme-less URL should fail")
	}
}

// ---------------------------------------------------------------------------
// Handler — 无检查器时 Message 字段
// ---------------------------------------------------------------------------

func TestHandler_NoCheckers_MessageField(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	NewHandler(New()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var resp handlerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Message != "no checkers registered" {
		t.Errorf("expected message %q, got %q", "no checkers registered", resp.Message)
	}
}

// ---------------------------------------------------------------------------
// Health.Check — Details 合并与 Unknown 聚合
// ---------------------------------------------------------------------------

func TestHealth_CheckMergesDetails(t *testing.T) {
	h := New()
	h.Register("svc", staticChecker{res: Result{
		Status:  StatusUp,
		Details: map[string]any{"latency": "5ms"},
	}})

	r := h.Check(context.Background())
	if r.Status != StatusUp {
		t.Fatalf("expected StatusUp, got %v", r.Status)
	}
	detail, ok := r.Details["svc"].(map[string]any)
	if !ok {
		t.Fatalf("expected map detail for svc, got %T", r.Details["svc"])
	}
	if detail["latency"] != "5ms" {
		t.Errorf("expected merged latency=5ms, got %v", detail["latency"])
	}
}

func TestHealth_CheckUnknown(t *testing.T) {
	h := New()
	h.Register("nil", PingFunc(nil)) // → StatusUnknown

	r := h.Check(context.Background())
	if r.Status != StatusUnknown {
		t.Errorf("expected StatusUnknown, got %v", r.Status)
	}
}

// ---------------------------------------------------------------------------
// Readiness
// ---------------------------------------------------------------------------

func TestReadiness_Up_ReturnsNil(t *testing.T) {
	h := New()
	h.Register("svc", PingFunc(func(context.Context) error { return nil }))
	if err := h.Readiness(context.Background()); err != nil {
		t.Errorf("expected nil for Up, got %v", err)
	}
}

func TestReadiness_Unknown_ReturnsNil(t *testing.T) {
	h := New()
	h.Register("nil", PingFunc(nil)) // → StatusUnknown，宁可乐观返回 nil
	if err := h.Readiness(context.Background()); err != nil {
		t.Errorf("expected nil for Unknown, got %v", err)
	}
}

func TestReadiness_DownWithMessage(t *testing.T) {
	h := New()
	h.Register("db", PingFunc(func(context.Context) error { return errors.New("boom") }))

	err := h.Readiness(context.Background())
	if err == nil {
		t.Fatal("expected error for Down")
	}
	if err.Error() != "health: not ready: db: boom" {
		t.Errorf("unexpected error: %q", err.Error())
	}
}

func TestReadiness_DownNoMessage(t *testing.T) {
	h := New()
	h.Register("svc", staticChecker{res: Result{Status: StatusDown}})

	err := h.Readiness(context.Background())
	if err == nil {
		t.Fatal("expected error for Down")
	}
	if err.Error() != "health: not ready: svc" {
		t.Errorf("unexpected error: %q", err.Error())
	}
}

func TestReadiness_MixedUpAndDown(t *testing.T) {
	h := New()
	h.Register("up", PingFunc(func(context.Context) error { return nil }))
	h.Register("down", PingFunc(func(context.Context) error { return errors.New("fail") }))

	err := h.Readiness(context.Background())
	if err == nil {
		t.Fatal("expected error for Down")
	}
	if strings.Contains(err.Error(), "up") {
		t.Errorf("Up checker should be skipped, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "down: fail") {
		t.Errorf("expected down checker in error, got %q", err.Error())
	}
}

func TestReadiness_MultipleDown_Sorted(t *testing.T) {
	h := New()
	h.Register("zeta", PingFunc(func(context.Context) error { return errors.New("z") }))
	h.Register("alpha", PingFunc(func(context.Context) error { return errors.New("a") }))

	err := h.Readiness(context.Background())
	if err == nil {
		t.Fatal("expected error for Down")
	}
	if err.Error() != "health: not ready: alpha: a; zeta: z" {
		t.Errorf("expected sorted join, got %q", err.Error())
	}
}
