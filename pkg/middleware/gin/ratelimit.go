package gin

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/ratelimit"
	"github.com/kalandramo/bald/ratelimit/tokenbucket"
)

// ---------------------------------------------------------------------------
// 配置驱动的限流中间件（D5）
//
// 背景：契约 `server.http.middleware.rate_limit`（字段 rate/burst/wait）此前
// **零消费者**——`RateLimit` 段解析得到值却无人使用，业务只能自持配置段
// （bald-admin 的 `login.rate_limit.*`）绕行。本文件把该契约段接上真正的
// 中间件。
//
// 算法选择：[ratelimit/tokenbucket] 令牌桶——rate 为稳态速率，burst 为瞬时
// 突发容量，与契约字段一一对应（bbh/sentinel 无对应字段语义）。
// ---------------------------------------------------------------------------

// RateLimitConfig 是限流中间件的参数，对应契约 rate_limit 段。
//
// 语义映射：
//   - Rate  → 每秒补充的令牌数（稳态 QPS）；
//   - Burst → 桶容量（瞬时突发上限）；
//   - Wait  → true 时超限阻塞等待至令牌可用（平滑限流），false 时立即以
//     429 拒绝（快速失败，保护下游）。
type RateLimitConfig struct {
	Rate  float64
	Burst float64
	Wait  bool
}

// Validate 校验配置。Rate/Burst 必须为正——契约显式写了 rate_limit 段却给
// 非正值属配置错误，启动期 fail-fast（而非静默不生效）。
func (c RateLimitConfig) Validate() error {
	if c.Rate <= 0 {
		return fmt.Errorf("gin: rate_limit.rate must be > 0, got %v", c.Rate)
	}
	if c.Burst <= 0 {
		return fmt.Errorf("gin: rate_limit.burst must be > 0, got %v", c.Burst)
	}
	return nil
}

// RateLimiter 持有中间件与其底层 limiter 的生命周期。
//
// 生命周期纪律：limiter 内部无后台 goroutine（令牌按需惰性补充），但
// Close 会唤醒阻塞在 Wait 上的调用者——停机时应调用，避免 Wait 悬挂。
type RateLimiter struct {
	limiter ratelimit.Limiter
	handler gin.HandlerFunc
	wait    bool

	closeOnce sync.Once
	closeErr  error
}

// NewRateLimit 按配置构建限流中间件（含底层令牌桶）。
func NewRateLimit(cfg RateLimitConfig) (*RateLimiter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	l, err := tokenbucket.New(cfg.Rate, cfg.Burst)
	if err != nil {
		return nil, fmt.Errorf("gin: build token-bucket limiter: %w", err)
	}
	return &RateLimiter{
		limiter: l,
		handler: RateLimit(l, cfg.Wait),
		wait:    cfg.Wait,
	}, nil
}

// Handler 返回可直接挂进 gin 链的中间件。
func (r *RateLimiter) Handler() gin.HandlerFunc { return r.handler }

// Limiter 暴露底层 limiter（供观测/测试）。
func (r *RateLimiter) Limiter() ratelimit.Limiter { return r.limiter }

// Wait 报告该限流器是否处于等待模式。
func (r *RateLimiter) Wait() bool { return r.wait }

// Close 释放底层 limiter。幂等（多次调用只关闭一次，返回同一结果）。
func (r *RateLimiter) Close() error {
	r.closeOnce.Do(func() {
		if r.limiter != nil {
			r.closeErr = r.limiter.Close()
		}
	})
	return r.closeErr
}

// RateLimit 返回基于给定 limiter 的 gin 中间件。
//
// wait=false（默认快速失败）：令牌不足即以 429 拒绝，不进入后续中间件/handler。
// wait=true（平滑限流）：阻塞至令牌可用或请求 ctx 取消；ctx 取消同样以 429
// 结束（客户端已断开，继续排队无意义）。
func RateLimit(l ratelimit.Limiter, wait bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if wait {
			if err := l.Wait(c.Request.Context()); err != nil {
				c.AbortWithStatus(http.StatusTooManyRequests)
				return
			}
			c.Next()
			return
		}

		ok, err := l.Allow()
		if err != nil || !ok {
			c.AbortWithStatus(http.StatusTooManyRequests)
			return
		}
		c.Next()
	}
}
