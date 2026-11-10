package gin

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/circuitbreaker"
	"github.com/kalandramo/bald/circuitbreaker/hystrix"
)

// CircuitBreakerConfig 配置服务端入口熔断层，对应契约
// server.http.middleware.circuit_breaker（gRPC 面同段去掉 status_code，
// 见 pkg/middleware/bundle 的 GRPCCircuitBreaker）。
//
// 0 的读法与契约一致：字段为 0 表示「未设置」，由底层 hystrix 取自己的默认
// （阈值 0.5、样本量 20、开窗 5s、滑窗 10s、状态码 503）。proto3 里没有字段
// 存在性，「显式写 0」与「没写」不可区分，所以这里不能把 0 当配置错误——
// 那会让「只想改一个字段」的配置必须写满五个。负值与超界值才是错误。
type CircuitBreakerConfig struct {
	// ErrorThreshold 是开窗的失败率阈值，取值 (0,1]。0 → 默认 0.5。
	// 契约字段 circuit_breaker.error_threshold。
	ErrorThreshold float64

	// MinRequests 是参与判定的最小样本量（滑窗内请求数低于它不开窗）。
	// 0 → 默认 20。契约字段 circuit_breaker.min_requests。
	MinRequests int

	// OpenDurationMs 是开窗时长（毫秒），到期后半开、只放一个探针。
	// 0 → 默认 5000。契约字段 circuit_breaker.open_duration_ms。
	OpenDurationMs int64

	// WindowMs 是失败率统计滑窗长度（毫秒）。0 → 默认 10000。
	// 契约字段 circuit_breaker.window_ms。
	WindowMs int64

	// StatusCode 是开态（含半开被拒）返回的 HTTP 状态码。0 → 默认 503；
	// 非 0 时必须是 5xx。契约字段 circuit_breaker.status_code。
	StatusCode int
}

// DefaultCircuitBreakerStatusCode 是契约注释声明的开态默认状态码（503）。
const DefaultCircuitBreakerStatusCode = http.StatusServiceUnavailable

// Validate 检查显式给出的字段是否合法。0 一律合法（按未处理，见类型注释），
// 非法值返回错误 —— 调用方在启动期 fail-fast。
func (c CircuitBreakerConfig) Validate() error {
	if c.ErrorThreshold < 0 || c.ErrorThreshold > 1 {
		return fmt.Errorf("gin: circuit_breaker.error_threshold must be in (0,1], got %v", c.ErrorThreshold)
	}
	if c.MinRequests < 0 {
		return fmt.Errorf("gin: circuit_breaker.min_requests must be >= 0, got %d", c.MinRequests)
	}
	if c.OpenDurationMs < 0 {
		return fmt.Errorf("gin: circuit_breaker.open_duration_ms must be >= 0, got %d", c.OpenDurationMs)
	}
	if c.WindowMs < 0 {
		return fmt.Errorf("gin: circuit_breaker.window_ms must be >= 0, got %d", c.WindowMs)
	}
	if c.StatusCode != 0 && (c.StatusCode < 500 || c.StatusCode > 599) {
		return fmt.Errorf("gin: circuit_breaker.status_code must be 5xx or 0, got %d", c.StatusCode)
	}
	return nil
}

// CircuitBreaker 持有中间件与其底层熔断器。
//
// 生命周期纪律：hystrix 内部无后台 goroutine（滑窗按请求惰性推进），但它的
// Close 把实例置为「永久拒绝」——即 Close 之后本中间件对所有请求回
// cfg.StatusCode。因此 closer 只能在服务已停止接收流量之后调用；若停机顺序
// 是「先 close 再 drain」，drain 期间的请求会拿到 503。这是底层实现的既定
// 语义，本层不改写，也不掩盖。
type CircuitBreaker struct {
	breaker circuitbreaker.CircuitBreaker
	handler gin.HandlerFunc

	// statusCode 是开态（含半开被拒）的 HTTP 状态码（cfg.StatusCode 经默认值
	// 归一后的结果）。批次 4 后面段熔断换统一件（middleware.CircuitBreaker），
	// 本字段经 StatusCode() 逆映射成统一件的 openCode 语义（HTTPToCode），
	// 契约 status_code 字段语义在此保真；本件的 Handler() 保留为独立消费面。
	statusCode int

	closeOnce sync.Once
	closeErr  error
}

// StatusCode 返回开态 HTTP 状态码（默认 503；供统一件 openCode 逆映射）。
func (c *CircuitBreaker) StatusCode() int { return c.statusCode }

// NewCircuitBreaker 按配置构建熔断中间件（含底层 hystrix 熔断器）。
func NewCircuitBreaker(cfg CircuitBreakerConfig) (*CircuitBreaker, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// 0 值传给 Option 是 no-op（hystrix 的 setter 自己判 >0），默认值因此只有
	// 一个来源 = hystrix 包，不在这里抄第二份、也就不会各自漂移。
	cb := hystrix.New(
		hystrix.WithErrorThreshold(cfg.ErrorThreshold),
		hystrix.WithRequestVolumeThreshold(cfg.MinRequests),
		hystrix.WithSleepWindow(time.Duration(cfg.OpenDurationMs)*time.Millisecond),
		hystrix.WithWindow(time.Duration(cfg.WindowMs)*time.Millisecond),
	)
	status := cfg.StatusCode
	if status == 0 {
		status = DefaultCircuitBreakerStatusCode
	}
	return &CircuitBreaker{
		breaker:    cb,
		handler:    CircuitBreakerMiddleware(cb, status),
		statusCode: status,
	}, nil
}

// Handler 返回可直接挂进 gin 链的中间件。
func (c *CircuitBreaker) Handler() gin.HandlerFunc { return c.handler }

// Breaker 暴露底层熔断器（供 gRPC 面复用、观测与测试）。
func (c *CircuitBreaker) Breaker() circuitbreaker.CircuitBreaker { return c.breaker }

// Close 关闭底层熔断器（关闭后本层对所有请求回开态状态码，见类型注释）。
// 幂等（多次调用只关闭一次，返回同一结果）。
func (c *CircuitBreaker) Close() error {
	c.closeOnce.Do(func() {
		if c.breaker != nil {
			c.closeErr = c.breaker.Close()
		}
	})
	return c.closeErr
}

// CircuitBreakerMiddleware 返回基于给定熔断器的 gin 中间件。
//
// 与限流（[RateLimit]）的分工：限流判「这个主体还有没有配额」，熔断判
// 「这台服务现在值不值得真做」。链序上熔断在限流之内、超时之外（见
// bundle.Gin），三个判据写在契约注释里。
//
// 失败口径：响应状态码 >= 500 记失败，其余记成功。4xx 是调用方的错，计入
// 失败会让一个坏客户端替全体用户开窗。
//
// 上报义务：circuitbreaker 契约里「Allow 放行即欠一次 Mark*」，而半开态的
// 在途探针标记只有 Mark* 会复位——所以 panic 路径也必须在 defer 里上报后
// 再原样抛出。漏掉这一次上报的后果不是「少统计一条」，是**熔断永久拒绝**：
// 探针悬着 → halfOpenIn 恒真 → 之后每个请求都被判成「已有探针在途」。
// 被本层自己拒掉的请求发生在 Allow 之前，按契约不欠上报，也就不能上报
// （否则拒绝在喂养自己的失败率）。
func CircuitBreakerMiddleware(cb circuitbreaker.CircuitBreaker, statusCode int) gin.HandlerFunc {
	if statusCode <= 0 {
		statusCode = DefaultCircuitBreakerStatusCode
	}
	return func(c *gin.Context) {
		if cb == nil {
			c.Next()
			return
		}
		if err := cb.Allow(); err != nil {
			c.AbortWithStatus(statusCode)
			return
		}
		defer func() {
			if p := recover(); p != nil {
				cb.MarkFailure()
				panic(p)
			}
			if c.Writer.Status() >= http.StatusInternalServerError {
				cb.MarkFailure()
				return
			}
			cb.MarkSuccess()
		}()
		c.Next()
	}
}
