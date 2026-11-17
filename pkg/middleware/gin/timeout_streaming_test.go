package gin

// timeout_streaming_test.go 覆盖 W-12 收口后的缓冲上限 / 流式退化 / Flush 透传
// 与并发安全。回归项（小响应 200、小响应超时 503、自定义 504、超时后迟到写入
// 丢弃）由 middleware_config_test.go 既有测试覆盖。

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// flushCountingRecorder 记录底层 http.ResponseWriter 被请求的 Flush 次数与
// WriteHeader 序列，用于断言 Flush 真透传、以及流式后超时没有 superfluous
// WriteHeader。嵌入 httptest.ResponseRecorder 以满足 gin.ResponseWriter 的底层
// http.ResponseWriter 需要。
type flushCountingRecorder struct {
	*httptest.ResponseRecorder
	mu          sync.Mutex
	flushCount  int
	headerCalls []int
}

func newFlushCountingRecorder() *flushCountingRecorder {
	return &flushCountingRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *flushCountingRecorder) Flush() {
	r.mu.Lock()
	r.flushCount++
	r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

func (r *flushCountingRecorder) WriteHeader(code int) {
	r.mu.Lock()
	r.headerCalls = append(r.headerCalls, code)
	r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(code)
}

func (r *flushCountingRecorder) flushes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushCount
}

func (r *flushCountingRecorder) headers() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.headerCalls...)
}

// --- 超限转流式：body 完整不截断（端到端）---

func TestTimeout_OverflowSwitchesToStreamingCompleteBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Timeout(TimeoutConfig{DefaultTimeoutMs: 5000, MaxBufferBytes: 1024}))
	payload := strings.Repeat("x", 64*1024+257) // 远超默认 64KiB，也远超显式 1KiB
	r.GET("/big", func(c *gin.Context) {
		c.Status(http.StatusOK)
		_, _ = c.Writer.WriteString(payload)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/big", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("未超时且 handler 显式 200，got %d", w.Code)
	}
	if w.Body.Len() != len(payload) || w.Body.String() != payload {
		t.Fatalf("超限转流式后 body 应完整：got %d bytes want %d", w.Body.Len(), len(payload))
	}
}

// --- 超限转流式：状态机层面断言单向切换、已缓冲内容先落盘、本次写入不丢 ---

func TestBufferedResponseWriter_OverflowSwitchState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	bw := &bufferedResponseWriter{
		ResponseWriter: c.Writer,
		body:           &bytes.Buffer{},
		hdr:            make(http.Header),
		maxBufferBytes: 8,
	}

	bw.Header().Set("X-Test", "1")
	bw.WriteHeader(http.StatusOK)
	if _, err := bw.Write([]byte("1234")); err != nil {
		t.Fatalf("缓冲内写入失败: %v", err)
	}
	if bw.IsStreaming() {
		t.Fatal("未超限不应切流式")
	}

	// 已缓冲 4 + 本次 9 > 8 → 切流式：先提交 "1234"，再直写 "123456789"。
	n, err := bw.Write([]byte("123456789"))
	if err != nil || n != 9 {
		t.Fatalf("超限写入: n=%d err=%v", n, err)
	}
	if !bw.IsStreaming() {
		t.Fatal("超限后应切流式")
	}
	if got := rec.Body.String(); got != "1234123456789" {
		t.Fatalf("已缓冲 + 本次应按序到达: got %q", got)
	}
	if rec.Code != http.StatusOK || rec.Header().Get("X-Test") != "1" {
		t.Fatalf("提交应携带 code/header: code=%d X-Test=%q", rec.Code, rec.Header().Get("X-Test"))
	}

	// 流式单向不可逆，后续写入直接透传。
	_, _ = bw.Write([]byte("Z"))
	if got := rec.Body.String(); !strings.HasSuffix(got, "Z") {
		t.Fatalf("流式后写入应透传: got %q", got)
	}

	// 流式后超时：放弃改写 503，只断流。
	if bw.timeoutResponse(http.StatusServiceUnavailable) {
		t.Fatal("流式后超时不应写超时响应")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("流式后状态码应保持 200，got %d", rec.Code)
	}
	_, _ = bw.Write([]byte("LATE"))
	if strings.Contains(rec.Body.String(), "LATE") {
		t.Fatal("超时断流后写入应被丢弃")
	}
}

// --- Flush 透传：两次写中间 Flush，Flush 后数据已到 recorder，且 Flush 次数被记录 ---

func TestTimeout_FlushPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Timeout(TimeoutConfig{DefaultTimeoutMs: 5000}))
	rec := newFlushCountingRecorder()

	var bodyAtFlush string
	r.GET("/sse", func(c *gin.Context) {
		c.Status(http.StatusOK)
		_, _ = c.Writer.WriteString("first")
		c.Writer.Flush()
		// Flush 后立刻观察底层 recorder：数据应已透传，不必等 handler 返回。
		bodyAtFlush = rec.Body.String()
		_, _ = c.Writer.WriteString("second")
	})
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sse", nil))

	if bodyAtFlush != "first" {
		t.Fatalf("Flush 后数据应已到 recorder，got %q", bodyAtFlush)
	}
	if rec.flushes() < 1 {
		t.Fatalf("Flush 应透传到真实 writer，flushCount=%d", rec.flushes())
	}
	if rec.Body.String() != "firstsecond" {
		t.Fatalf("完整 body 应为 firstsecond，got %q", rec.Body.String())
	}
	if hs := rec.headers(); len(hs) != 1 || hs[0] != http.StatusOK {
		t.Fatalf("应只有一次 WriteHeader(200)，got %v", hs)
	}
}

// --- 流式后超时：状态码保持 handler 的 200，无 superfluous WriteHeader ---

func TestTimeout_StreamingThenTimeoutKeepsStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Timeout(TimeoutConfig{DefaultTimeoutMs: 40}))
	rec := newFlushCountingRecorder()
	r.GET("/sse", func(c *gin.Context) {
		c.Status(http.StatusOK)
		_, _ = c.Writer.WriteString("hello")
		c.Writer.Flush() // 切流式，状态码 200 上线
		time.Sleep(300 * time.Millisecond)
		_, _ = c.Writer.WriteString("late") // 超时后应断流丢弃
	})
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sse", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("流式后超时不应改写状态码为 503，got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "late") {
		t.Fatalf("超时后写入应被丢弃，body=%q", rec.Body.String())
	}
	if hs := rec.headers(); len(hs) != 1 || hs[0] != http.StatusOK {
		t.Fatalf("无 superfluous WriteHeader：应只有一次 WriteHeader(200)，got %v", hs)
	}
}

// --- 并发写 / Flush / 超时：-race 下无数据竞争、无 panic ---

func TestTimeout_ConcurrentWriteFlushTimeoutRace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Timeout(TimeoutConfig{DefaultTimeoutMs: 15}))
	rec := newFlushCountingRecorder()
	r.GET("/race", func(c *gin.Context) {
		c.Status(http.StatusOK)
		cw := c.Writer // 只读取一次，goroutine 内不再碰 gin.Context
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 64; j++ {
					_, _ = cw.Write([]byte("x"))
					if j%8 == 0 {
						cw.Flush()
					}
				}
			}()
		}
		wg.Wait()
	})
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/race", nil))

	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("并发路径状态码应为 200 或 503，got %d", rec.Code)
	}
}
