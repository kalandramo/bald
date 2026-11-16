package asynq

import (
	"context"
	"testing"
	"time"
)

// TestStartFailsFastWhenRedisUnreachable 复现消费者假启动：asynq 的 Run 对
// 「连不上 Redis」不报错（后台处理器只在运行期记错误），此前 Start 把 Run 放进
// goroutine、错误只进日志，自己继续阻塞在 ctx.Done()——进程自报已启动，任务
// 却进了一个死消费者。修复后：Start 先对 Redis 探活（Ping），失败即失败；
// Run 的启动期错误也经 errChan 回传并上抛。
func TestStartFailsFastWhenRedisUnreachable(t *testing.T) {
	srv := NewServer(
		// 127.0.0.1:1 是拒绝连接的保留端口：不需要真实 Redis。
		WithRedisURI("redis://127.0.0.1:1/0"),
		WithServerEnabled(true),
		WithClientEnabled(false),
		WithSchedulerEnabled(false),
		WithShutdownTimeout(time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Redis 不可达时 Start 必须报错（修复前假启动并阻塞在 ctx.Done）")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Start 未在 15s 内返回（假启动阻塞）")
	}
}
