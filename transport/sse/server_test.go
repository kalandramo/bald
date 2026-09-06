package sse

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/kalandramo/bald/encoding/json"
)

// ---------------------------------------------------------------------------
// NewServer + defaults
// ---------------------------------------------------------------------------

func TestNewServer_Defaults(t *testing.T) {
	srv := NewServer(":0")
	if srv.Addr() != ":0" {
		t.Errorf("Addr() = %q, want %q", srv.Addr(), ":0")
	}
	if srv.path != "/events" {
		t.Errorf("path = %q, want %q", srv.path, "/events")
	}
	if srv.streamIdKey != "stream" {
		t.Errorf("streamIdKey = %q, want %q", srv.streamIdKey, "stream")
	}
	if srv.bufferSize != DefaultBufferSize {
		t.Errorf("bufferSize = %d, want %d", srv.bufferSize, DefaultBufferSize)
	}
	if !srv.autoReplay {
		t.Errorf("autoReplay = false, want true")
	}
	if srv.autoStream {
		t.Errorf("autoStream = true, want false")
	}
	if srv.corsAllowOrigin != "*" {
		t.Errorf("corsAllowOrigin = %q, want %q", srv.corsAllowOrigin, "*")
	}
}

func TestNewServer_Options(t *testing.T) {
	srv := NewServer(":0",
		WithPath("/sse"),
		WithStreamIdKey("channel"),
		WithBufferSize(512),
		WithAutoStream(true),
		WithAutoReplay(false),
		WithEncodeBase64(true),
		WithSplitData(true),
		WithCORSAllowOrigin("https://example.com"),
	)
	if srv.path != "/sse" {
		t.Errorf("path = %q, want %q", srv.path, "/sse")
	}
	if srv.streamIdKey != "channel" {
		t.Errorf("streamIdKey = %q, want %q", srv.streamIdKey, "channel")
	}
	if srv.bufferSize != 512 {
		t.Errorf("bufferSize = %d, want %d", srv.bufferSize, 512)
	}
	if !srv.autoStream {
		t.Errorf("autoStream = false, want true")
	}
	if srv.autoReplay {
		t.Errorf("autoReplay = true, want false")
	}
	if !srv.encodeBase64 {
		t.Errorf("encodeBase64 = false, want true")
	}
	if !srv.splitData {
		t.Errorf("splitData = false, want true")
	}
	if srv.corsAllowOrigin != "https://example.com" {
		t.Errorf("corsAllowOrigin = %q, want %q", srv.corsAllowOrigin, "https://example.com")
	}
}

// ---------------------------------------------------------------------------
// Endpoint
// ---------------------------------------------------------------------------

func TestServer_Endpoint(t *testing.T) {
	srv := NewServer(":9090")
	ep := srv.Endpoint()
	if !strings.HasPrefix(ep, "http://") {
		t.Errorf("Endpoint() = %q, want http:// prefix", ep)
	}
}

func TestServer_Endpoint_HTTPS(t *testing.T) {
	srv := NewServer(":9090", WithTLSConfig(&tls.Config{}))
	ep := srv.Endpoint()
	if !strings.HasPrefix(ep, "https://") {
		t.Errorf("Endpoint() = %q, want https:// prefix", ep)
	}
}

func TestServer_Endpoint_NormalizeWildcard(t *testing.T) {
	srv := NewServer("0.0.0.0:9090")
	ep := srv.Endpoint()
	if !strings.HasPrefix(ep, "http://localhost:") {
		t.Errorf("Endpoint() = %q, want http://localhost: prefix", ep)
	}
}

// ---------------------------------------------------------------------------
// Stream management
// ---------------------------------------------------------------------------

func TestServer_CreateStream(t *testing.T) {
	srv := NewServer(":0")
	srv.CreateStream("test-stream")
	if srv.StreamCount() != 1 {
		t.Errorf("StreamCount() = %d, want 1", srv.StreamCount())
	}
	// Creating same stream again should not create a duplicate
	srv.CreateStream("test-stream")
	if srv.StreamCount() != 1 {
		t.Errorf("StreamCount() = %d, want 1 (duplicate)", srv.StreamCount())
	}
	// Creating a different stream
	srv.CreateStream("other-stream")
	if srv.StreamCount() != 2 {
		t.Errorf("StreamCount() = %d, want 2", srv.StreamCount())
	}
}

func TestServer_RemoveStream(t *testing.T) {
	srv := NewServer(":0")
	srv.CreateStream("test-stream")
	srv.RemoveStream("test-stream")
	if srv.StreamCount() != 0 {
		t.Errorf("StreamCount() = %d, want 0 after remove", srv.StreamCount())
	}
}

func TestServer_GetStream(t *testing.T) {
	srv := NewServer(":0")
	s := srv.CreateStream("test-stream")
	if s == nil {
		t.Fatal("CreateStream returned nil")
	}
	got := srv.GetStream("test-stream")
	if got == nil {
		t.Fatal("GetStream returned nil for existing stream")
	}
	if got.StreamID() != StreamID("test-stream") {
		t.Errorf("StreamID() = %q, want %q", got.StreamID(), "test-stream")
	}
	// Non-existent stream
	if srv.GetStream("nonexistent") != nil {
		t.Error("GetStream should return nil for non-existent stream")
	}
}

// ---------------------------------------------------------------------------
// Publish
// ---------------------------------------------------------------------------

func TestServer_Publish_NonExistentStream(t *testing.T) {
	srv := NewServer(":0")
	// Publishing to non-existent stream should be a no-op (no panic)
	srv.Publish(context.Background(), "nonexistent", &Event{Data: []byte("hello")})
}

func TestServer_TryPublish(t *testing.T) {
	srv := NewServer(":0")
	srv.CreateStream("test")
	// TryPublish to existing stream should succeed
	ok := srv.TryPublish(context.Background(), "test", &Event{Data: []byte("hello")})
	if !ok {
		t.Error("TryPublish to existing stream returned false")
	}
	// TryPublish to non-existent stream should fail
	ok = srv.TryPublish(context.Background(), "nonexistent", &Event{Data: []byte("hello")})
	if ok {
		t.Error("TryPublish to non-existent stream returned true")
	}
}

func TestServer_PublishData(t *testing.T) {
	srv := NewServer(":0")
	srv.CreateStream("test")
	err := srv.PublishData(context.Background(), "test", map[string]string{"msg": "hello"})
	if err != nil {
		t.Errorf("PublishData error: %v", err)
	}
	// PublishDataWithEventName
	err = srv.PublishDataWithEventName(context.Background(), "test", "update", map[string]string{"msg": "world"})
	if err != nil {
		t.Errorf("PublishDataWithEventName error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Notify (broadcast to all streams)
// ---------------------------------------------------------------------------

func TestServer_Notify(t *testing.T) {
	srv := NewServer(":0")
	srv.CreateStream("a")
	srv.CreateStream("b")
	// Notify should not panic with existing streams
	srv.Notify(context.Background(), &Event{Data: []byte("broadcast")})
	// NotifyData
	err := srv.NotifyData(context.Background(), map[string]string{"msg": "broadcast"})
	if err != nil {
		t.Errorf("NotifyData error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Start/Stop lifecycle
// ---------------------------------------------------------------------------

func TestServer_StartStop(t *testing.T) {
	srv := NewServer("127.0.0.1:0",
		WithPath("/events"),
	)

	srv.CreateStream("test")

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- srv.Start(ctx)
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Cancel to trigger graceful shutdown
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop within 5s")
	}
}

func TestServer_StopBeforeStart(t *testing.T) {
	srv := NewServer(":0")
	// Stopping before Start should not panic
	if err := srv.Stop(context.Background()); err != nil {
		t.Errorf("Stop before Start returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SSE integration: connect a real HTTP client to the SSE endpoint
// ---------------------------------------------------------------------------

func TestServer_SSE_ConnectAndReceive(t *testing.T) {
	srv := NewServer("127.0.0.1:0", WithPath("/events"))
	srv.CreateStream("updates")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("http://%s/events?stream=updates", addr)

	// Client: connect to SSE endpoint and read events
	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()

	eventsReceived := make(chan string, 10)
	go func() {
		req, _ := http.NewRequestWithContext(clientCtx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				eventsReceived <- strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
	}()

	// Give the client time to connect and subscribe
	time.Sleep(150 * time.Millisecond)

	// Publish an event
	srv.Publish(context.Background(), "updates", &Event{Data: []byte("hello-sse")})

	// Wait for the event to arrive
	select {
	case data := <-eventsReceived:
		if data != "hello-sse" {
			t.Errorf("received data = %q, want %q", data, "hello-sse")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive SSE event within 3s")
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestServer_SSE_MissingStreamParam(t *testing.T) {
	srv := NewServer("127.0.0.1:0", WithPath("/events"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	// No ?stream= parameter
	url := fmt.Sprintf("http://%s/events", addr)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("HTTP GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestServer_SSE_StreamNotFound(t *testing.T) {
	srv := NewServer("127.0.0.1:0", WithPath("/events"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("http://%s/events?stream=nonexistent", addr)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("HTTP GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestServer_SSE_AutoStream(t *testing.T) {
	srv := NewServer("127.0.0.1:0",
		WithPath("/events"),
		WithAutoStream(true),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("http://%s/events?stream=auto-created", addr)

	// With autoStream, the stream should be auto-created
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP GET error: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// The stream should now exist
	if srv.GetStream("auto-created") == nil {
		t.Error("auto-created stream should exist")
	}

	resp.Body.Close()
	cancel()
	time.Sleep(50 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

func TestServer_Use(t *testing.T) {
	srv := NewServer("127.0.0.1:0", WithPath("/events"))

	headerSet := false
	srv.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Custom", "middleware-applied")
			headerSet = true
			next.ServeHTTP(w, r)
		})
	})

	srv.CreateStream("test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("http://%s/events?stream=nonexistent", addr)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("HTTP GET error: %v", err)
	}
	defer resp.Body.Close()

	if !headerSet {
		t.Error("middleware was not invoked")
	}
	if resp.Header.Get("X-Custom") != "middleware-applied" {
		t.Error("custom header not set by middleware")
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestServer_WithMiddleware(t *testing.T) {
	called := false
	srv := NewServer("127.0.0.1:0",
		WithPath("/events"),
		WithMiddleware(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				next.ServeHTTP(w, r)
			})
		}),
	)

	srv.CreateStream("test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("http://%s/events?stream=nonexistent", addr)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("HTTP GET error: %v", err)
	}
	resp.Body.Close()

	if !called {
		t.Error("middleware passed via WithMiddleware was not called")
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

func TestServer_Auth_Unauthorized(t *testing.T) {
	srv := NewServer("127.0.0.1:0",
		WithPath("/events"),
		WithAuthorizeFunc(func(r *http.Request, token string) error {
			return ErrUnauthorized
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("http://%s/events?stream=test", addr)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("HTTP GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestServer_Auth_Forbidden(t *testing.T) {
	srv := NewServer("127.0.0.1:0",
		WithPath("/events"),
		WithAuthorizeFunc(func(r *http.Request, token string) error {
			return ErrForbidden
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("http://%s/events?stream=test", addr)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("HTTP GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// OPTIONS preflight
// ---------------------------------------------------------------------------

func TestServer_SSE_Options(t *testing.T) {
	srv := NewServer("127.0.0.1:0", WithPath("/events"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("http://%s/events?stream=test", addr)

	req, _ := http.NewRequest(http.MethodOptions, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP OPTIONS error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	if resp.Header.Get("Access-Control-Allow-Methods") == "" {
		t.Error("Access-Control-Allow-Methods header not set")
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// HandleFunc (custom routes)
// ---------------------------------------------------------------------------

func TestServer_HandleFunc(t *testing.T) {
	srv := NewServer("127.0.0.1:0")
	srv.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("http://%s/health", addr)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("HTTP GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// Subscriber
// ---------------------------------------------------------------------------

func TestSubscriber_Token(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/events?token=mytoken", nil)
	req.Header.Set("X-Custom-Auth", "custom-token")
	req.Header.Set("Authorization", "Bearer bearer-token")

	sub := &Subscriber{
		URL:    req.URL,
		Header: req.Header.Clone(),
	}

	if sub.BearerToken() != "bearer-token" {
		t.Errorf("BearerToken() = %q, want %q", sub.BearerToken(), "bearer-token")
	}
	if token := sub.Token("X-Custom-Auth"); token != "custom-token" {
		t.Errorf("Token(\"X-Custom-Auth\") = %q, want %q", token, "custom-token")
	}
	if token := sub.Token(""); token != "bearer-token" {
		t.Errorf("Token(\"\") = %q, want %q", token, "bearer-token")
	}
}

func TestSubscriber_HeaderValue(t *testing.T) {
	sub := &Subscriber{Header: http.Header{}}
	sub.Header.Set("X-Key", "value")
	if v := sub.HeaderValue("X-Key"); v != "value" {
		t.Errorf("HeaderValue = %q, want %q", v, "value")
	}
	if v := sub.HeaderValue("X-Missing"); v != "" {
		t.Errorf("HeaderValue for missing key = %q, want empty", v)
	}

	// Nil subscriber
	var nilSub *Subscriber
	if v := nilSub.HeaderValue("X-Key"); v != "" {
		t.Errorf("HeaderValue on nil subscriber = %q, want empty", v)
	}
}

// ---------------------------------------------------------------------------
// Auth helpers
// ---------------------------------------------------------------------------

func TestDefaultTokenExtractor(t *testing.T) {
	// Bearer token
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req.Header.Set("Authorization", "Bearer my-bearer-token")
	if token := DefaultTokenExtractor(req); token != "my-bearer-token" {
		t.Errorf("Bearer token = %q, want %q", token, "my-bearer-token")
	}

	// X-Token header
	req = httptest.NewRequest(http.MethodGet, "/events", nil)
	req.Header.Set("X-Token", "x-token-value")
	if token := DefaultTokenExtractor(req); token != "x-token-value" {
		t.Errorf("X-Token = %q, want %q", token, "x-token-value")
	}

	// Query param
	req = httptest.NewRequest(http.MethodGet, "/events?token=query-token", nil)
	if token := DefaultTokenExtractor(req); token != "query-token" {
		t.Errorf("Query token = %q, want %q", token, "query-token")
	}

	// Empty
	req = httptest.NewRequest(http.MethodGet, "/events", nil)
	if token := DefaultTokenExtractor(req); token != "" {
		t.Errorf("Empty token = %q, want empty", token)
	}

	// Nil request
	if token := DefaultTokenExtractor(nil); token != "" {
		t.Errorf("Nil request token = %q, want empty", token)
	}
}

// ---------------------------------------------------------------------------
// Event
// ---------------------------------------------------------------------------

func TestEvent_HasContent(t *testing.T) {
	e := &Event{}
	if e.hasContent() {
		t.Error("empty event should not have content")
	}

	e.Data = []byte("hello")
	if !e.hasContent() {
		t.Error("event with data should have content")
	}
}

func TestEvent_EncodeBase64(t *testing.T) {
	e := &Event{Data: []byte("hello")}
	e.encodeBase64()
	decoded, err := base64.StdEncoding.DecodeString(string(e.Data))
	if err != nil {
		t.Fatalf("base64 decode error: %v", err)
	}
	if string(decoded) != "hello" {
		t.Errorf("base64 decoded = %q, want %q", string(decoded), "hello")
	}
}

func TestEventMetaOptions(t *testing.T) {
	e := &Event{}
	WithEventID("123")(e)
	WithEventName("update")(e)
	WithEventRetry("5000")(e)
	WithEventComment("keepalive")(e)

	if string(e.ID) != "123" {
		t.Errorf("ID = %q, want %q", string(e.ID), "123")
	}
	if string(e.Event) != "update" {
		t.Errorf("Event = %q, want %q", string(e.Event), "update")
	}
	if string(e.Retry) != "5000" {
		t.Errorf("Retry = %q, want %q", string(e.Retry), "5000")
	}
	if string(e.Comment) != "keepalive" {
		t.Errorf("Comment = %q, want %q", string(e.Comment), "keepalive")
	}
}

// ---------------------------------------------------------------------------
// EventLog
// ---------------------------------------------------------------------------

func TestEventLog_Add(t *testing.T) {
	var log EventLog
	log.Add(&Event{Data: []byte("event1")})
	if len(log) != 1 {
		t.Fatalf("EventLog length = %d, want 1", len(log))
	}
	if len(log[0].ID) == 0 {
		t.Error("event ID should be set by Add")
	}
	if log[0].timestamp.IsZero() {
		t.Error("event timestamp should be set by Add")
	}
}

func TestEventLog_Add_EmptyEvent(t *testing.T) {
	var log EventLog
	log.Add(&Event{}) // no content
	if len(log) != 0 {
		t.Errorf("EventLog length = %d, want 0 for empty event", len(log))
	}
}

func TestEventLog_Clear(t *testing.T) {
	var log EventLog
	log.Add(&Event{Data: []byte("event1")})
	log.Clear()
	if len(log) != 0 {
		t.Errorf("EventLog length after Clear = %d, want 0", len(log))
	}
}

// ---------------------------------------------------------------------------
// StreamManager
// ---------------------------------------------------------------------------

func TestStreamManager(t *testing.T) {
	mgr := NewStreamManager()

	if mgr.Count() != 0 {
		t.Errorf("Count() = %d, want 0", mgr.Count())
	}

	s := newStream("test", 1024, true, false, nil, nil)
	s.run()
	mgr.Add(s)

	if !mgr.Exist("test") {
		t.Error("Exist(\"test\") = false, want true")
	}

	if mgr.Get("test") == nil {
		t.Error("Get(\"test\") = nil, want stream")
	}

	mgr.RemoveWithID("test")
	if mgr.Exist("test") {
		t.Error("Exist(\"test\") = true after remove, want false")
	}
}
