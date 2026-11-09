package tcp

// tcp_gaps_test.go —— 行级缺口补齐：客户端的收发/错误路径、服务端的启动/
// 分发/观测路径、Session 的 pump 生命周期与 SessionManager 的边界输入。
// 手工联调测试（TestClient/TestServer）由 -short 跳过，与本文件无关。

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kalandramo/bald/encoding"
	jsoncodec "github.com/kalandramo/bald/encoding/json"

	"go.opentelemetry.io/otel/trace/noop"
)

func ensureJSON(t *testing.T) {
	t.Helper()
	if encoding.GetCodec("json") == nil {
		encoding.MustRegister(jsoncodec.New())
	}
}

type gapMsg struct {
	Message string `json:"message"`
}

// memMetrics 记录调用名称，断言「指标是否被同步 emit」。
type memMetrics struct {
	mu         sync.Mutex
	counters   []string
	histograms []string
	gauges     []string
	gaugeAdds  []float64
}

func (m *memMetrics) Counter(_ context.Context, name string, _ float64, _ map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters = append(m.counters, name)
}

func (m *memMetrics) Histogram(_ context.Context, name string, _ float64, _ map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.histograms = append(m.histograms, name)
}

func (m *memMetrics) Gauge(_ context.Context, name string, _ float64, _ map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gauges = append(m.gauges, name)
}

func (m *memMetrics) GaugeAdd(_ context.Context, _ string, delta float64, _ map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gaugeAdds = append(m.gaugeAdds, delta)
}

func (m *memMetrics) snapshot() (counters []string, gaugeAdds []float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.counters...), append([]float64(nil), m.gaugeAdds...)
}

// scriptedConn 按脚本返回读写错误；reads 逐条消费后回落到 readErr。
type scriptedConn struct {
	closed   chan struct{}
	once     sync.Once
	reads    [][]byte
	readErr  error
	writeErr error
	closeErr error
}

func newScriptedConn() *scriptedConn { return &scriptedConn{closed: make(chan struct{})} }

func (c *scriptedConn) Read(p []byte) (int, error) {
	if len(c.reads) > 0 {
		n := copy(p, c.reads[0])
		c.reads = c.reads[1:]
		return n, nil
	}
	if c.readErr != nil {
		return 0, c.readErr
	}
	<-c.closed
	return 0, net.ErrClosed
}

func (c *scriptedConn) Write(b []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return len(b), nil
}

func (c *scriptedConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.closeErr
}

func (c *scriptedConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *scriptedConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *scriptedConn) SetDeadline(time.Time) error      { return nil }
func (c *scriptedConn) SetReadDeadline(time.Time) error  { return nil }
func (c *scriptedConn) SetWriteDeadline(time.Time) error { return nil }

// callbackConn 在读写时执行副作用（用于制造「done 已关闭」的并发取消分支）。
type callbackConn struct {
	*scriptedConn
	onRead  func()
	onWrite func()
}

func (c *callbackConn) Read(p []byte) (int, error) {
	if c.onRead != nil {
		c.onRead()
	}
	return 0, errors.New("read failed")
}

func (c *callbackConn) Write(b []byte) (int, error) {
	if c.onWrite != nil {
		c.onWrite()
	}
	return 0, errors.New("write failed")
}

// failingCodec 让 Marshal/Unmarshal 可控失败。
type failingCodec struct{}

func (failingCodec) Marshal(any) ([]byte, error) { return nil, errors.New("marshal failed") }
func (failingCodec) Unmarshal([]byte, any) error { return errors.New("unmarshal failed") }
func (failingCodec) Name() string                { return "failing" }

// sessionRecorder 记录 SessionHooks 调用。
type sessionRecorder struct {
	mu      sync.Mutex
	removed int
	raw     []byte
	rawErr  error
}

func (r *sessionRecorder) removeSession(*Session) {
	r.mu.Lock()
	r.removed++
	r.mu.Unlock()
}

func (r *sessionRecorder) handleSocketRawData(_ SessionID, b []byte) error {
	r.mu.Lock()
	r.raw = append([]byte(nil), b...)
	r.mu.Unlock()
	return r.rawErr
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNetPacket_OrderAndDecodeErrors(t *testing.T) {
	// 显式 Order 生效：大端类型头。
	p := &NetPacket{Type: 7, Payload: []byte("x"), Order: binary.BigEndian}
	buf, err := p.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := binary.BigEndian.Uint32(buf[:4]); got != 7 {
		t.Fatalf("big-endian header = %d, want 7", got)
	}

	// 未显式指定时回落进程级默认（小端）。
	p2 := &NetPacket{Type: 1}
	buf2, err := p2.Marshal()
	if err != nil {
		t.Fatalf("marshal default order: %v", err)
	}
	if got := binary.LittleEndian.Uint32(buf2[:4]); got != 1 {
		t.Fatalf("default header = %d, want 1 (little-endian)", got)
	}

	// 负载不足 4 字节 → binary.Read 失败。
	if err := (&NetPacket{}).Unmarshal([]byte{1, 2}); err == nil {
		t.Fatal("short buffer should fail to decode type header")
	}

	// 正常解码（须同上使用大端类型头）。
	var back NetPacket
	back.Order = binary.BigEndian
	if err := back.Unmarshal(buf); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Type != 7 || string(back.Payload) != "x" {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
}

func TestByteOrder_GlobalAndPerInstanceSetters(t *testing.T) {
	prev := defaultByteOrder
	defer func() { defaultByteOrder = prev }()

	WithBigEndian()
	if defaultByteOrder != binary.BigEndian {
		t.Fatal("WithBigEndian did not change process default")
	}
	WithLittleEndian()
	if defaultByteOrder != binary.LittleEndian {
		t.Fatal("WithLittleEndian did not restore little-endian")
	}

	srv := NewServer(WithByteOrder(nil))
	if srv.byteOrder != nil {
		t.Fatal("nil option must leave per-instance order unset")
	}
	srv = NewServer(WithByteOrder(binary.BigEndian))
	if srv.byteOrder != binary.BigEndian {
		t.Fatal("WithByteOrder did not apply")
	}
	cli := NewClient(WithClientByteOrder(nil))
	if cli.byteOrder != nil {
		t.Fatal("nil client option must leave order unset")
	}
	cli = NewClient(WithClientByteOrder(binary.BigEndian))
	if cli.byteOrder != binary.BigEndian {
		t.Fatal("WithClientByteOrder did not apply")
	}
}

func TestMessageHandlerData_Create(t *testing.T) {
	if got := (&MessageHandlerData{}).Create(); got != nil {
		t.Fatalf("nil creator must return nil, got %v", got)
	}
	got := (&MessageHandlerData{Creator: func() any { return &gapMsg{} }}).Create()
	if _, ok := got.(*gapMsg); !ok {
		t.Fatalf("creator result = %T, want *gapMsg", got)
	}
}

func TestClientOptions_AllFields(t *testing.T) {
	raw := func([]byte) error { return nil }
	tr := noop.NewTracerProvider().Tracer("t")
	mm := &memMetrics{}
	c := NewClient(
		WithClientRawDataHandler(raw),
		WithClientTracer(tr),
		WithClientTracerProvider(),
		WithClientMetrics(mm),
		WithClientCodec("json"),
		WithEndpoint("127.0.0.1:1"),
	)
	if c.rawMessageHandler == nil || c.tracer == nil || c.m != mm || c.url != "127.0.0.1:1" {
		t.Fatalf("client options not applied: %+v", c)
	}
}

func TestClient_ConnectErrorsAndDisconnectedSend(t *testing.T) {
	// 无法解析的 endpoint → endpoint 为 nil。
	if err := NewClient(WithEndpoint("://bad")).Connect(); err == nil || err.Error() != "endpoint is nil" {
		t.Fatalf("want endpoint is nil error, got %v", err)
	}

	// 关闭的监听端口 → dial 失败（localhost 前缀保证 url 解析出非 nil endpoint）。
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()

	cli := NewClient(WithEndpoint("localhost:" + strconv.Itoa(port)))
	if err := cli.Connect(); err == nil {
		t.Fatal("connect to closed port should fail")
	}
	cli.Disconnect() // 未连接时 Disconnect 为空操作
	if err := cli.SendRawData([]byte("x")); err == nil {
		t.Fatal("send on disconnected client should fail")
	}
}

func TestClient_ReleaseAndCloseError(t *testing.T) {
	c := &Client{}
	connA := newScriptedConn()
	if !c.release(nil) {
		t.Fatal("release(nil) on zero-value client should succeed")
	}
	c.conn = connA
	if c.release(newScriptedConn()) {
		t.Fatal("release of a foreign conn must not clear current conn")
	}
	if !c.release(connA) {
		t.Fatal("release of current conn should succeed")
	}

	// Close 报错仅记日志，不外抛。
	c.conn = &scriptedConn{closed: make(chan struct{}), closeErr: errors.New("close failed")}
	c.Disconnect()
	if c.conn != nil {
		t.Fatal("Disconnect must clear conn")
	}
}

func TestClient_HandlerRegistryAndSends(t *testing.T) {
	ensureJSON(t)
	c := NewClient()
	called := 0
	c.RegisterMessageHandler(1, func(NetMessagePayload) error { called = 1; return nil }, nil)
	c.RegisterMessageHandler(1, func(NetMessagePayload) error { called = 2; return nil }, nil)
	if err := c.messageHandlers[1].Handler(nil); err != nil || called != 1 {
		t.Fatalf("duplicate registration must be ignored: called=%d err=%v", called, err)
	}
	c.DeregisterMessageHandler(1)
	if _, ok := c.messageHandlers[1]; ok {
		t.Fatal("deregister did not remove handler")
	}

	// 泛型注册器的类型断言失败分支。
	RegisterClientMessageHandler(c, 2, func(*gapMsg) error { return nil })
	if err := c.messageHandlers[2].Handler("wrong type"); err == nil {
		t.Fatal("wrong payload type should error")
	}

	// SendRawData 成功 + 指标；写失败 + 错误指标。
	mm := &memMetrics{}
	c.m = mm
	c.conn = newScriptedConn()
	if err := c.SendRawData([]byte("hi")); err != nil {
		t.Fatalf("send raw: %v", err)
	}
	c.conn = &scriptedConn{closed: make(chan struct{}), writeErr: errors.New("write failed")}
	if err := c.SendRawData([]byte("hi")); err == nil {
		t.Fatal("send on failing conn should error")
	}
	counters, _ := mm.snapshot()
	if len(counters) != 3 {
		t.Fatalf("metric counters = %v, want sent+error+send-duration", counters)
	}

	// SendMessage：codec 缺失 / Marshal 失败 / 成功。
	c.conn = newScriptedConn()
	c.codec = nil
	if err := c.SendMessage(1, &gapMsg{}); err == nil {
		t.Fatal("nil codec should error")
	}
	c.codec = failingCodec{}
	if err := c.SendMessage(1, &gapMsg{}); err == nil {
		t.Fatal("marshal failure should propagate")
	}
	c.codec = encoding.GetCodec("json")
	if err := c.SendMessage(1, &gapMsg{Message: "m"}); err != nil {
		t.Fatalf("send message: %v", err)
	}
}

func TestClient_MessageHandlerDispatch(t *testing.T) {
	ensureJSON(t)
	c := NewClient()

	// 报文解码失败。
	if err := c.messageHandler([]byte{1}); err == nil {
		t.Fatal("short buffer should fail")
	}

	// 类型未注册。
	pkt, _ := (&NetPacket{Type: 9}).Marshal()
	if err := c.messageHandler(pkt); err == nil {
		t.Fatal("unknown type should error")
	}

	// creator + 非法载荷 → 反序列化失败。
	c.RegisterMessageHandler(1, func(NetMessagePayload) error { return nil }, func() any { return &gapMsg{} })
	bad, _ := (&NetPacket{Type: 1, Payload: []byte("{")}).Marshal()
	if err := c.messageHandler(bad); err == nil {
		t.Fatal("invalid json should fail unmarshal")
	}

	// creator + 合法载荷 → handler 收到 *gapMsg。
	var got any
	c.RegisterMessageHandler(2, func(p NetMessagePayload) error { got = p; return nil }, func() any { return &gapMsg{} })
	var p2 NetPacket
	_ = p2.Unmarshal(pkt)
	p2.Type = 2
	p2.Payload = []byte(`{"message":"hi"}`)
	good, _ := p2.Marshal()
	if err := c.messageHandler(good); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if m, ok := got.(*gapMsg); !ok || m.Message != "hi" {
		t.Fatalf("handler payload = %#v, want *gapMsg{hi}", got)
	}

	// 无 creator → handler 收原始字节。
	var raw NetMessagePayload
	c.RegisterMessageHandler(3, func(p NetMessagePayload) error { raw = p; return nil }, nil)
	p3 := &NetPacket{Type: 3, Payload: []byte("raw")}
	b3, _ := p3.Marshal()
	if err := c.messageHandler(b3); err != nil {
		t.Fatalf("dispatch raw: %v", err)
	}
	if string(raw.([]byte)) != "raw" {
		t.Fatalf("raw payload = %q, want raw", raw)
	}

	// handler 自身报错向上传。
	c.RegisterMessageHandler(4, func(NetMessagePayload) error { return errors.New("handler failed") }, nil)
	b4, _ := (&NetPacket{Type: 4}).Marshal()
	if err := c.messageHandler(b4); err == nil {
		t.Fatal("handler error should propagate")
	}

	// creator 存在但 codec 为空。
	c.codec = nil
	if err := c.messageHandler(good); err == nil {
		t.Fatal("nil codec with creator should error")
	}
}

func TestClient_RunRawAndMessagePaths(t *testing.T) {
	ensureJSON(t)

	// raw 路径：handler 报错后继续读，连接关闭后退出并释放。
	mm := &memMetrics{}
	var rawCalls atomic.Int32
	raw := NewClient(WithClientRawDataHandler(func([]byte) error {
		rawCalls.Add(1)
		return errors.New("raw failed")
	}), WithClientMetrics(mm))
	serverEnd, clientEnd := net.Pipe()
	raw.conn = clientEnd
	go raw.run(clientEnd)
	if _, err := serverEnd.Write([]byte("x")); err != nil {
		t.Fatalf("pipe write: %v", err)
	}
	waitUntil(t, "raw handler call", func() bool { return rawCalls.Load() > 0 })
	_ = serverEnd.Close()
	waitUntil(t, "client run exit", func() bool {
		raw.connMu.Lock()
		defer raw.connMu.Unlock()
		return raw.conn == nil
	})

	// raw 路径成功：handler 返回 nil 后继续读，连接关闭后退出。
	rawOK := NewClient(WithClientRawDataHandler(func([]byte) error { return nil }))
	serverEnd3, clientEnd3 := net.Pipe()
	rawOK.conn = clientEnd3
	go rawOK.run(clientEnd3)
	if _, err := serverEnd3.Write([]byte("y")); err != nil {
		t.Fatalf("pipe write: %v", err)
	}
	_ = serverEnd3.Close()
	waitUntil(t, "raw-ok client run exit", func() bool {
		rawOK.connMu.Lock()
		defer rawOK.connMu.Unlock()
		return rawOK.conn == nil
	})

	// 消息路径：正常分发 + received 指标。
	got := make(chan NetMessagePayload, 1)
	msgc := NewClient(WithClientMetrics(mm))
	msgc.RegisterMessageHandler(1, func(p NetMessagePayload) error {
		got <- p
		return nil
	}, func() any { return &gapMsg{} })
	serverEnd2, clientEnd2 := net.Pipe()
	msgc.conn = clientEnd2
	go msgc.run(clientEnd2)
	// 先发未知类型：分发报错后必须继续读，不得退出。
	unknown, _ := (&NetPacket{Type: 99}).Marshal()
	if _, err := serverEnd2.Write(unknown); err != nil {
		t.Fatalf("pipe write unknown: %v", err)
	}
	pkt, _ := (&NetPacket{Type: 1, Payload: []byte(`{"message":"hi"}`)}).Marshal()
	if _, err := serverEnd2.Write(pkt); err != nil {
		t.Fatalf("pipe write: %v", err)
	}
	select {
	case p := <-got:
		if p.(*gapMsg).Message != "hi" {
			t.Fatalf("payload = %#v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("message handler not invoked")
	}
	_ = serverEnd2.Close()
	waitUntil(t, "message client run exit", func() bool {
		msgc.connMu.Lock()
		defer msgc.connMu.Unlock()
		return msgc.conn == nil
	})
}

func TestClient_RunClosesConnWithError(t *testing.T) {
	// release 为 true 且 Close 失败时仅记日志。
	c := NewClient()
	conn := &scriptedConn{closed: make(chan struct{}), readErr: errors.New("read failed"), closeErr: errors.New("close failed")}
	c.conn = conn
	c.run(conn)
	if c.conn != nil {
		t.Fatal("run must clear conn on exit")
	}
}

func TestServerOptions_AllFields(t *testing.T) {
	prevBuf, prevRecv := channelBufSize, recvBufferSize
	defer func() { channelBufSize, recvBufferSize = prevBuf, prevRecv }()

	connected := 0
	raw := func(SessionID, []byte) error { return nil }
	tr := noop.NewTracerProvider().Tracer("t")
	marshal := func(NetMessageType, NetMessagePayload) ([]byte, error) { return []byte("m"), nil }
	unmarshal := func([]byte) (*MessageHandlerData, NetMessagePayload, error) { return nil, nil, nil }
	srv := NewServer(
		WithTimeout(time.Minute),
		WithCodec("json"),
		WithChannelBufferSize(64),
		WithReceiveBufferSize(128),
		WithMessageMarshaler(marshal),
		WithMessageUnmarshaler(unmarshal),
		WithSocketConnectHandler(func(SessionID, bool) { connected++ }),
		WithSocketRawDataHandler(raw),
		WithSocketRawDataHandler(nil), // nil 不覆盖既有 handler
		WithTracer(tr),
		WithTracerProvider(),
		WithMetrics(&memMetrics{}),
	)
	if srv.timeout != time.Minute || channelBufSize != 64 || recvBufferSize != 128 {
		t.Fatalf("timeout/buffers not applied: %v/%d/%d", srv.timeout, channelBufSize, recvBufferSize)
	}
	if srv.socketRawDataHandler == nil || srv.tracer == nil || srv.m == nil {
		t.Fatal("handler/tracer/metrics options not applied")
	}
	buf, err := srv.marshalNetPacket(1, nil)
	if err != nil || string(buf) != "m" {
		t.Fatalf("custom marshaler = %q/%v", buf, err)
	}
	if h, p, err := srv.unmarshalNetPacket(nil); h != nil || p != nil || err != nil {
		t.Fatalf("custom unmarshaler = %v/%v/%v", h, p, err)
	}
	srv.tracer = nil // WithTracerProvider 覆盖了注入值，分支本身已覆盖
}

func TestServer_StartListenErrorAndTLS(t *testing.T) {
	if err := NewServer(WithAddress("bad address")).Start(context.Background()); err == nil {
		t.Fatal("invalid address should fail to listen")
	}

	// TLS 包装分支：只验证监听可用与关闭流程。
	srv := NewServer(WithAddress("127.0.0.1:0"), WithTLSConfig(&tls.Config{}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	_ = waitEndpoint(t, srv)
	if err := srv.Start(ctx); err != nil { // 已运行：直接返回
		t.Fatalf("second Start should return nil, got %v", err)
	}
	cancel() // 走 Start 自身的 listener 关闭路径
	if err := <-done; err != nil {
		t.Fatalf("Start returned %v", err)
	}
	if got := srv.SessionCount(); got != 0 {
		t.Fatalf("sessions after cancel = %d, want 0", got)
	}
}

func TestServer_EndpointFallbacks(t *testing.T) {
	if got := NewServer(WithAddress("bad:addr:extra")).Endpoint(); got != "tcp://bad:addr:extra" {
		t.Fatalf("unparsable address endpoint = %q", got)
	}
	if got := NewServer(WithAddress(":1234")).Endpoint(); got != "tcp://localhost:1234" {
		t.Fatalf("empty host endpoint = %q", got)
	}
	if got := NewServer(WithAddress("0.0.0.0:1234")).Endpoint(); got != "tcp://localhost:1234" {
		t.Fatalf("wildcard host endpoint = %q", got)
	}
}

func TestServer_HandlerRegistryAndSendErrors(t *testing.T) {
	srv := NewServer()
	called := 0
	srv.RegisterMessageHandler(1, func(SessionID, NetMessagePayload) error { called = 1; return nil }, nil)
	srv.RegisterMessageHandler(1, func(SessionID, NetMessagePayload) error { called = 2; return nil }, nil)
	h, err := srv.GetMessageHandler(1)
	if err != nil || called != 0 {
		t.Fatalf("get handler: %v", err)
	}
	if err := h.Handler("", nil); err != nil || called != 1 {
		t.Fatalf("duplicate registration must keep first: called=%d err=%v", called, err)
	}
	srv.DeregisterMessageHandler(1)
	if _, err := srv.GetMessageHandler(1); err == nil {
		t.Fatal("deregistered handler should be missing")
	}

	// 泛型注册器：类型不匹配报错，匹配则调用。
	var typed *gapMsg
	RegisterServerMessageHandler(srv, 2, func(_ SessionID, m *gapMsg) error { typed = m; return nil })
	h2, _ := srv.GetMessageHandler(2)
	if err := h2.Handler("", "wrong"); err == nil {
		t.Fatal("wrong payload type should error")
	}
	if err := h2.Handler("", &gapMsg{Message: "ok"}); err != nil || typed.Message != "ok" {
		t.Fatalf("typed handler: typed=%v err=%v", typed, err)
	}

	if err := srv.SendRawData("missing", []byte("x")); err == nil {
		t.Fatal("send to missing session should error")
	}

	// 自定义（失败）marshaler 影响 SendMessage 与 Broadcast。
	boomMarshal := func(NetMessageType, NetMessagePayload) ([]byte, error) {
		return nil, errors.New("marshal failed")
	}
	bs := NewServer(WithMessageMarshaler(boomMarshal))
	if err := bs.SendMessage("x", 1, nil); err == nil {
		t.Fatal("SendMessage should surface marshal error")
	}
	bs.Broadcast(1, nil) // 仅记日志
}

func TestServer_MarshalUnmarshalEdges(t *testing.T) {
	srv := NewServer()

	// codec 为 nil。
	srv.codec = nil
	if _, err := srv.defaultMarshalNetPacket(1, nil); err == nil {
		t.Fatal("nil codec marshal should error")
	}

	// Marshal 失败。
	srv.codec = failingCodec{}
	if _, err := srv.defaultMarshalNetPacket(1, nil); err == nil {
		t.Fatal("marshal failure should propagate")
	}

	// 报文解码失败。
	ensureJSON(t)
	srv.codec = encoding.GetCodec("json")
	if _, _, err := srv.defaultUnmarshalNetPacket([]byte{1}); err == nil {
		t.Fatal("short packet should fail")
	}

	// 未注册类型。
	unknown, _ := (&NetPacket{Type: 9}).Marshal()
	if _, _, err := srv.defaultUnmarshalNetPacket(unknown); err == nil {
		t.Fatal("unknown type should fail")
	}

	// creator + 非法载荷。
	srv.RegisterMessageHandler(1, func(SessionID, NetMessagePayload) error { return nil }, func() any { return &gapMsg{} })
	bad, _ := (&NetPacket{Type: 1, Payload: []byte("{")}).Marshal()
	if _, _, err := srv.defaultUnmarshalNetPacket(bad); err == nil {
		t.Fatal("invalid json should fail")
	}

	// creator 但 codec 为空。
	srv.codec = nil
	good, _ := (&NetPacket{Type: 1, Payload: []byte(`{}`)}).Marshal()
	if _, _, err := srv.defaultUnmarshalNetPacket(good); err == nil {
		t.Fatal("nil codec with creator should fail")
	}

	// 无 creator：payload 原样返回。
	srv.codec = encoding.GetCodec("json")
	srv.RegisterMessageHandler(2, func(SessionID, NetMessagePayload) error { return nil }, nil)
	raw, _ := (&NetPacket{Type: 2, Payload: []byte("raw")}).Marshal()
	h, payload, err := srv.defaultUnmarshalNetPacket(raw)
	if err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if h == nil || string(payload.([]byte)) != "raw" {
		t.Fatalf("raw dispatch mismatch: handler=%v payload=%v", h, payload)
	}
}

func TestServer_HandleSocketRawDataPaths(t *testing.T) {
	ensureJSON(t)
	mm := &memMetrics{}
	srv := NewServer(
		WithTracer(noop.NewTracerProvider().Tracer("t")),
		WithMetrics(mm),
	)
	srv.RegisterMessageHandler(1, func(SessionID, NetMessagePayload) error { return nil }, func() any { return &gapMsg{} })
	srv.RegisterMessageHandler(2, func(SessionID, NetMessagePayload) error { return errors.New("handler failed") }, func() any { return &gapMsg{} })

	okPkt, _ := (&NetPacket{Type: 1, Payload: []byte(`{}`)}).Marshal()
	if err := srv.defaultHandleSocketRawData("s", okPkt); err != nil {
		t.Fatalf("success dispatch: %v", err)
	}
	failPkt, _ := (&NetPacket{Type: 2, Payload: []byte(`{}`)}).Marshal()
	if err := srv.defaultHandleSocketRawData("s", failPkt); err == nil {
		t.Fatal("handler failure should propagate")
	}
	if err := srv.defaultHandleSocketRawData("s", []byte{1}); err == nil {
		t.Fatal("malformed packet should propagate")
	}
	counters, histories := mm.counters, mm.histograms
	mm.mu.Lock()
	if len(counters) != 3 || len(histories) != 1 {
		t.Fatalf("metrics = %v / %v, want 3 counters (ok+2 errors) and 1 histogram", counters, histories)
	}
	mm.mu.Unlock()

	// handleSocketRawData：自定义 handler 为空时回落默认实现。
	srv.socketRawDataHandler = nil
	if err := srv.handleSocketRawData("s", okPkt); err != nil {
		t.Fatalf("fallback dispatch: %v", err)
	}
}

func TestServer_AcceptErrorBranchAndGauge(t *testing.T) {
	// 未运行 / 无 listener：doAccept 立即返回。
	NewServer().doAccept() // running=false
	fl0 := NewServer()
	fl0.running = true // lis 为 nil
	fl0.doAccept()

	// 自定义 listener：第一次 Accept 返回临时错误（触发 continue），
	// 第二次返回 ErrClosed（退出循环）。
	fl := &flakyListener{errs: []error{errors.New("temporary"), net.ErrClosed}}
	srv := NewServer(WithMetrics(&memMetrics{}))
	srv.lis = fl
	srv.running = true
	done := make(chan struct{})
	go func() { srv.doAccept(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("doAccept did not exit")
	}
	if fl.calls != 2 {
		t.Fatalf("Accept calls = %d, want 2", fl.calls)
	}

	// 真实接受路径上的连接 gauge。
	mm := &memMetrics{}
	live := NewServer(WithAddress("127.0.0.1:0"), WithMetrics(mm))
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- live.Start(ctx) }()
	addr := waitEndpoint(t, live)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	waitUntil(t, "session accepted", func() bool { return live.SessionCount() == 1 })
	_, adds := mm.snapshot()
	if len(adds) == 0 || adds[0] != 1 {
		t.Fatalf("accept should GaugeAdd(+1), got %v", adds)
	}
	_ = conn.Close()
	waitUntil(t, "session removed", func() bool { return live.SessionCount() == 0 })
	cancel()
	<-started
}

type flakyListener struct {
	errs  []error
	calls int
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.calls++
	if l.calls <= len(l.errs) {
		return nil, l.errs[l.calls-1]
	}
	return nil, net.ErrClosed
}
func (l *flakyListener) Close() error   { return nil }
func (l *flakyListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestServer_SessionCallbacksAndNilManager(t *testing.T) {
	var events []bool
	mm := &memMetrics{}
	srv := NewServer(
		WithSocketConnectHandler(func(_ SessionID, up bool) { events = append(events, up) }),
		WithMetrics(mm),
	)
	// nil 会话不触发连接回调。
	srv.OnSessionAdded(nil)
	session := NewSession(newFakeConn(), nil)
	srv.OnSessionAdded(session)
	srv.OnSessionRemoved(session)
	if len(events) != 2 || !events[0] || events[1] {
		t.Fatalf("connect events = %v, want [true false]", events)
	}
	_, adds := mm.snapshot()
	if len(adds) != 1 || adds[0] != -1 {
		t.Fatalf("remove should GaugeAdd(-1), got %v", adds)
	}

	// 零值 Server（无 sessionManager）下 removeSession 必须安全返回。
	(&Server{}).removeSession(session)
	// 管理器内不存在的会话删除安全。
	srv.removeSession(session)
}

func TestServer_SendAndBroadcastWithSession(t *testing.T) {
	ensureJSON(t)
	srv := NewServer()
	session := NewSession(newFakeConn(), nil)
	srv.sessionManager.addSession(session)
	if err := srv.SendRawData(session.SessionID(), []byte("x")); err != nil {
		t.Fatalf("send raw: %v", err)
	}
	if err := srv.SendMessage(session.SessionID(), 1, &gapMsg{Message: "m"}); err != nil {
		t.Fatalf("send message: %v", err)
	}
	srv.Broadcast(1, &gapMsg{Message: "b"})
	// 缓冲通道应收到 3 条消息（raw + message + broadcast）。
	if got := len(session.send); got != 3 {
		t.Fatalf("queued messages = %d, want 3", got)
	}
}

func TestSession_NilConnPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewSession(nil) must panic")
		}
	}()
	NewSession(nil, nil)
}

func TestSession_SendAfterCloseAndHooks(t *testing.T) {
	rec := &sessionRecorder{}
	s := NewSession(newFakeConn(), rec)
	if s.Conn() == nil || s.SessionID() == "" {
		t.Fatal("session accessors must return live values")
	}
	s.Close()
	s.Close() // 幂等
	// 先填满发送缓冲，保证 SendMessage 的 select 只能走 done 分支（确定性）。
	for i := 0; i < channelBufSize; i++ {
		s.send <- []byte("fill")
	}
	s.SendMessage([]byte("dropped"))
	if s.Conn() != nil {
		t.Fatal("Close must clear conn")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.removed != 1 {
		t.Fatalf("removeSession calls = %d, want 1", rec.removed)
	}
}

func TestSession_CloseConnectError(t *testing.T) {
	s := NewSession(&scriptedConn{closed: make(chan struct{}), closeErr: errors.New("close failed")}, nil)
	s.Close() // Close 报错仅记日志
	if s.conn != nil {
		t.Fatal("conn must be cleared even when Close errors")
	}
}

func TestSession_ListenAndWait(t *testing.T) {
	s := NewSession(newFakeConn(), nil)
	s.Listen()
	s.Listen() // listenOnce 幂等
	s.Close()
	waited := make(chan struct{})
	go func() { s.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after Close")
	}
}

func TestSession_WritePumpBranches(t *testing.T) {
	// conn 为 nil：丢弃消息并退出。
	s := &Session{conn: nil, done: make(chan struct{}), send: make(chan []byte, 1)}
	s.wg.Add(1)
	s.send <- []byte("x")
	s.writePump()
	s.wg.Wait()

	// 写失败且已关闭 → 走 done 分支（不记错误日志）。
	var inner *Session
	conn := &callbackConn{scriptedConn: newScriptedConn(), onWrite: func() { inner.Close() }}
	s2 := &Session{conn: conn, done: make(chan struct{}), send: make(chan []byte, 1)}
	inner = s2
	s2.wg.Add(1)
	s2.send <- []byte("x")
	s2.writePump()
	s2.wg.Wait()

	// 写失败但未关闭 → 记录日志后退出。
	s3 := &Session{conn: &scriptedConn{closed: make(chan struct{}), writeErr: errors.New("w")},
		done: make(chan struct{}), send: make(chan []byte, 1)}
	s3.wg.Add(1)
	s3.send <- []byte("x")
	s3.writePump()
	s3.wg.Wait()
}

func TestSession_ReadPumpBranches(t *testing.T) {
	// conn 为 nil：直接退出。
	s := &Session{conn: nil, done: make(chan struct{})}
	s.wg.Add(1)
	s.readPump()
	s.wg.Wait()

	// hooks 为 nil：读到的数据被丢弃后继续读，直到读错误退出。
	sc := newScriptedConn()
	sc.reads = [][]byte{[]byte("ignored")}
	sc.readErr = errors.New("eof")
	s2 := &Session{conn: sc, done: make(chan struct{})}
	s2.wg.Add(1)
	s2.readPump()
	s2.wg.Wait()

	// hooks 处理报错：记录后继续，读错误退出。
	rec := &sessionRecorder{rawErr: errors.New("bad data")}
	sc2 := newScriptedConn()
	sc2.reads = [][]byte{[]byte("payload")}
	sc2.readErr = errors.New("eof")
	s3 := &Session{conn: sc2, done: make(chan struct{}), hooks: rec}
	s3.wg.Add(1)
	s3.readPump()
	s3.wg.Wait()
	rec.mu.Lock()
	raw := string(rec.raw)
	rec.mu.Unlock()
	if raw != "payload" {
		t.Fatalf("raw data forwarded = %q, want payload", raw)
	}

	// 读失败且已关闭 → done 分支退出。
	var inner *Session
	cc := &callbackConn{scriptedConn: newScriptedConn(), onRead: func() { inner.Close() }}
	s4 := &Session{conn: cc, done: make(chan struct{})}
	inner = s4
	s4.wg.Add(1)
	s4.readPump()
	s4.wg.Wait()
}

func TestSessionManager_EdgeInputs(t *testing.T) {
	sm := NewSessionManager(nil)
	sm.addSession(nil)
	sm.removeSession(nil)
	sm.addSession(NewSession(newFakeConn(), nil))
	sm.Clean()
	if got := sm.count(); got != 0 {
		t.Fatalf("count after Clean = %d, want 0", got)
	}
	s := NewSession(newFakeConn(), nil)
	sm.removeSession(s) // 不存在：不通知、不 panic
	if sm.getSession("missing") != nil {
		t.Fatal("unknown session should be nil")
	}
	sm.addSession(s)
	if sm.getSession(s.SessionID()) == nil {
		t.Fatal("added session not retrievable")
	}
}

// ---------------------------------------------------------------------------
// 结构性零覆盖（登记备查）：
//   - message.go:31 —— binary.Write 到 bytes.Buffer 写 uint32 永不失败
//     （bytes.Buffer 的 Write 无错误路径），Marshal 的错误分支不可达。
//   - client.go:201 —— SendMessage 中 NetPacket.Marshal 的错误分支：
//     同上，类型头写入 bytes.Buffer 不可能失败，返回不可达。
// ---------------------------------------------------------------------------

// 结构性零覆盖（登记备查）：
//   message.go:30-32 —— NetPacket.Marshal 的 binary.Write 写的是 bytes.Buffer，
//   其 Write 永不返回错误；该分支是 io.Writer 契约的错误出口，不可达。
//   client.go:200-202 —— SendMessage 对 msg.Marshal 的错误封装，同一不可达出口
//   的下游，随上一条一并登记。
