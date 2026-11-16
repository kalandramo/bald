package kafka

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kalandramo/bald/broker"
)

// TestSubscribeBatchOptionsMustBePaired 批量订阅的两个参数必须成对声明：
// 只给 batchSize 时 batchInterval==0，processBatchMessage 的 time.NewTicker(0)
// 直接 panic（非正周期，后台 goroutine 崩全进程）；只给 interval 时 batchSize==0，
// 缓冲每收一条即清空，interval 形同虚设。修复前：Subscribe 返回 nil 错误，
// 随后订阅 goroutine panic（整棵测试树崩死，即「红」）；修复后：Subscribe
// 当场报错，进程存活。
func TestSubscribeBatchOptionsMustBePaired(t *testing.T) {
	b := newTestBroker(t, refusedAddr(t))

	handler := func(ctx context.Context, event broker.Event) error { return nil }

	if _, err := b.Subscribe("bald.batch.size-only", handler, nil,
		WithSubscribeBatchSize(10)); err == nil {
		t.Fatal("只给 batchSize 必须报错（修复前这里返回 nil 并在后台 panic）")
	}
	if _, err := b.Subscribe("bald.batch.interval-only", handler, nil,
		WithSubscribeBatchInterval(time.Second)); err == nil {
		t.Fatal("只给 batchInterval 必须报错（缓冲逐条清空，interval 无效）")
	}
	if _, err := b.Subscribe("bald.batch.negative", handler, nil,
		WithSubscribeBatchSize(-1), WithSubscribeBatchInterval(time.Second)); err == nil {
		t.Fatal("负 batchSize 必须报错（cap 为负的 make 也会 panic）")
	}

	// 成对声明是合法路径：订阅立起来，随后由退订收场。
	sub, err := b.Subscribe("bald.batch.both", handler, nil,
		WithSubscribeBatchSize(10), WithSubscribeBatchInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("成对声明的批量订阅不应报错: %v", err)
	}
	if err := sub.Unsubscribe(false); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
}

// TestPublishOneWriter_ConcurrentBrokenWriterRebuild 并发压 publishOneWriter 的
// 错误恢复路径：此前写入用的是解锁后裸读的 b.writer.Writer 字段，而并发发布者
// 的错误路径会在锁内置 nil / 换新——nil 解引用与撕裂读都在这条缝上。修复后
// 写入全程只用锁内捕获的局部 writer。本测试在 -race 下由 CI 的 race 闸门兜底；
// 非 race 模式断言并发下的错误语义不坍塌（每条发布都如实报错、不留脏状态）。
func TestPublishOneWriter_ConcurrentBrokenWriterRebuild(t *testing.T) {
	b := newTestBroker(t, refusedAddr(t), WithEnableOneTopicOneWriter(false))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	msg := &broker.Message{Body: map[string]string{"v": "1"}}
	const topic = "bald.test.onewriter.race"

	const goroutines = 8
	const publishes = 25

	var wg sync.WaitGroup
	errs := make([]error, goroutines*publishes)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < publishes; i++ {
				errs[g*publishes+i] = b.Publish(ctx, topic, msg)
			}
		}(g)
	}
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Fatalf("errs[%d] = nil：指向无人监听地址的发布不该报成功", i)
		}
	}
	// 收尾状态不判「writer 已摘除」：fresh 创建路径本就会把新 writer 挂回
	// broker（串行版 TestPublishOneWriter 判的是特定序列的终态），并发下
	// 终态取决于交错——这里只钉「无 panic + 语义不坍塌」，驱逐语义由串行版守。
}
