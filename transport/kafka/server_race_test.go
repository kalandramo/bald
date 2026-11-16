package kafka

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/kalandramo/bald/broker"
)

// TestStopConcurrentRegisterSubscriber 复现服务器生命周期映射的数据竞争：
// Stop 曾在无锁状态下遍历并重建 subscribers/subscriberOpts，Start 的延迟注册
// （doRegisterSubscriberMap）也在无锁状态下遍历+删除 subscriberOpts，而
// RegisterSubscriber 持 s.Lock() 写同一批映射——并发下是 concurrent map
// read/write fatal，-race 下必报 DATA RACE。修复后所有映射突变都在锁内，
// 本测试必须全绿。
func TestStopConcurrentRegisterSubscriber(t *testing.T) {
	srv := NewServer(WithAddress([]string{"127.0.0.1:19093"}), WithCodec("json"))
	// kafka 离线可 Start：Connect 只翻标志位，不需要真集群（见 server_unit_test.go）。
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	handler := func(ctx context.Context, event broker.Event) error { return nil }

	var wg sync.WaitGroup
	lifecycleDone := make(chan struct{})
	go func() {
		defer close(lifecycleDone)
		for i := 0; i < 20; i++ {
			_ = srv.Stop(context.Background())
			_ = srv.Start(context.Background())
		}
	}()
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				_ = srv.RegisterSubscriber(context.Background(),
					fmt.Sprintf("race-%d-%d", g, i), "queue", false, handler, nil)
			}
		}(g)
	}
	wg.Wait()
	<-lifecycleDone

	// 收尾：回到已启动态交给 Stop 清场；未注册成功的订阅由 Stop 统一退订。
	if !srv.started.Load() {
		_ = srv.Start(context.Background())
	}
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("final stop: %v", err)
	}
}
