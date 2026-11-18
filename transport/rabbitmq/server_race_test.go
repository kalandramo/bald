package rabbitmq

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/kalandramo/bald/broker"

	"github.com/stretchr/testify/require"
)

// raceStubBroker 只为竞争测试注入 Subscribe/Disconnect/Init/Connect：真
// rabbitmq 离线 Connect 是完整 AMQP 握手必红（见 server_unit_test.go 的先例），
// 而竞争测试只需要「注册路径真的往订阅簿写元素」这一件事——started 经真实
// Start 路径置真（桩零错误），不再直翻标志位。
type raceStubBroker struct {
	broker.Broker
	sub broker.Subscriber
}

func (b *raceStubBroker) Init(...broker.Option) error { return nil }
func (b *raceStubBroker) Connect() error              { return nil }
func (b *raceStubBroker) Disconnect() error           { return nil }
func (b *raceStubBroker) Address() string             { return "amqp://race-stub:5672" }

func (b *raceStubBroker) Subscribe(topic string, _ broker.Handler, _ broker.Binder, _ ...broker.SubscribeOption) (broker.Subscriber, error) {
	return b.sub, nil
}

type raceStubSubscriber struct{}

func (s *raceStubSubscriber) Topic() string                            { return "race" }
func (s *raceStubSubscriber) Options() broker.SubscribeOptions         { return broker.SubscribeOptions{} }
func (s *raceStubSubscriber) Unsubscribe(removeFromManager bool) error { return nil }

// TestStopConcurrentRegisterSubscriber 复现服务器生命周期映射的数据竞争：
// Stop 曾在无锁状态下遍历并重建 subscribers/subscriberOpts，Start 的延迟注册
// 也在无锁遍历+删除，而 RegisterSubscriber 持 s.Lock() 写同一批映射——
// concurrent map read/write fatal，-race 下必报 DATA RACE。修复后所有映射
// 突变都在锁内（kernel 统一承担），本测试必须全绿。
func TestStopConcurrentRegisterSubscriber(t *testing.T) {
	srv := NewServer(WithAddress([]string{"amqp://127.0.0.1:55672"}), WithCodec("json"))
	srv.Broker = &raceStubBroker{sub: &raceStubSubscriber{}}
	// 离线 Start：桩零错误，started 经真实路径置真。
	require.NoError(t, srv.Start(context.Background()))

	handler := func(ctx context.Context, event broker.Event) error { return nil }

	var wg sync.WaitGroup
	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
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
					fmt.Sprintf("race-%d-%d", g, i), handler, nil)
			}
		}(g)
	}
	wg.Wait()
	<-stopDone

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("final stop: %v", err)
	}
}
