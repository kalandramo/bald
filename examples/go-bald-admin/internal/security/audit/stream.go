package audit

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/log"
)

// StreamAuditor 把审计事件发布到 Redis Stream（消息总线语义，M9 延伸异步后端）。
// 与 StoreAuditor（落库）对称，复用业务同一真实 Redis（bootstrap.RedisCache.Client()），
// 不引入 fake broker，符合 §0。下游可用 consumer group 消费做异步分析/转发。
//
// 异步语义：Record 仅把事件入内存缓冲 chan（非阻塞），由后台 goroutine 经 XADD 发布到
// stream；chan 满或发布失败降级到 fallback（默认 LoggerAuditor），绝不阻塞请求链路——
// 强化 M7「审计旁路不阻断」原则。
type StreamAuditor struct {
	rdb     *redis.Client
	stream  string
	fallback audit.Auditor
	ch      chan audit.AuditEvent
	stop    chan struct{}
}

// NewStream 构造 Redis Stream 审计后端并启动后台发布 goroutine；rdb 为 nil 返回 nil（调用方跳过）。
func NewStream(rdb *redis.Client) *StreamAuditor {
	if rdb == nil {
		return nil
	}
	a := &StreamAuditor{
		rdb:     rdb,
		stream:  "audit.events",
		fallback: New(),
		ch:      make(chan audit.AuditEvent, 1024),
		stop:    make(chan struct{}),
	}
	go a.run()
	return a
}

// run 后台消费缓冲 chan，XADD 到 Redis Stream；失败时降级 LoggerAuditor。
func (a *StreamAuditor) run() {
	for {
		select {
		case <-a.stop:
			return
		case ev := <-a.ch:
			if err := a.publish(context.Background(), ev); err != nil {
				log.GetLogger().Warn(context.Background(), "audit stream publish failed", "error", err.Error())
				if a.fallback != nil {
					a.fallback.Record(context.Background(), ev)
				}
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

// Record 实现 audit.Auditor：非阻塞入队；队列满则降级 fallback（不丢事件、不阻断）。
func (a *StreamAuditor) Record(ctx context.Context, ev audit.AuditEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.GetLogger().Warn(ctx, "StreamAuditor panic recovered", "error", r)
		}
	}()
	select {
	case a.ch <- ev:
	default:
		// 缓冲满：降级（不阻塞上游）。
		if a.fallback != nil {
			a.fallback.Record(ctx, ev)
		}
	}
}

// Stop 停止后台 goroutine（进程退出时调用，flush 剩余缓冲由调用方保证已 drain，此处仅退出）。
func (a *StreamAuditor) Stop() {
	close(a.stop)
}

// compile-time 断言 StreamAuditor 实现 audit.Auditor。
var _ audit.Auditor = (*StreamAuditor)(nil)

// MultiAuditor 组合多个审计后端（如落库 + 消息总线），逐个 Record，任一失败仅记日志不阻断。
type MultiAuditor struct {
	auditors []audit.Auditor
}

// NewMulti 构造组合审计后端；空切片返回 no-op（不记录）。
func NewMulti(auditors ...audit.Auditor) *MultiAuditor {
	live := auditors[:0]
	for _, a := range auditors {
		if a != nil {
			live = append(live, a)
		}
	}
	return &MultiAuditor{auditors: live}
}

// Record 依次调用各后端 Record；任一 panic/err 仅记日志（不阻断后续后端）。
func (m *MultiAuditor) Record(ctx context.Context, ev audit.AuditEvent) {
	for _, a := range m.auditors {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.GetLogger().Warn(ctx, "MultiAuditor backend panic recovered", "error", r)
				}
			}()
			a.Record(ctx, ev)
		}()
	}
}

var _ audit.Auditor = (*MultiAuditor)(nil)
