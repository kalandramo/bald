package kafka

import (
	"context"
	"errors"
	stdjson "encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	kafkaGo "github.com/segmentio/kafka-go"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/encoding"
)

// TestMain 注册一个 json codec：Publish 要过 broker.Marshal，而框架不用 init()
// 自注册，注册表里没有 "json" 时 marshal 直接 fail-fast。这里用 stdlib json 现搭
// 一个（模块只 require 了 encoding，没 require encoding/json，测试不该为此加依赖）。
type jsonCodec struct{}

func (jsonCodec) Marshal(v any) ([]byte, error) { return stdjson.Marshal(v) }

func (jsonCodec) Unmarshal(data []byte, v any) error { return stdjson.Unmarshal(data, v) }

func (jsonCodec) Name() string { return "json" }

func TestMain(m *testing.M) {
	if encoding.GetCodec("json") == nil {
		encoding.MustRegister(jsonCodec{})
	}
	os.Exit(m.Run())
}

// refusedAddr 留一个确定无人监听的本地端口：拨号立刻 ECONNREFUSED，于是
// 「写失败 / 读失败」这些分支不用起真实 broker 就能走到。
func refusedAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err = ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

// newTestBroker 按契约装配的同一步序（NewBroker → Init → Connect）构造指向 addr
// 的 broker，并把写侧压成「1 次尝试 + 2s 超时」，免得失败路径拖满 kafka-go 的
// 默认 10 次重试。
func newTestBroker(t *testing.T, addr string, opts ...broker.Option) *kafkaBroker {
	t.Helper()

	base := []broker.Option{
		broker.WithAddress(addr),
		WithWriterConfig(WriterConfig{
			Brokers:      []string{addr},
			Balancer:     &kafkaGo.LeastBytes{},
			MaxAttempts:  1,
			BatchSize:    1,
			BatchTimeout: 10 * time.Millisecond,
			ReadTimeout:  2 * time.Second,
			WriteTimeout: 2 * time.Second,
		}),
		WithEnableLogger(false),
		WithEnableErrorLogger(false),
	}

	b, ok := NewBroker(append(base, opts...)...).(*kafkaBroker)
	if !ok {
		t.Fatal("NewBroker 返回了非 *kafkaBroker")
	}
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := b.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = b.Disconnect() })
	return b
}

// writerCached 在 broker 自己的锁下看某 topic 是否已登记 writer。
// Writer.Close 之后容器是空 map 而不是 nil，所以这里只判 nil broker/writer。
func writerCached(b *kafkaBroker, topic string) bool {
	b.RLock()
	defer b.RUnlock()
	if b.writer == nil {
		return false
	}
	_, ok := b.writer.Writers[topic]
	return ok
}

func TestShouldRetryFreshWriter(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"leader not available", kafkaGo.Error(kafkaGo.LeaderNotAvailable), true},
		{"not leader for partition", kafkaGo.Error(kafkaGo.NotLeaderForPartition), true},
		{"network exception", kafkaGo.Error(kafkaGo.NetworkException), true},
		{"unknown topic（retriable）", kafkaGo.Error(kafkaGo.UnknownTopicOrPartition), true},
		// RequestTimedOut 同时在 Temporary 与 Timeout 集合里，判据把它剔掉：
		// 超时不原地重试，交给重建路径。
		{"request timed out", kafkaGo.Error(kafkaGo.RequestTimedOut), false},
		{"message size too large（不可重试）", kafkaGo.Error(kafkaGo.MessageSizeTooLarge), false},
		{"wrapped retriable", fmt.Errorf("publish: %w", kafkaGo.Error(kafkaGo.LeaderNotAvailable)), true},
		{"net refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, false},
		{"context canceled", context.Canceled, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldRetryFreshWriter(c.err); got != c.want {
				t.Fatalf("shouldRetryFreshWriter(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestPublishMultipleWriter_EvictsBrokenCachedWriter 驱动 kafka.go 里的重建分支：
// 首次写（map 未命中）把 writer 登记进 map；第二次写命中这个坏 writer，必须
// Close + 摘除 + 按 retriesCount 重建，全失败后不能把坏 writer 留在 map 里，
// 否则之后每次发布都要重付一次「命中坏 writer」的代价。
func TestPublishMultipleWriter_EvictsBrokenCachedWriter(t *testing.T) {
	b := newTestBroker(t, refusedAddr(t))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const topic = "bald.test.evict"
	msg := &broker.Message{Body: map[string]string{"v": "1"}}

	if err := b.Publish(ctx, topic, msg); err == nil {
		t.Fatal("指向无人监听地址的发布应当失败")
	}
	if !writerCached(b, topic) {
		t.Fatal("首次发布应把新建的 writer 登记进 map（这是重建路径的前提）")
	}

	if err := b.Publish(ctx, topic, msg); err == nil {
		t.Fatal("第二次发布仍应失败")
	}
	if writerCached(b, topic) {
		t.Fatalf("重建全失败后 %q 仍留在 writer map 里", topic)
	}
}

// TestPublishRetriesCountZeroSkipsRebuild 钉住重试上界：retriesCount=0 时重建
// 循环一次都不跑，坏 writer 照样被摘掉、错误照实返回。
//
// 这个计数没有对外的 Option（retriesCountKey 只被读、从没被写），默认恒为
// defaultRetriesCount=1，所以这里按包内白盒直接改字段。
func TestPublishRetriesCountZeroSkipsRebuild(t *testing.T) {
	b := newTestBroker(t, refusedAddr(t))
	b.retriesCount = 0

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const topic = "bald.test.retries0"
	msg := &broker.Message{Body: map[string]string{"v": "1"}}

	if err := b.Publish(ctx, topic, msg); err == nil {
		t.Fatal("first publish should fail")
	}
	if err := b.Publish(ctx, topic, msg); err == nil {
		t.Fatal("second publish should fail")
	}
	if writerCached(b, topic) {
		t.Fatalf("retries=0 时坏 writer 也必须被摘除，topic %q 仍在 map 里", topic)
	}
}

// TestPublishAfterReconnect 覆盖热更路径：Disconnect 会 Close 掉 writer 容器，
// 之后 Connect + Publish 不能再触碰失效状态。原先 Close 把 Writers 置成 nil map，
// 重连后的第一次发布是「往 nil map 写元素」直接 panic。
func TestPublishAfterReconnect(t *testing.T) {
	b := newTestBroker(t, refusedAddr(t))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const topic = "bald.test.reconnect"
	msg := &broker.Message{Body: map[string]string{"v": "1"}}

	if err := b.Publish(ctx, topic, msg); err == nil {
		t.Fatal("first publish should fail")
	}
	if err := b.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if err := b.Connect(); err != nil {
		t.Fatalf("re-Connect: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("重连后发布 panic（writer 容器未复位）: %v", r)
		}
	}()
	if err := b.Publish(ctx, topic, msg); err == nil {
		t.Fatal("publish against the refused address should still fail")
	}
}

// TestPublishOneWriter 走 EnableOneTopicOneWriter=false 的单 writer 分支：
// 失败同样要落回错误，且重建全失败后不能把坏 writer 留在结构体上。
func TestPublishOneWriter(t *testing.T) {
	b := newTestBroker(t, refusedAddr(t), WithEnableOneTopicOneWriter(false))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	msg := &broker.Message{Body: map[string]string{"v": "1"}}

	if err := b.Publish(ctx, "bald.test.onewriter", msg); err == nil {
		t.Fatal("first publish should fail")
	}
	b.RLock()
	seeded := b.writer.Writer != nil
	b.RUnlock()
	if !seeded {
		t.Fatal("首次发布应把新建的 writer 挂在 broker 上")
	}

	if err := b.Publish(ctx, "bald.test.onewriter", msg); err == nil {
		t.Fatal("second publish should fail")
	}
	b.RLock()
	kept := b.writer.Writer != nil
	b.RUnlock()
	if kept {
		t.Fatal("重建全失败后不该把坏 writer 重新挂回 broker")
	}
}

// TestPublishMarshalFailureShortCircuits：body 编码失败时不该碰 writer。
func TestPublishMarshalFailureShortCircuits(t *testing.T) {
	b := newTestBroker(t, refusedAddr(t))

	// chan 无法 json 编码
	err := b.Publish(context.Background(), "bald.test.marshal", &broker.Message{Body: make(chan int)})
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if writerCached(b, "bald.test.marshal") {
		t.Fatal("编码失败不该走到建 writer 这一步")
	}
}
