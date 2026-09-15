// Package auditstream 把审计事件发布到 Redis Stream——bald audit.Auditor
// 的消息总线桥接（异步后端）。
//
// 异步语义：Record 仅把事件入内存缓冲 chan（非阻塞），由后台 goroutine
// 经 XADD 发布到 stream；chan 满或发布失败降级到 fallback（默认
// LoggerAuditor），绝不阻塞请求链路——审计旁路不阻断原则。
//
// 生命周期：Close 停止后台 goroutine 并 drain 剩余缓冲（发布失败的降级
// fallback，语义与后台消费一致），不丢弃已入队事件。签名适配
// appkit.ComponentFunc 的 dispose——纳入组件生命周期（C1）：
//
//	appkit.ComponentFunc("audit-stream", a.Close)
//
// 下游可用 consumer group 消费做异步分析/转发。
package auditstream

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/redis/go-redis/v9"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
)

// Option 配置 StreamAuditor。
type Option func(*streamConfig)

type streamConfig struct {
	stream   string
	buffer   int
	fallback audit.Auditor
}

// WithStream 自定义 Redis Stream 名（空串保持缺省 "audit.events"）。
func WithStream(name string) Option {
	return func(c *streamConfig) {
		if name != "" {
			c.stream = name
		}
	}
}

// WithBuffer 自定义内存缓冲大小（缺省 1024）。缓冲满时 Record 降级
// fallback（不阻塞、不丢事件）。
func WithBuffer(n int) Option {
	return func(c *streamConfig) { c.buffer = n }
}

// WithFallback 设置降级后端（缓冲满/发布失败时事件仍进 fallback）。
// 缺省 LoggerAuditor（结构化日志）；传 audit.NopAuditor() 关闭降级。
func WithFallback(a audit.Auditor) Option {
	return func(c *streamConfig) { c.fallback = a }
}

// StreamAuditor 把审计事件发布到 Redis Stream（异步）。
type StreamAuditor struct {
	rdb       redis.UniversalClient
	stream    string
	fallback  audit.Auditor
	ch        chan audit.AuditEvent
	stop      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New 构造 Redis Stream 审计后端并启动后台发布 goroutine。
// rdb 为 nil 时返回 nil（调用方跳过装配，与 go-bald-admin 同语义）。
func New(rdb redis.UniversalClient, opts ...Option) *StreamAuditor {
	if rdb == nil {
		return nil
	}
	cfg := streamConfig{
		stream:   "audit.events",
		buffer:   1024,
		fallback: audit.NewLoggerAuditor(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.buffer <= 0 {
		cfg.buffer = 1024
	}
	a := &StreamAuditor{
		rdb:      rdb,
		stream:   cfg.stream,
		fallback: cfg.fallback,
		ch:       make(chan audit.AuditEvent, cfg.buffer),
		stop:     make(chan struct{}),
	}
	a.wg.Add(1)
	go a.run()
	return a
}

// run 后台消费缓冲 chan，XADD 到 Redis Stream；失败时降级 fallback。
func (a *StreamAuditor) run() {
	defer a.wg.Done()
	for {
		select {
		case <-a.stop:
			return
		case ev := <-a.ch:
			if err := a.publish(context.Background(), ev); err != nil {
				log.Warn(context.Background(), "audit-stream: publish failed", "error", err.Error())
				a.recordFallback(context.Background(), ev)
			}
		}
	}
}

// publish 将事件 JSON 化后 XADD 到 stream（* 由 Redis 分配 id）。
func (a *StreamAuditor) publish(ctx context.Context, ev audit.AuditEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return a.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: a.stream,
		Values: map[string]interface{}{"event": string(b)},
	}).Err()
}

// Record 实现 audit.Auditor：非阻塞入队；队列满则降级 fallback
// （不丢事件、不阻断）。
func (a *StreamAuditor) Record(ctx context.Context, ev audit.AuditEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.Warn(ctx, "audit-stream: record panicked", "panic", r)
		}
	}()
	select {
	case a.ch <- ev:
	default:
		// 缓冲满：降级（不阻塞上游）。
		a.recordFallback(ctx, ev)
	}
}

// Close 停止后台 goroutine（等待其退出，无并发取 chan 竞态）并 drain
// 剩余缓冲事件（发布失败的降级 fallback，语义与后台消费一致）——不丢弃
// 已入队事件。幂等（sync.Once 防重复 close panic）。签名适配
// appkit.ComponentFunc dispose。
func (a *StreamAuditor) Close() error {
	a.closeOnce.Do(func() {
		close(a.stop)
	})
	a.wg.Wait()
	for {
		select {
		case ev := <-a.ch:
			if err := a.publish(context.Background(), ev); err != nil {
				log.Warn(context.Background(), "audit-stream: drain publish failed", "error", err.Error())
				a.recordFallback(context.Background(), ev)
			}
		default:
			return nil
		}
	}
}

// recordFallback 降级双写（fallback 为 nil 时静默丢弃）。
func (a *StreamAuditor) recordFallback(ctx context.Context, ev audit.AuditEvent) {
	if a.fallback != nil {
		a.fallback.Record(ctx, ev)
	}
}

// compile-time 断言 StreamAuditor 实现 audit.Auditor。
var _ audit.Auditor = (*StreamAuditor)(nil)
