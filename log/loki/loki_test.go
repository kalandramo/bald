package loki

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	log "github.com/kalandramo/bald/log"
)

// newTestLogger 构造不触发 flush 的 logger（batchSize 足够大），
// 便于检查 buffer 中待发送的 JSON 行。
func newTestLogger(t *testing.T) *lokiLog {
	t.Helper()
	l, err := NewLogger(
		WithEndpoint("http://127.0.0.1:3100/loki/api/v1/push"),
		WithBatchSize(1000),
	)
	if err != nil {
		t.Fatal(err)
	}
	return l.(*lokiLog)
}

func bufferedLines(t *testing.T, l *lokiLog) []string {
	t.Helper()
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	lines := make([]string, len(l.s.buffer))
	for i, e := range l.s.buffer {
		lines[i] = e.line
	}
	return lines
}

func firstLineMap(t *testing.T, l *lokiLog) map[string]string {
	t.Helper()
	lines := bufferedLines(t, l)
	if len(lines) == 0 {
		t.Fatal("no buffered entries")
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("unmarshal line: %v", err)
	}
	return m
}

// ctx 属性流必须在构建 JSON 行时合并进条目（Extra 之外同步携带）。
func TestContextAttrsMergedIntoLine(t *testing.T) {
	l := newTestLogger(t)

	ctx := log.ContextWithAttrs(context.Background(),
		slog.String("trace_id", "t-ctx-1"),
		slog.String("request_id", "r-1"),
	)
	l.Info(ctx, "hello", "user", "alice")

	m := firstLineMap(t, l)
	if m["trace_id"] != "t-ctx-1" {
		t.Errorf("trace_id = %q, want t-ctx-1", m["trace_id"])
	}
	if m["request_id"] != "r-1" {
		t.Errorf("request_id = %q, want r-1", m["request_id"])
	}
	if m["user"] != "alice" {
		t.Errorf("user = %q, want alice", m["user"])
	}
	if m["msg"] != "hello" {
		t.Errorf("msg = %q, want hello", m["msg"])
	}
}

// 调用参数同名 key 覆盖 ctx 属性流（ctx attrs 先合入，参数在后）。
func TestContextAttrsKeyPrecedence(t *testing.T) {
	l := newTestLogger(t)

	ctx := log.ContextWithAttrs(context.Background(), slog.String("trace_id", "from-ctx"))
	l.Info(ctx, "hello", "trace_id", "from-args")

	m := firstLineMap(t, l)
	if m["trace_id"] != "from-args" {
		t.Errorf("trace_id = %q, want from-args (call args take precedence)", m["trace_id"])
	}
}

// With 携带的 extra 与 ctx 属性流共存；派生实例写入共享缓冲，
// 从原实例即可观测（修复前派生实例持有独立缓冲）。
func TestWithAndContextAttrs(t *testing.T) {
	l := newTestLogger(t)

	ctx := log.ContextWithAttrs(context.Background(), slog.String("trace_id", "t-1"))
	l.With("module", "api").Info(ctx, "hello")

	m := firstLineMap(t, l) // 经原实例读共享缓冲
	if m["module"] != "api" {
		t.Errorf("module = %q, want api", m["module"])
	}
	if m["trace_id"] != "t-1" {
		t.Errorf("trace_id = %q, want t-1", m["trace_id"])
	}
}

// 缓冲共享端到端验证：派生实例的日志经原实例 Close 全量送达，
// 不形成孤岛缓冲（2026-09-13 修复的缺陷回归测试）。
func TestSharedBufferFlushOnParentClose(t *testing.T) {
	var mu sync.Mutex
	received := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload struct {
			Streams []struct {
				Values [][]string `json:"values"`
			} `json:"streams"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		for _, s := range payload.Streams {
			received += len(s.Values)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	l, err := NewLogger(WithEndpoint(srv.URL), WithBatchSize(100))
	if err != nil {
		t.Fatal(err)
	}
	root := l.(*lokiLog)

	derived := l.With("module", "api")
	derived.Info(nil, "from derived #1")
	derived.Info(nil, "from derived #2")
	l.Info(nil, "from parent")

	// 两条派生日志 + 一条原实例日志都在共享缓冲（未达 batchSize，未推送）。
	if got := len(bufferedLines(t, root)); got != 3 {
		t.Fatalf("want 3 shared buffered entries, got %d", got)
	}

	// 原实例 Close 冲刷全部（含派生实例写入的）。
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if received != 3 {
		t.Fatalf("server received %d entries, want 3", received)
	}
}

// nil ctx 与空 ctx：无属性流，不应 panic，行内不含 ctx 键。
func TestNilContextNoAttrs(t *testing.T) {
	l := newTestLogger(t)

	l.Info(nil, "nil ctx", "k", "v")
	l.Info(context.Background(), "empty ctx")

	lines := bufferedLines(t, l)
	if len(lines) != 2 {
		t.Fatalf("want 2 buffered entries, got %d", len(lines))
	}
	if strings.Contains(lines[0], "trace_id") {
		t.Errorf("nil ctx line should not contain ctx attrs: %s", lines[0])
	}
}
