package loki

// loki_gaps_test.go —— 级别方法、选项、flush 的失败分支与 toString 全类型分支。

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLevelsAndEnabled(t *testing.T) {
	l := newTestLogger(t)
	l.Debug(nil, "d")
	l.Info(nil, "i")
	l.Warn(nil, "w")
	l.Error(nil, "e")
	if !l.Enabled(0) {
		t.Fatal("loki logger should enable all levels")
	}
	lines := bufferedLines(t, l)
	if len(lines) != 4 {
		t.Fatalf("buffered = %d, want 4", len(lines))
	}
	if !strings.Contains(lines[0], `"level":"DEBUG"`) || !strings.Contains(lines[3], `"level":"ERROR"`) {
		t.Fatalf("level fields missing: %v", lines)
	}
}

func TestNewLogger_EndpointRequired(t *testing.T) {
	if _, err := NewLogger(); err == nil || !strings.Contains(err.Error(), "endpoint is required") {
		t.Fatalf("want endpoint required error, got %v", err)
	}
}

func TestOptions_Apply(t *testing.T) {
	hc := &http.Client{}
	cfg := defaultOptions()
	WithLabel("env", "test")(cfg)
	WithBatchSize(7)(cfg)
	WithFlushInterval(2 * time.Second)(cfg)
	WithHTTPClient(hc)(cfg)
	if cfg.labels["env"] != "test" || cfg.labels["app"] != "default" {
		t.Fatalf("label merge failed: %v", cfg.labels)
	}
	if cfg.batchSize != 7 || cfg.flushInterval != 2*time.Second || cfg.httpClient != hc {
		t.Fatalf("options not applied: %+v", cfg)
	}

	// 标签表为 nil 时 WithLabel 自行初始化。
	empty := &options{}
	WithLabel("k", "v")(empty)
	if empty.labels["k"] != "v" {
		t.Fatalf("nil label map not initialized: %v", empty.labels)
	}

	// 自定义 client 生效（NewLogger 不覆盖）。
	l, err := NewLogger(WithEndpoint("http://example.invalid/push"), WithHTTPClient(hc))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	if l.(*lokiLog).s.client != hc {
		t.Fatal("custom http client not retained")
	}
}

func TestFlush_EmptyAndErrorPaths(t *testing.T) {
	// 空缓冲：直接返回 nil。
	empty := newTestLogger(t)
	if err := empty.flush(); err != nil {
		t.Fatalf("empty flush = %v", err)
	}

	// 非法 endpoint（控制字符）：NewRequest 失败。
	bad, err := NewLogger(WithEndpoint("http://bad\x00host"), WithBatchSize(100))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	bad.Info(nil, "x")
	if err := bad.Close(); err == nil || !strings.Contains(err.Error(), "create request") {
		t.Fatalf("want create request error, got %v", err)
	}

	// 连接失败：client.Do 报错。
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedAddr := lis.Addr().String()
	_ = lis.Close()
	down, err := NewLogger(WithEndpoint("http://"+closedAddr), WithBatchSize(100))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	down.Info(nil, "x")
	if err := down.Close(); err == nil || !strings.Contains(err.Error(), "push") {
		t.Fatalf("want push error, got %v", err)
	}

	// 服务端 5xx：状态码检查。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	l500, err := NewLogger(WithEndpoint(srv.URL), WithBatchSize(100))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	l500.Info(nil, "x")
	if err := l500.Close(); err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("want status 500 error, got %v", err)
	}
}

func TestPost_FlushesAtBatchThreshold(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	l, err := NewLogger(WithEndpoint(srv.URL), WithBatchSize(2), WithLabel("app", "t"))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	l.Info(nil, "1")
	if hits.Load() != 0 {
		t.Fatal("must not flush before threshold")
	}
	l.Info(nil, "2")
	if hits.Load() != 1 {
		t.Fatalf("flush hits = %d, want 1", hits.Load())
	}
	// 已冲刷，Close 空缓冲不产生请求。
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("empty Close should not push, hits = %d", hits.Load())
	}
}

type stringerVal struct{}

func (stringerVal) String() string { return "stringer" }

func TestToString_AllBranches(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{float64(1.5), "1.5"},
		{float32(2.5), "2.5"},
		{int(-3), "-3"},
		{uint(4), "4"},
		{int8(-5), "-5"},
		{uint8(6), "6"},
		{int16(-7), "-7"},
		{uint16(8), "8"},
		{int32(-9), "-9"},
		{uint32(10), "10"},
		{int64(-11), "-11"},
		{uint64(12), "12"},
		{"s", "s"},
		{true, "true"},
		{[]byte("bytes"), "bytes"},
		{stringerVal{}, "stringer"},
		{struct {
			A int `json:"a"`
		}{A: 1}, `{"a":1}`},
	}
	for _, c := range cases {
		if got := toString(c.in); got != c.want {
			t.Errorf("toString(%#v) = %q, want %q", c.in, got, c.want)
		}
	}

	// 不可 JSON 序列化的值：错误被忽略，返回空对象前的部分（空串）。
	if got := toString(func() {}); got != "" {
		t.Errorf("toString(func) = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// 结构性零覆盖（登记备查）：
//   - loki.go:192 —— flush 中 payload 的 json.Marshal 错误分支：payload 由
//     map[string]string（labels）与 [][]string（values）构成，json.Marshal
//     不可能失败，返回不可达。
// ---------------------------------------------------------------------------
