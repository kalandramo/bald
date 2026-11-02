package rabbitmq

import (
	"context"
	"net"
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/kalandramo/bald/broker"
)

// refusedAddr 留一个确定无人监听的本地端口：拨号立刻 ECONNREFUSED，
// 于是「连接失败 / 未连接」这些分支不用起真实 AMQP server 就能走到。
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

func TestHasUrlPrefix(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"amqp://guest:guest@127.0.0.1:5672", true},
		{"amqps://127.0.0.1:5671", true},
		{"amqp://", true},
		{"127.0.0.1:5672", false},
		{"http://127.0.0.1:5672", false},
		// 正则没有锚在行尾以外的大小写无关标志，scheme 必须小写
		{"AMQP://127.0.0.1:5672", false},
		{"", false},
	}
	for _, c := range cases {
		if got := hasUrlPrefix(c.url); got != c.want {
			t.Errorf("hasUrlPrefix(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

// TestInitNormalizesAddrs 钉住地址归一化：Init 之后每个地址都带 scheme，
// 连接侧只认 hasUrlPrefix 通过的地址，漏加前缀会静默退回 DefaultRabbitURL。
func TestInitNormalizesAddrs(t *testing.T) {
	cases := []struct {
		name string
		adds []string
		want []string
	}{
		{"裸 host:port 补 amqp://", []string{"127.0.0.1:5672"}, []string{"amqp://127.0.0.1:5672"}},
		{"已带 scheme 原样保留", []string{"amqps://rb:5671"}, []string{"amqps://rb:5671"}},
		{"空串被丢弃", []string{"", "127.0.0.1:5672"}, []string{"amqp://127.0.0.1:5672"}},
		{"全空退回默认地址", []string{""}, []string{DefaultRabbitURL}},
		{"无地址退回默认地址", nil, []string{DefaultRabbitURL}},
		{"多地址逐个归一化", []string{"a:1", "amqp://b:2"}, []string{"amqp://a:1", "amqp://b:2"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := make([]broker.Option, 0, len(c.adds))
			if len(c.adds) > 0 {
				opts = append(opts, broker.WithAddress(c.adds...))
			}

			b, ok := NewBroker(opts...).(*rabbitBroker)
			if !ok {
				t.Fatal("NewBroker 返回了非 *rabbitBroker")
			}
			if err := b.Init(); err != nil {
				t.Fatalf("Init: %v", err)
			}
			if got := b.options.Addrs; len(got) != len(c.want) {
				t.Fatalf("Addrs = %v, want %v", got, c.want)
			} else {
				for i := range got {
					if got[i] != c.want[i] {
						t.Fatalf("Addrs[%d] = %q, want %q", i, got[i], c.want[i])
					}
				}
			}
		})
	}
}

func TestNameAndAddress(t *testing.T) {
	b, ok := NewBroker(broker.WithAddress("10.0.0.1:5672")).(*rabbitBroker)
	if !ok {
		t.Fatal("NewBroker 返回了非 *rabbitBroker")
	}
	if got := b.Name(); got != "rabbitmq" {
		t.Fatalf("Name() = %q", got)
	}
	// Address 取的是原始 Addrs，Init 之前没有 scheme 前缀
	if got := b.Address(); got != "10.0.0.1:5672" {
		t.Fatalf("Address() before Init = %q", got)
	}
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := b.Address(); !strings.HasPrefix(got, "amqp://") {
		t.Fatalf("Address() after Init = %q, want amqp:// 前缀", got)
	}
	if b.Options().Codec != nil {
		t.Fatal("未注册 json codec 时默认 Codec 应为 nil（透传 []byte/string）")
	}

	empty := NewBroker().(*rabbitBroker)
	if got := empty.Address(); got != "" {
		t.Fatalf("无地址时 Address() = %q, want \"\"", got)
	}
}

// TestUnconnectedGuards 未 Connect 时三个入口都必须报错，不能 panic：
// publish/Subscribe 直接看 b.conn，Disconnect 走 conn==nil 分支。
func TestUnconnectedGuards(t *testing.T) {
	b, ok := NewBroker(broker.WithAddress(refusedAddr(t))).(*rabbitBroker)
	if !ok {
		t.Fatal("NewBroker 返回了非 *rabbitBroker")
	}
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	ctx := context.Background()
	if err := b.Publish(ctx, "rk", &broker.Message{Body: "payload"}); err == nil {
		t.Fatal("未 Connect 的发布应报错")
	} else if !strings.Contains(err.Error(), "connection is nil") {
		t.Fatalf("发布错误 = %v, want 提示 connection is nil", err)
	}

	if _, err := b.Subscribe("rk", func(context.Context, broker.Event) error { return nil }, nil); err == nil {
		t.Fatal("未 Connect 的订阅应报错")
	} else if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("订阅错误 = %v, want 提示 not connected", err)
	}

	if err := b.Disconnect(); err == nil {
		t.Fatal("未 Connect 就 Disconnect 应报错而不是静默成功")
	}
}

// TestPublishMarshalFailureShortCircuits：codec 未注册时非透传类型编不出来，
// 必须在碰到连接之前就把错误还回去。
func TestPublishMarshalFailureShortCircuits(t *testing.T) {
	b, ok := NewBroker(broker.WithAddress(refusedAddr(t))).(*rabbitBroker)
	if !ok {
		t.Fatal("NewBroker 返回了非 *rabbitBroker")
	}
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	err := b.Publish(context.Background(), "rk", &broker.Message{Body: map[string]string{"v": "1"}})
	if err == nil {
		t.Fatal("无 codec 时结构体 body 应编码失败")
	}
	if !strings.Contains(err.Error(), "codec is nil") {
		t.Fatalf("编码错误 = %v, want 提示 codec 未注册", err)
	}
}

// TestConnectFailureReturnsError 指向无人监听的端口：Connect 报错、不留可用的
// 半连接状态，随后的 Disconnect 也只是收尾，不 panic。
func TestConnectFailureReturnsError(t *testing.T) {
	b, ok := NewBroker(broker.WithAddress(refusedAddr(t))).(*rabbitBroker)
	if !ok {
		t.Fatal("NewBroker 返回了非 *rabbitBroker")
	}
	if err := b.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if err := b.Connect(); err == nil {
		t.Fatal("指向死地址的 Connect 应失败")
	}

	// Connect 失败时 conn 已经建出来但底下没有 amqp.Connection，
	// Disconnect 必须报「connection is nil」而不是崩溃或挂住。
	if err := b.Disconnect(); err == nil {
		t.Fatal("连接失败后 Disconnect 应报出没有底层连接")
	}
}

// TestRabbitHeaderToMap 是消费侧唯一的头部转换入口：非字符串值会被丢成空串，
// 这点必须钉住，因为 amqp 的 headers 允许 int/bool/[]byte 等类型。
func TestRabbitHeaderToMap(t *testing.T) {
	got := rabbitHeaderToMap(amqp.Table{
		"str":   "v",
		"int":   7,
		"bool":  true,
		"bytes": []byte("x"),
		"nil":   nil,
	})

	want := map[string]string{"str": "v", "int": "", "bool": "", "bytes": "", "nil": ""}
	if len(got) != len(want) {
		t.Fatalf("rabbitHeaderToMap 键数 = %d, want %d: %v", len(got), len(want), got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("headers[%q] = %q, want %q", k, got[k], w)
		}
	}

	if got := rabbitHeaderToMap(nil); len(got) != 0 {
		t.Fatalf("nil headers 应得空表，得到 %v", got)
	}
}
