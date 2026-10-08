package gin

import (
	"bytes"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// TimeoutConfig 配置请求超时中间件，对应契约 timeout 段。
type TimeoutConfig struct {
	// DefaultTimeoutMs 是请求处理超时（毫秒）。必须 > 0。
	// 契约字段 server.http.middleware.timeout.default_timeout_ms。
	DefaultTimeoutMs int64

	// StatusCode 是超时后返回的 HTTP 状态码。<=0 时取默认 503。
	// 契约字段 server.http.middleware.timeout.status_code。
	StatusCode int
}

// DefaultTimeoutStatusCode 是契约注释声明的默认超时状态码（503）。
const DefaultTimeoutStatusCode = http.StatusServiceUnavailable

// Timeout 返回请求超时中间件：handler 在 cfg.DefaultTimeoutMs 内未返回，即以
// cfg.StatusCode（默认 503）结束响应。
//
// ⚠️ 必须知晓的代价（不静默掩盖）：**超时不会中断 handler**——Go 无法杀死
// goroutine。超时后 handler 仍在后台运行至自行返回；其后续写入被**静默丢弃**
// （不 panic、不污染已发出的超时响应）。即本中间件保证「**响应**及时返回」，
// 不保证「**工作**停止」。要真正终止，handler 须监听 c.Request.Context().Done
// 并自行清理；只做 HTTP 超时（不设 ctx deadline）以免与上层的 contextx 包装冲突。
//
// 并发模型：handler 在独立 goroutine 跑 c.Next()，写入缓冲 writer；主 goroutine
// 计时。两条路径对真实 writer（original）的访问是**互斥**的——只有 flushTo(done)
// 或超时分支会碰 original，且 done 关闭意味着 c.Next 已返回。缓冲 writer 内部
// 用 mutex 保护，超时后 handler 的并发写入安全丢弃。
func Timeout(cfg TimeoutConfig) gin.HandlerFunc {
	status := cfg.StatusCode
	if status <= 0 {
		status = DefaultTimeoutStatusCode
	}
	timeout := time.Duration(cfg.DefaultTimeoutMs) * time.Millisecond

	return func(c *gin.Context) {
		original := c.Writer
		bw := &bufferedResponseWriter{ResponseWriter: original, body: &bytes.Buffer{}}
		c.Writer = bw

		done := make(chan struct{})
		go func() {
			defer close(done)
			c.Next()
		}()

		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case <-done:
			// 正常完成：把缓冲的响应原样刷给客户端。
			code, body := bw.snapshot()
			c.Writer = original
			if code != 0 {
				original.WriteHeader(code)
			}
			if len(body) > 0 {
				_, _ = original.Write(body)
			}
		case <-timer.C:
			// 超时：丢弃 handler 的后续写入，直接向真实 writer 写超时响应。
			bw.markDiscarded()
			c.Writer = original
			c.AbortWithStatus(status)
		}
	}
}

// bufferedResponseWriter 缓冲 handler 的输出，直到 snapshot 或 markDiscarded。
type bufferedResponseWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer

	mu        sync.Mutex
	code      int  // 显式 WriteHeader 的状态码（0 = 未设置）
	discarded bool // 超时后置位：后续写入丢弃
}

// WriteHeader 仅记录状态码，不立刻下发（超时路径要能改写成 503）。
func (w *bufferedResponseWriter) WriteHeader(code int) {
	w.mu.Lock()
	if !w.discarded && w.code == 0 {
		w.code = code
	}
	w.mu.Unlock()
}

// Status 报告当前状态码（未显式设置时为 200，与 net/http 语义一致）。
func (w *bufferedResponseWriter) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}

// Written 报告是否已写入（gin ResponseWriter 契约的一部分）。
func (w *bufferedResponseWriter) Written() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.code != 0 || w.body.Len() > 0
}

func (w *bufferedResponseWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.discarded {
		return len(b), nil // 超时后静默丢弃：不报错、不 panic
	}
	return w.body.Write(b)
}

func (w *bufferedResponseWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// snapshot 返回已缓冲的状态码与响应体（供 flush）。
func (w *bufferedResponseWriter) snapshot() (int, []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.code, w.body.Bytes()
}

// markDiscarded 置位丢弃标志（超时后调用）。
func (w *bufferedResponseWriter) markDiscarded() {
	w.mu.Lock()
	w.discarded = true
	w.mu.Unlock()
}
