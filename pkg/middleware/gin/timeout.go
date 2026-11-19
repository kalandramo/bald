package gin

import (
	"bytes"
	"context"
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

	// MaxBufferBytes 是响应体缓冲上限（字节）。<=0 时取 DefaultMaxBufferBytes。
	// 一旦「已缓冲 + 本次写入」超过上限，中间件切到流式透传：本次及后续写入
	// 直接落到真实 writer，从此**永久放弃**「超时改写 503」的能力（状态码
	// 只能写一次，首字节上线后无法改）。bundle 翻译层已把契约字段
	// server.http.middleware.timeout.max_buffer_bytes 透传到本字段；未配置
	// 该字段时走 64 KiB 默认上限。
	MaxBufferBytes int
}

// DefaultTimeoutStatusCode 是契约注释声明的默认超时状态码（503）。
const DefaultTimeoutStatusCode = http.StatusServiceUnavailable

// DefaultMaxBufferBytes 是响应体缓冲的默认硬上限（64 KiB）。
// 把每请求峰值内存从 ≈2× 响应体压到 ≈2×64 KiB（W-12）。
const DefaultMaxBufferBytes = 64 * 1024

// bufferPool 复用 bytes.Buffer 以降低每请求分配。**归还时机严格晚于 handler
// goroutine 结束**：Timeout 的两条退出路径都等待过 done（正常分支 done 已关闭；
// 超时分支显式 <-done），此时 buffer 不再被任何 goroutine 引用。
var bufferPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// Timeout 返回请求超时中间件：handler 在 cfg.DefaultTimeoutMs 内未返回，即以
// cfg.StatusCode（默认 503）结束响应。
//
// ⚠️ 超时不中断 handler（Go 无法杀 goroutine）：超时后 handler 仍在后台运行至
// 返回；其写入被静默丢弃。只做 HTTP 超时（不设 ctx deadline）以免与上层
// contextx 包装冲突。
//
// 缓冲与流式（W-12 决策）：响应体在 64 KiB 以内全缓冲，超时可改写 503。超过
// 上限、或 handler 主动 Flush() 时，中间件把已缓冲的 header+code+body 一次性
// 提交给真实 writer 并切到流式；此后状态码已上线，超时分支**不再写超时码**，
// 只做断流（丢弃后续写入）。这是协议硬约束下的必然代价，不是 bug。
//
// 并发模型：handler 独立 goroutine 跑 c.Next()；main 计时。bufferedResponseWriter
// 的 mu 同时保护「状态切换」与「对真实 writer 的写」，使 handler 的流式提交与
// main 的超时响应严格串行。gin.Context 非并发安全：main 在超时分支只碰真实
// writer，不动 c.Abort/c.Writer；等 handler 结束后才归还 c.Writer 与 buffer。
func Timeout(cfg TimeoutConfig) gin.HandlerFunc {
	status := cfg.StatusCode
	if status <= 0 {
		status = DefaultTimeoutStatusCode
	}
	limit := cfg.MaxBufferBytes
	if limit <= 0 {
		limit = DefaultMaxBufferBytes
	}
	timeout := time.Duration(cfg.DefaultTimeoutMs) * time.Millisecond

	return func(c *gin.Context) {
		original := c.Writer
		buf := bufferPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer func() {
			buf.Reset()
			bufferPool.Put(buf)
		}()

		bw := &bufferedResponseWriter{
			ResponseWriter: original,
			body:           buf,
			hdr:            make(http.Header),
			maxBufferBytes: limit,
		}
		c.Writer = bw

		// AR-13：handler 的 ctx 带超时 deadline——能感知 ctx 的 handler 在
		// 超时点自行返回，响应随之中结（此前 ctx 永不取消，超时完全依赖本
		// 中间件的重写 503）。「503 后立即返回」的早返回半版经评审正式关闭
		// （gin v1.12 Copy() 置空 handlers+ResponseWriter，副本不能跑 handler
		// 链；gin 池化复用与超时后仍在跑的 handler 竞争同一份 Context——gin
		// 架构约束，无安全实现）。不感知 ctx 的 handler 拖长响应体关闭是
		// 已知残留，影响是时延而非正确性。
		timeoutCtx, cancelTimeoutCtx := context.WithTimeout(c.Request.Context(), timeout)
		defer cancelTimeoutCtx()
		c.Request = c.Request.WithContext(timeoutCtx)

		done := make(chan struct{})
		var panicked any
		go func() {
			defer func() {
				// handler panic 必须带回主 goroutine 重抛：外层 Recovery 的
				// defer 在另一条 goroutine 上接不到这里逃逸的 panic，不重抛
				// 就是进程崩溃（AR-4）。写入先于 close(done)，主 goroutine
				// 在 <-done 之后读取，通道关闭建立 happens-before。
				if r := recover(); r != nil {
					panicked = r
				}
				close(done)
			}()
			c.Next()
		}()

		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case <-done:
			// 正常完成：已流式则响应已直写真实 writer，不能再 snapshot 重放
			// （否则重复 WriteHeader 且 body 已被清空）；否则原样刷出缓冲。
			if bw.IsStreaming() {
				c.Writer = original
				if panicked != nil {
					panic(panicked) // 交还外层 Recovery（本 goroutine 的 defer 链）
				}
				return
			}
			code, hdr, body := bw.snapshot()
			c.Writer = original
			dst := original.Header()
			for k, vv := range hdr {
				dst[k] = vv
			}
			if code != 0 {
				original.WriteHeader(code)
			}
			if len(body) > 0 {
				_, _ = original.Write(body)
			}
			if panicked != nil {
				panic(panicked)
			}
		case <-timer.C:
			// 超时：流式时状态码已上线，协议上无法改写 503 —— 只断流。
			// 未流式则在 mu 下写超时响应，与 handler 的流式提交串行。
			bw.timeoutResponse(status)
			<-done
			c.Writer = original
			// 超时分支里 handler 的 panic 同样交还外层 Recovery：响应已以
			// 503 结束，Recovery 按 gin 语义跳过重复写响应，只做日志与链终止。
			if panicked != nil {
				panic(panicked)
			}
		}
	}
}

// bufferedResponseWriter 缓冲 handler 的输出（状态码/头/体），直到 snapshot、
// 超限、或 Flush 时提交。头也必须缓冲：直接透传到真实 writer 会让 handler
// goroutine 与计时 goroutine 并发写同一份 header map。
//
// body 与 hdr 的字段名/类型被既有测试直接构造，保持不变。
type bufferedResponseWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
	hdr  http.Header

	mu             sync.Mutex
	code           int  // 显式 WriteHeader 的状态码（0 = 未设置）
	discarded      bool // 超时后置位：后续写入丢弃（流式后置位即断流）
	streaming      bool // 已切流式：单向不可逆，状态码已上线
	maxBufferBytes int  // <=0 时按 DefaultMaxBufferBytes 处理
}

// Header 返回缓冲头，避免 handler 直接写真实 writer 的 header。
func (w *bufferedResponseWriter) Header() http.Header { return w.hdr }

// WriteHeaderNow 推迟到提交时统一下发（缓冲阶段不触碰真实 writer）。
func (w *bufferedResponseWriter) WriteHeaderNow() {}

// Flush 透传：
//   - 已丢弃（超时断流）：空操作，不再向真实 writer 写任何东西；
//   - 已流式：直接透传给真实 writer 的 Flush；
//   - 缓冲态且有内容：先提交已缓冲内容并切流式，再透传 Flush（SSE 靠此生效）；
//   - 缓冲态且无内容：空操作（保持 Status/Written 语义，兼容既有接口测试）。
func (w *bufferedResponseWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.discarded {
		return
	}
	if w.streaming {
		w.ResponseWriter.Flush()
		return
	}
	if w.code != 0 || w.body.Len() > 0 {
		w.commitBufferedLocked()
		w.ResponseWriter.Flush()
	}
}

// Size 报告已缓冲的字节数（gin 日志/观测读取）。
func (w *bufferedResponseWriter) Size() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Len()
}

// WriteHeader 缓冲态仅记录状态码（超时路径要能改写 503）；流式态直接透传，
// net/http 会忽略已提交状态码上的重复写入。
func (w *bufferedResponseWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.discarded {
		return
	}
	if w.streaming {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.code == 0 {
		w.code = code
	}
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
	return w.streaming || w.code != 0 || w.body.Len() > 0
}

// IsStreaming 报告是否已切流式（单向不可逆），供测试/观测断言。
func (w *bufferedResponseWriter) IsStreaming() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.streaming
}

// limit 返回本 writer 的有效缓冲上限（<=0 取默认 64 KiB）。
func (w *bufferedResponseWriter) limit() int {
	if w.maxBufferBytes <= 0 {
		return DefaultMaxBufferBytes
	}
	return w.maxBufferBytes
}

func (w *bufferedResponseWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.discarded {
		return len(b), nil // 超时/断流后静默丢弃：不报错、不 panic
	}
	if w.streaming {
		return w.ResponseWriter.Write(b)
	}
	if w.body.Len()+len(b) > w.limit() {
		// 超限：先提交已缓冲的 header+code+body，再透传本次写入，顺序不丢。
		w.commitBufferedLocked()
		return w.ResponseWriter.Write(b)
	}
	return w.body.Write(b)
}

func (w *bufferedResponseWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// commitBufferedLocked 把已缓冲的 header+code+body 一次性提交给真实 writer，
// 并切到流式。**调用者必须持有 w.mu**：持锁做这次网络写牺牲了一点并发度，
// 换来与超时分支的严格串行——否则 main 可能在提交中途插入 503，导致状态码
// 交错/重复。提交量受 64 KiB 上限约束，持锁时间有界（见提交说明）。
func (w *bufferedResponseWriter) commitBufferedLocked() {
	w.streaming = true
	dst := w.ResponseWriter.Header()
	for k, vv := range w.hdr {
		dst[k] = vv
	}
	if w.code != 0 {
		w.ResponseWriter.WriteHeader(w.code)
	}
	if w.body.Len() > 0 {
		_, _ = w.ResponseWriter.Write(w.body.Bytes())
	}
	w.body.Reset()
}

// timeoutResponse 在超时分支调用。已流式时状态码已上线，协议上不能再写
// 503：只置 discarded 断流并返回 false；未流式则在 mu 下写超时响应并 Flush。
func (w *bufferedResponseWriter) timeoutResponse(status int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.discarded {
		return false
	}
	w.discarded = true
	if w.streaming {
		return false
	}
	w.ResponseWriter.WriteHeader(status)
	w.ResponseWriter.Flush()
	return true
}

// snapshot 返回已缓冲的状态码、响应头副本与响应体（供正常路径 flush）。
func (w *bufferedResponseWriter) snapshot() (int, http.Header, []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.code, w.hdr.Clone(), w.body.Bytes()
}

// markDiscarded 置位丢弃标志（既有接口测试直接调用，保留）。
func (w *bufferedResponseWriter) markDiscarded() {
	w.mu.Lock()
	w.discarded = true
	w.mu.Unlock()
}
