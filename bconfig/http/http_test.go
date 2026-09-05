package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestNew_ParameterValidation 参数校验。
func TestNew_ParameterValidation(t *testing.T) {
	if _, err := New(); err == nil {
		t.Error("New() without url should fail")
	}
	if _, err := NewWithClient(nil, WithURL("http://x")); err == nil {
		t.Error("NewWithClient(nil) should fail")
	}
	if _, err := NewWithClient(&http.Client{}); err == nil {
		t.Error("NewWithClient without url should fail")
	}
}

// TestLoad_Basic 基本 GET 拉取。
func TestLoad_Basic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("log:\n  level: debug"))
	}))
	defer srv.Close()

	src, err := New(WithURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer src.Close()

	data, err := src.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "log:\n  level: debug" {
		t.Errorf("Load() = %q", data)
	}
}

// TestLoad_MethodHeaders 方法与自定义 header 透传。
func TestLoad_MethodHeaders(t *testing.T) {
	var gotMethod, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotToken = r.Method, r.Header.Get("Authorization")
	}))
	defer srv.Close()

	src, err := New(
		WithURL(srv.URL),
		WithMethod(http.MethodPost),
		WithHeader("Authorization", "Bearer tok"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer src.Close()

	if _, err := src.Load(context.Background(), ""); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotToken != "Bearer tok" {
		t.Errorf("Authorization = %s", gotToken)
	}
}

// TestLoad_HTTPErrorStatus 非 2xx 报错。
func TestLoad_HTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	src, _ := New(WithURL(srv.URL))
	defer src.Close()

	if _, err := src.Load(context.Background(), ""); err == nil {
		t.Error("Load() on 403 should fail")
	}
}

// TestWatchValue_ETagPolling ETag 条件轮询：变更才推送。
func TestWatchValue_ETagPolling(t *testing.T) {
	var version atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := version.Load()
		w.Header().Set("ETag", `"v`+time.Now().Format("150405")+`"`)
		w.Header().Set("ETag", `"static-etag"`)
		if r.Header.Get("If-None-Match") == `"static-etag"` && cur == 0 {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("ver: " + time.Now().Format("15:04:05.000")))
	}))
	defer srv.Close()

	src, err := New(WithURL(srv.URL), WithPollInterval(20*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := src.WatchValue(ctx, "")
	if err != nil {
		t.Fatalf("WatchValue: %v", err)
	}

	select {
	case data := <-ch:
		if len(data) == 0 {
			t.Error("initial push should carry data")
		}
	case <-ctx.Done():
		t.Fatal("no initial push")
	}

	// 内容不变（304）→ 不应收到第二次推送。
	version.Store(0) // 保持 304 分支
	select {
	case data := <-ch:
		t.Fatalf("unexpected push while unchanged: %s", data)
	case <-time.After(80 * time.Millisecond):
	}
}
