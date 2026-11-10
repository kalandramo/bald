// 本文件盖 Message/Options 的纯逻辑面：链式 setter、元数据上下文桥、
// Clone 深浅拷贝、Options 组装（批6 覆盖率战役收尾）。
package broker

import (
	"context"
	"testing"

)

func TestMessageChainedSetters(t *testing.T) {
	ackable := &fakeAckableMsg{acked: false}
	m := NewMessage("body",
		WithID("id-1"),
		WithKey("k-1"),
		WithMsg(ackable),
		WithPartitionOffset(3, 7),
		WithHeaders(map[string]string{"h0": "v0"}),
		WithHeader("h1", "v1"),
		WithMetadata(Metadata{"m0": 1}),
		WithMetadataKV("m1", 2),
	).
		WithKey("k-2").
		WithBody([]byte("chain-body")).
		WithMetadata("m2", 3)
	m.SetDelay("5s")

	if m.ID != "id-1" || m.Key != "k-2" {
		t.Fatalf("id/key = %s/%s", m.ID, m.Key)
	}
	if m.Partition != 3 || m.Offset != 7 {
		t.Fatalf("partition/offset = %d/%d", m.Partition, m.Offset)
	}
	if m.Headers["h0"] != "v0" || m.Headers["h1"] != "v1" {
		t.Fatalf("headers = %v", m.Headers)
	}
	if m.Metadata["m0"] != 1 || m.Metadata["m1"] != 2 || m.Metadata["m2"] != 3 {
		t.Fatalf("metadata = %v", m.Metadata)
	}
	if m.Metadata["x-delay-level"] != "5s" {
		t.Fatalf("delay level = %v", m.Metadata["x-delay-level"])
	}
	if got := m.BodyBytes(); string(got) != "chain-body" {
		t.Fatalf("BodyBytes = %q", got)
	}
	// 非 []byte 载荷 BodyBytes 返回 nil（不 panic）。
	if NewMessage(struct{}{}).BodyBytes() != nil {
		t.Fatal("BodyBytes non-nil for non-byte body")
	}
	// AckSuccess：底层 Msg 支持 Ack 才真正确认。
	if err := m.AckSuccess(); err != nil || !ackable.acked {
		t.Fatalf("ack = %v acked=%v", err, ackable.acked)
	}
	if err := NewMessage("x").AckSuccess(); err != nil {
		t.Fatalf("ack on bare message: %v", err)
	}
}

// fakeAckableMsg 实现匿名接口 interface{ Ack() error }（AckSuccess 的探测面）。
type fakeAckableMsg struct{ acked bool }

func (f *fakeAckableMsg) Ack() error { f.acked = true; return nil }

func TestMessageMetadataContextBridge(t *testing.T) {
	m := NewMessage("b")
	if m.GetMetadata("absent") != nil {
		t.Fatal("GetMetadata on absent key non-nil")
	}
	NewMessage("b").GetMetadata("absent") // nil metadata 不 panic

	// ExtractContext：metadata 挂进 ctx；nil metadata 透传 baseCtx。
	withMD := NewMessage("b").WithMetadata("k", "v")
	ctx := withMD.ExtractContext(context.Background())
	if got := GetMetadataFromContext(ctx); got["k"] != "v" {
		t.Fatalf("metadata from ctx = %v", got)
	}
	if GetMetadataFromContext((&Message{}).ExtractContext(context.Background())) != nil {
		t.Fatal("nil-metadata message leaked metadata into ctx")
	}

	// InjectContext：ctx 值按 key 写进 header（struct key 与自定义 string 类型
	// key 两种形态；SA1029 禁用内置 string 直接做 key）。
	type ctxKey struct{}
	type plainKey string
	ctx = context.WithValue(context.Background(), ctxKey{}, "sv")
	ctx = context.WithValue(ctx, plainKey("plain"), 9)
	got := NewMessageWithContext(ctx, "b", ctxKey{}, plainKey("plain"))
	if got.Headers["broker.ctxKey"] != "sv" {
		t.Fatalf("struct key header = %q, want broker.ctxKey", got.Headers["broker.ctxKey"])
	}
	if got.Headers["plain"] != "9" {
		t.Fatalf("string key header = %q, want 9", got.Headers["plain"])
	}
	// InjectContext 对 ctx 里不存在的 key 不写 header。
	absent := NewMessage("b").InjectContext(context.Background(), ctxKey{})
	if _, hit := absent.Headers["broker.ctxKey"]; hit {
		t.Fatal("absent ctx key wrote header")
	}

	// ExtractToContext：header 按映射写进 ctx；缺失 header 跳过。
	out := got.ExtractToContext(context.Background(), map[string]any{"plain": "destKey"})
	if out.Value("destKey") != "9" {
		t.Fatalf("extracted ctx value = %v", out.Value("destKey"))
	}
}

func TestMessageCloneIsolation(t *testing.T) {
	src := NewMessage("b", WithID("i"), WithKey("k"),
		WithHeader("h", "v"), WithMetadataKV("m", 1))
	cp := src.Clone()
	if cp.ID != "i" || cp.Key != "k" || cp.Headers["h"] != "v" || cp.Metadata["m"] != 1 {
		t.Fatalf("clone mismatch: %+v", cp)
	}
	// 浅拷贝隔离：改 clone 的 map 不影响原消息。
	cp.Headers["h"] = "changed"
	cp.Metadata["m"] = 2
	if src.Headers["h"] != "v" || src.Metadata["m"] != 1 {
		t.Fatal("clone shares header/metadata maps with source")
	}
	// nil maps 的 Clone 不 panic 且保持 nil 语义。
	n := &Message{}
	if nc := n.Clone(); nc.HeadersCopy() != nil || nc.MetadataCopy() != nil {
		t.Fatal("nil-map clone produced non-nil copies")
	}
}

func TestOptionsAssembly(t *testing.T) {
	// 默认：json codec、空地址表、Background ctx。
	def := NewOptions()
	if len(def.Addrs) != 0 {
		t.Fatalf("default addrs = %v", def.Addrs)
	}
	// codec 惰性查表：测试二进制未注册 codec 时为 nil，由 Marshal/Unmarshal
	// fail-fast（显式注册是业务的main期动作，不是包 init）。
	_ = def.Codec
	if def.Context == nil || def.Secure {
		t.Fatalf("default context/secure = %v/%v", def.Context == nil, def.Secure)
	}

	// Apply 对 nil 接收者是 no-op。
	var nilOpts *Options
	nilOpts.Apply(WithAddress("a:1"))

	// NewOptionsAndApply：全部选项落位。
	type optCtxKey struct{}
	opts := NewOptionsAndApply(
		WithAddress("a:1", "a:2"),
		WithCodec("json"),
		WithEnableSecure(true),
		WithTLSConfig(nil),
		WithOptionContext(context.WithValue(context.Background(), optCtxKey{}, "v")),
		WithSubscriberMiddlewares(func(Handler) Handler { return nil }),
		WithPublishMiddlewares(func(PublishHandler) PublishHandler { return nil }),
	)
	if len(opts.Addrs) != 2 || !opts.Secure || opts.Context.Value(optCtxKey{}) != "v" {
		t.Fatalf("options mismatch: addrs=%v secure=%v ctx=%v", opts.Addrs, opts.Secure, opts.Context)
	}
	if len(opts.SubscriberMiddlewares) != 1 || len(opts.PublishMiddlewares) != 1 {
		t.Fatalf("middlewares = %d/%d", len(opts.SubscriberMiddlewares), len(opts.PublishMiddlewares))
	}
	// codec 惰性查表口径：NewOptions 只查表不注册——测试二进制里其它用例可能
	// 已注册假 codec，这里不断言 Codec 的具体值（nil 或已注册实现都合法），
	// fail-fast 责任在 Marshal/Unmarshal。
	_ = def.Codec
}

func TestDerefAnyPointer(t *testing.T) {
	// 命中形态：*any 且 any 装着指针值 → 返回该指针（反序列化保类型）。
	x := 7
	var boxed any = &x
	if got := derefAnyPointer(&boxed); got != &x {
		t.Fatalf("deref boxed ptr = %v", got)
	}
	// any 装非指针：nil（保类型无从谈起）。
	var plain any = 7
	if got := derefAnyPointer(&plain); got != nil {
		t.Fatalf("boxed non-ptr = %v", got)
	}
	// 非 *any 入参 / nil：nil。
	if got := derefAnyPointer(boxed); got != nil {
		t.Fatalf("unboxed input = %v", got)
	}
	if got := derefAnyPointer(nil); got != nil {
		t.Fatalf("nil input = %v", got)
	}
	// *any 但 any 本身为 nil：nil。
	var empty *any
	if got := derefAnyPointer(empty); got != nil {
		t.Fatalf("nil any ptr = %v", got)
	}
	var nilAny any
	if got := derefAnyPointer(&nilAny); got != nil {
		t.Fatalf("boxed nil any = %v", got)
	}
}
