package resiliency

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kalandramo/bald/circuitbreaker"
	"github.com/kalandramo/bald/retry"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func getReq(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestHTTPSuccessMarksSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	b := &fakeBreaker{}
	resp, err := Transport(Policy{Breaker: b}, nil).RoundTrip(getReq(t, srv.URL))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 || readBody(t, resp) != "ok" {
		t.Fatalf("resp=%v body mismatch", resp.StatusCode)
	}
	if s, f := b.counts(); s != 1 || f != 0 {
		t.Fatalf("counts=(%d,%d), want (1,0)", s, f)
	}
}

// 单次尝试命中 503（无重试器）：原样交还 503 响应，绝不泄漏内部哨兵 error。
func TestHTTPSingleAttempt503ReturnsResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("down"))
	}))
	defer srv.Close()
	b := &fakeBreaker{}
	resp, err := Transport(Policy{Breaker: b}, nil).RoundTrip(getReq(t, srv.URL))
	if err != nil {
		t.Fatalf("RoundTrip returned error %v; the retryable-status sentinel leaked", err)
	}
	if resp.StatusCode != 503 || readBody(t, resp) != "down" {
		t.Fatalf("resp=%v, want 503 with buffered body", resp.StatusCode)
	}
	if s, f := b.counts(); s != 0 || f != 1 {
		t.Fatalf("counts=(%d,%d), want (0,1)", s, f)
	}
}

// 重试耗尽于 503：交还最后一个 503 响应（含体），记一次逻辑失败。
func TestHTTPRetryExhaustedReturnsLastResponse(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(503)
		_, _ = w.Write([]byte("still down"))
	}))
	defer srv.Close()
	b := &fakeBreaker{}
	p := Policy{Breaker: b, Retrier: fastRetrier(3)}
	resp, err := Transport(p, nil).RoundTrip(getReq(t, srv.URL))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 503 || readBody(t, resp) != "still down" {
		t.Fatalf("resp=%v, want last 503", resp.StatusCode)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("server calls=%d, want 3", calls)
	}
	if s, f := b.counts(); s != 0 || f != 1 {
		t.Fatalf("counts=(%d,%d), want (0,1)", s, f)
	}
}

func TestHTTPRetryThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte("transient"))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("recovered"))
	}))
	defer srv.Close()
	b := &fakeBreaker{}
	p := Policy{Breaker: b, Retrier: fastRetrier(5)}
	resp, err := Transport(p, nil).RoundTrip(getReq(t, srv.URL))
	if err != nil || resp.StatusCode != 200 || readBody(t, resp) != "recovered" {
		t.Fatalf("resp=%v err=%v, want 200 recovered", resp.StatusCode, err)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("calls=%d, want 3", calls)
	}
	if s, f := b.counts(); s != 1 || f != 0 {
		t.Fatalf("counts=(%d,%d), want (1,0)", s, f)
	}
}

// 429 可重试但不计熔断失败：耗尽于 429 仍记成功（下游节流 ≠ 下游坏了）。
func TestHTTP429RetryableButNotFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer srv.Close()
	b := &fakeBreaker{}
	p := Policy{Breaker: b, Retrier: fastRetrier(2)}
	resp, err := Transport(p, nil).RoundTrip(getReq(t, srv.URL))
	if err != nil || resp.StatusCode != 429 {
		t.Fatalf("resp=%v err=%v, want 429", resp.StatusCode, err)
	}
	if s, f := b.counts(); s != 1 || f != 0 {
		t.Fatalf("counts=(%d,%d), want (1,0) — 429 is not a breaker failure", s, f)
	}
}

func TestHTTP404NotRetryableNotFailure(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(404)
	}))
	defer srv.Close()
	b := &fakeBreaker{}
	p := Policy{Breaker: b, Retrier: fastRetrier(5)}
	resp, err := Transport(p, nil).RoundTrip(getReq(t, srv.URL))
	if err != nil || resp.StatusCode != 404 {
		t.Fatalf("resp=%v err=%v, want 404", resp.StatusCode, err)
	}
	resp.Body.Close()
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("calls=%d, want 1 (404 not retryable)", calls)
	}
	if s, f := b.counts(); s != 1 || f != 0 {
		t.Fatalf("counts=(%d,%d), want (1,0)", s, f)
	}
}

func TestHTTPTransportErrorExhaustedMarksFailure(t *testing.T) {
	var calls int32
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("connection refused")
	})
	b := &fakeBreaker{}
	p := Policy{Breaker: b, Retrier: fastRetrier(3)}
	resp, err := Transport(p, base).RoundTrip(getReq(t, "http://unused.invalid"))
	if err == nil || resp != nil {
		t.Fatalf("resp=%v err=%v, want nil response + error", resp, err)
	}
	if !errors.Is(err, retry.ErrMaxAttempts) {
		t.Fatalf("err=%v, want ErrMaxAttempts", err)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("base calls=%d, want 3", calls)
	}
	if s, f := b.counts(); s != 0 || f != 1 {
		t.Fatalf("counts=(%d,%d), want (0,1)", s, f)
	}
}

func TestHTTPOpenCircuitSkipsRequest(t *testing.T) {
	b := &fakeBreaker{allowErr: circuitbreaker.ErrCircuitOpen}
	called := false
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	resp, err := Transport(Policy{Breaker: b}, base).RoundTrip(getReq(t, "http://unused.invalid"))
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) || resp != nil {
		t.Fatalf("resp=%v err=%v, want ErrCircuitOpen", resp, err)
	}
	if called {
		t.Fatal("base called despite open circuit")
	}
	if s, f := b.counts(); s != 0 || f != 0 {
		t.Fatalf("counts=(%d,%d), want (0,0)", s, f)
	}
}

// 请求体重放：POST 体在每次尝试都要完整送达（GetBody 重放），首次 503 后成功。
func TestHTTPReplaysRequestBody(t *testing.T) {
	var calls int32
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if atomic.AddInt32(&calls, 1) < 2 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	b := &fakeBreaker{}
	p := Policy{Breaker: b, Retrier: fastRetrier(5)}
	resp, err := Transport(p, nil).RoundTrip(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp=%v err=%v, want 200", resp.StatusCode, err)
	}
	resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || bodies[0] != "payload" || bodies[1] != "payload" {
		t.Fatalf("bodies=%v, want two full 'payload' replays", bodies)
	}
	if s, f := b.counts(); s != 1 || f != 0 {
		t.Fatalf("counts=(%d,%d), want (1,0)", s, f)
	}
}

func TestHTTPPanicMarksFailureAndRethrows(t *testing.T) {
	b := &fakeBreaker{}
	base := roundTripFunc(func(*http.Request) (*http.Response, error) { panic("boom") })
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic to propagate")
		}
		if s, f := b.counts(); s != 0 || f != 1 {
			t.Fatalf("panic counts=(%d,%d), want (0,1)", s, f)
		}
	}()
	_, _ = Transport(Policy{Breaker: b}, base).RoundTrip(getReq(t, "http://unused.invalid"))
}