// Package sres implements a circuit breaker based on the Google SRE approach
// described in "Site Reliability Engineering" (Chapter 22).
//
// Unlike traditional threshold-based breakers, the SRE breaker uses a
// probabilistic acceptance model:
//
//	accept = max(0, (requests - K * errors) / (requests + 1))
//
// As the error ratio increases, the acceptance probability drops smoothly
// towards zero. There is no hard "open" / "closed" transition — instead the
// breaker probabilistically rejects requests, providing graceful degradation
// without abrupt service cutoff.
//
// The parameter K controls sensitivity:
//   - K = 1: rejects requests when error rate > 100% (never trips)
//   - K = 2: starts rejecting when error rate > 50%
//   - K = 0.5: starts rejecting when error rate > 200% (lenient)
//
// Example:
//
//	cb := sres.New(sres.WithK(2), sres.WithWindow(10*time.Second))
//	defer cb.Close()
//
//	err := cb.Execute(ctx, func() error {
//	    return downstreamCall()
//	})
package sres

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/kalandramo/bald/circuitbreaker"
)

var _ circuitbreaker.CircuitBreaker = (*Breaker)(nil)

const (
	defaultK           = 2.0
	defaultWindow      = 10 * time.Second
	defaultBucketCount = 40
)

// Option configures the SRE breaker.
type Option func(*config)

type config struct {
	k           float64       // sensitivity multiplier
	window      time.Duration // sliding-window length
	bucketCount int           // number of buckets
}

// WithK sets the sensitivity multiplier K. Default 2.0.
// Higher K is more lenient; lower K is more aggressive.
func WithK(k float64) Option {
	return func(c *config) {
		if k > 0 {
			c.k = k
		}
	}
}

// WithWindow sets the sliding-window length. Default 10s.
func WithWindow(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.window = d
		}
	}
}

// WithBucketCount sets the number of buckets in the window. Default 40.
func WithBucketCount(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.bucketCount = n
		}
	}
}

// New creates an SRE-based circuit breaker.
func New(opts ...Option) *Breaker {
	cfg := &config{
		k:           defaultK,
		window:      defaultWindow,
		bucketCount: defaultBucketCount,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	bucketDuration := cfg.window / time.Duration(cfg.bucketCount)
	return &Breaker{
		cfg:            cfg,
		bucketDuration: bucketDuration,
		buckets:        make([]sreBucket, cfg.bucketCount),
	}
}

// Breaker is a Google-SRE-style circuit breaker.
type Breaker struct {
	mu             sync.Mutex
	cfg            *config
	bucketDuration time.Duration
	buckets        []sreBucket
	// lastAbs 是上一次旋转所在的**绝对格号**（UnixNano/桶长，不取模）。
	// 旋转清桶按它算，见 rotateLocked。
	lastAbs   int64
	hasRotate bool
	closed    bool
}

type sreBucket struct {
	requests int64
	errors   int64
}

// Allow implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return circuitbreaker.ErrCircuitOpen
	}

	b.rotateLocked(time.Now())

	stats := b.statsLocked()

	// SRE acceptance formula: accept = max(0, (requests - K*errors) / (requests + 1))
	var accept float64
	if stats.requests > 0 {
		accept = float64(stats.requests-b.cfg.k*float64(stats.errors)) / (float64(stats.requests) + 1)
	} else {
		accept = 1 // no data yet — allow
	}

	if accept < 0 {
		accept = 0
	}

	if accept >= 1 {
		// Fully allow
		return nil
	}

	// Probabilistic acceptance
	if rand.Float64() < accept {
		return nil
	}

	return circuitbreaker.ErrCircuitOpen
}

// MarkSuccess implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) MarkSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	b.rotateLocked(now)
	idx := b.bucketIndexLocked(now)
	b.buckets[idx].requests++
}

// MarkFailure implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) MarkFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	b.rotateLocked(now)
	idx := b.bucketIndexLocked(now)
	b.buckets[idx].requests++
	b.buckets[idx].errors++
}

// Execute implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) Execute(ctx context.Context, fn func() error) error {
	if err := b.Allow(); err != nil {
		return err
	}

	err := fn()
	if err != nil {
		b.MarkFailure()
		return err
	}

	b.MarkSuccess()
	return nil
}

// State implements [circuitbreaker.CircuitBreaker].
//
// ⚠️ **语义提示**：SRE 的接受率 `accept = (requests - k*errors) / (requests + 1)`
// 对任何 `requests > 0` 都恒 `< 1`（分母比分子大 1），故：
//   - `StateClosed` **仅在窗内零请求时可达**（下方早退分支）；
//   - 窗内一旦有请求，只可能是 `StateHalfOpen`（accept<1 且 >0）或
//     `StateOpen`（accept<=0）。
//
// 即「本 breaker 是优雅降级而非三态熔断」——需要真正的三态语义请改用
// 阈值式实现（`circuitbreaker/hystrix`）。该性质由 `state_chars_test.go`
// 的特征化测试钉住（不改变本实现语义）。
//
// 「窗内」是限的：这里同样先旋转，于是静默过一整个窗后 State 回到 Closed——
// 与 Allow 在该时刻重新放行保持一致（否则读出来还在限流、发出去却放行）。
func (b *Breaker) State() circuitbreaker.State {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.rotateLocked(time.Now())

	stats := b.statsLocked()
	if stats.requests == 0 {
		return circuitbreaker.StateClosed
	}

	accept := (float64(stats.requests) - b.cfg.k*float64(stats.errors)) / (float64(stats.requests) + 1)
	if accept <= 0 {
		return circuitbreaker.StateOpen
	}
	if accept < 1 {
		return circuitbreaker.StateHalfOpen
	}
	return circuitbreaker.StateClosed
}

// Close implements [circuitbreaker.CircuitBreaker].
func (b *Breaker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

// windowStats aggregates the current window's request/error counts.
type windowStats struct {
	requests float64
	errors   float64
}

func (b *Breaker) statsLocked() windowStats {
	var s windowStats
	for _, bk := range b.buckets {
		s.requests += float64(bk.requests)
		s.errors += float64(bk.errors)
	}
	return s
}

// rotateLocked 按**绝对格点**清桶：两次旋转之间跨过的边界数 = curAbs - lastAbs。
//
// 不能拿「距上一次旋转的时长 / 桶长」当边界数：桶号是绝对时间取模，而旋转锚点
// 落在格内任意位置，两个基准会错开一格。错开方向不定，于是同时存在两种坏结果——
// 少清：整整一圈前的陈旧样本留在窗内，接受率被已经过期的失败拖着往下压（服务早已
// 恢复却持续被限）；多清：抹掉上一发刚写进去的那个桶，样本静默丢失，接受率虚高。
// 判据见 TestSlidingWindow_SameBucketSamplesAreNotLost 与
// TestSlidingWindow_LapOldSampleIsReplaced。
func (b *Breaker) rotateLocked(now time.Time) {
	curAbs := now.UnixNano() / int64(b.bucketDuration)
	if !b.hasRotate {
		b.hasRotate, b.lastAbs = true, curAbs
		return
	}
	steps := curAbs - b.lastAbs
	if steps <= 0 {
		return
	}
	n := int64(len(b.buckets))
	if steps >= n {
		b.resetBucketsLocked()
	} else {
		// 清 (lastAbs, curAbs]：含当前格——它装的是一整圈前的过期数据，清完才轮
		// 到本次样本写进去。**不含** lastAbs：那一格里的样本还在窗内。
		for i := int64(1); i <= steps; i++ {
			b.buckets[b.bucketIdxAbsLocked(b.lastAbs+i)] = sreBucket{}
		}
	}
	b.lastAbs = curAbs
}

// bucketIdxAbsLocked 把绝对格号折到桶下标（对负格号同样成立）。
func (b *Breaker) bucketIdxAbsLocked(abs int64) int {
	n := int64(len(b.buckets))
	m := abs % n
	if m < 0 {
		m += n
	}
	return int(m)
}

func (b *Breaker) bucketIndexLocked(now time.Time) int {
	return b.bucketIdxAbsLocked(now.UnixNano() / int64(b.bucketDuration))
}

func (b *Breaker) resetBucketsLocked() {
	for i := range b.buckets {
		b.buckets[i] = sreBucket{}
	}
}
