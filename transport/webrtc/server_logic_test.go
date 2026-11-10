// 本文件盖 server 纯逻辑面：ServerOption/ClientOption 全量应用、消息 handler
// 注册表（注册/去重/反注册/泛型适配）、marshalMessage 的编解码分派。对连
// （PeerConnection 握手）走 integration_test.go 的真 SFU 流，-short 下 Skip。
package webrtc

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/kalandramo/bald/metrics"
)

// noopMiddleware 是链序断言的观察哨：按应用序把名字记进 *[]string。
func noopMiddleware(name string, order *[]string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*order = append(*order, name)
			next.ServeHTTP(w, r)
		})
	}
}

// countingMetrics 是 metrics.Metrics 的最小桩：只记调用次数（WithClientMetrics
// 的注入断言用，不需要真后端）。
type countingMetrics struct{ counterCalls int }

func (m *countingMetrics) Counter(context.Context, string, float64, map[string]string) { m.counterCalls++ }
func (m *countingMetrics) Histogram(context.Context, string, float64, map[string]string) {}
func (m *countingMetrics) Gauge(context.Context, string, float64, map[string]string)     {}
func (m *countingMetrics) GaugeAdd(context.Context, string, float64, map[string]string)  {}

var _ metrics.Metrics = (*countingMetrics)(nil)

func TestServerOptionsApply(t *testing.T) {
	var order []string
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	s := NewServer(
		WithNetwork("tcp4"),
		WithAddress("127.0.0.1:9999"),
		WithStrictSlash(false),
		WithPath("/ws/signal"),
		WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}),
		WithListener(lis),
		WithCodec("json"),
		WithReadBufferSize(1024),  // 兼容保留：不得 panic
		WithWriteBufferSize(1024), // 兼容保留：不得 panic
		WithEnableCORS(true),
		WithCORS("https://a.example.com", "GET, POST", "X-Token"),
		WithChannelBufferSize(64),
		WithPayloadType(PayloadTypeText),
		WithCheckOrigin("https://a.example.com"),
	)
	s.Use(noopMiddleware("outer", &order))

	if s.network != "tcp4" || s.address != "127.0.0.1:9999" {
		t.Fatalf("network/address = %s/%s", s.network, s.address)
	}
	if s.strictSlash || s.path != "/ws/signal" || s.lis == nil || s.tlsConf == nil {
		t.Fatalf("strictSlash=%v path=%s lis/tls nil=%v/%v", s.strictSlash, s.path, s.lis == nil, s.tlsConf == nil)
	}
	if s.codec == nil || !s.enableCORS || s.payloadType != PayloadTypeText {
		t.Fatalf("codec nil=%v cors=%v payloadType=%d", s.codec == nil, s.enableCORS, s.payloadType)
	}
	if s.corsAllowOrigin != "https://a.example.com" || s.corsAllowMethods != "GET, POST" || s.corsAllowHeaders != "X-Token" {
		t.Fatalf("cors headers = %s/%s/%s", s.corsAllowOrigin, s.corsAllowMethods, s.corsAllowHeaders)
	}

	// WithCheckOrigin：Origin 恰配才放行。
	if !s.checkOrigin(&http.Request{Header: http.Header{"Origin": {"https://a.example.com"}}}) {
		t.Fatal("checkOrigin rejected matching origin")
	}
	if s.checkOrigin(&http.Request{Header: http.Header{"Origin": {"https://b.example.com"}}}) {
		t.Fatal("checkOrigin accepted foreign origin")
	}

	// 空值选项不得覆盖 NewServer 的默认（与选项实现的「非空才写」契约一致）。
	NewServer(WithNetwork(""), WithAddress(""), WithPath(""), WithCodec(""), WithCORS("", "", ""))
}

func TestServerCheckOriginFunc(t *testing.T) {
	called := false
	fn := func(*http.Request) bool { called = true; return true }
	s := NewServer(WithCheckOriginFunc(fn))
	if s.checkOrigin == nil {
		t.Fatal("checkOrigin nil after WithCheckOriginFunc")
	}
	_ = s.checkOrigin(&http.Request{})
	if !called {
		t.Fatal("injected origin func not invoked")
	}
	// nil fn 不得清掉默认放行。
	s2 := NewServer(WithCheckOriginFunc(nil))
	if !s2.checkOrigin(&http.Request{}) {
		t.Fatal("nil func replaced default allow-all origin check")
	}
}

func TestServerNameAndMiddlewareChain(t *testing.T) {
	var order []string
	s := NewServer()
	if got := s.Name(); got != KindWebRTC {
		t.Fatalf("Name = %q, want %q", got, KindWebRTC)
	}
	// buildHandler：先 Use 的在最外层（注册序 = 包装序，内层后执行）。
	s.Use(noopMiddleware("first", &order), noopMiddleware("second", &order))
	s.buildHandler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/signal", nil))
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("middleware order = %v, want [first second]", order)
	}
}

func TestServerMessageHandlerRegistry(t *testing.T) {
	s := NewServer()
	const typ NetMessageType = 42

	gotSession := SessionID("")
	s.RegisterMessageHandler(typ, func(id SessionID, _ MessagePayload) error {
		gotSession = id
		return nil
	}, func() any { return &map[string]any{} })

	h := s.GetMessageHandler(typ)
	if h == nil {
		t.Fatal("handler missing after register")
	}
	if err := h.Handler("sess-1", &map[string]any{}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if gotSession != "sess-1" {
		t.Fatalf("session = %q, want sess-1", gotSession)
	}

	// 重复注册被忽略（首注册胜——注册表去重语义）。
	s.RegisterMessageHandler(typ, func(SessionID, MessagePayload) error {
		return errors.New("should not be invoked")
	}, func() any { return &map[string]any{} })
	if err := s.GetMessageHandler(typ).Handler("x", &map[string]any{}); err != nil {
		t.Fatalf("duplicate register overrode first: %v", err)
	}

	// 反注册后即 miss。
	s.DeregisterMessageHandler(typ)
	if s.GetMessageHandler(typ) != nil {
		t.Fatal("handler still present after deregister")
	}
}

// typedA/typedB 是两个不同名的同形类型：泛型适配器按**类型身份**分派——
// 命中 *typedA 走业务路径，其它类型走 invalid payload struct type 错误分支。
type typedA struct{ Seq int }
type typedB struct{ Seq int }

func TestRegisterServerMessageHandlerTyped(t *testing.T) {
	s := NewServer()
	gotSeq := 0
	RegisterServerMessageHandler[typedA](s, 7, func(id SessionID, p *typedA) error {
		gotSeq = p.Seq
		return nil
	})
	h := s.GetMessageHandler(7)
	if h == nil {
		t.Fatal("typed handler missing")
	}
	// 命中 *typedA：正常路径。
	if err := h.Handler("s", &typedA{Seq: 3}); err != nil {
		t.Fatalf("typed path: %v", err)
	}
	if gotSeq != 3 {
		t.Fatalf("seq = %d, want 3", gotSeq)
	}
	// *typedB 不是 *typedA：fail-fast 报 invalid payload struct type。
	if err := h.Handler("s", &typedB{}); err == nil || !strings.Contains(err.Error(), "invalid payload struct type") {
		t.Fatalf("foreign payload err = %v, want invalid payload struct type", err)
	}
}

func TestServerMarshalMessage(t *testing.T) {
	bin := NewServer(WithPayloadType(PayloadTypeBinary))
	out, err := bin.marshalMessage(9, map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("binary marshal: %v", err)
	}
	var pkt BinaryNetPacket
	if err := pkt.Unmarshal(out); err != nil {
		t.Fatalf("binary unmarshal: %v", err)
	}
	if pkt.Type != 9 || len(pkt.Payload) == 0 {
		t.Fatalf("binary packet type=%d payload len=%d", pkt.Type, len(pkt.Payload))
	}

	txt := NewServer(WithPayloadType(PayloadTypeText))
	out, err = txt.marshalMessage(9, map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("text marshal: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("text marshal produced empty bytes")
	}

	// 未知 payloadType：fail-fast 报 unsupported。
	unk := NewServer()
	unk.payloadType = PayloadType(250)
	if _, err := unk.marshalMessage(9, map[string]any{}); err == nil || !strings.Contains(err.Error(), "unsupported payload type") {
		t.Fatalf("unknown payloadType err = %v", err)
	}

	// 自定义 marshaler 接管：defaultMarshalNetPacket 不再被走。
	customCalled := false
	c := NewServer()
	c.netPacketMarshaler = func(NetMessageType, MessagePayload) ([]byte, error) {
		customCalled = true
		return []byte("custom"), nil
	}
	if _, err := c.marshalMessage(1, map[string]any{}); err != nil || !customCalled {
		t.Fatalf("custom marshaler called=%v err=%v", customCalled, err)
	}
	// init 的缺省注入路径：unmarshaler 与 raw handler 兜底就位。
	u := NewServer()
	if u.netPacketUnmarshaler == nil || u.socketRawDataHandler == nil {
		t.Fatal("init did not inject default unmarshaler/raw handler")
	}
}

func TestServerSendRawMessageUnknownSession(t *testing.T) {
	s := NewServer()
	if err := s.SendRawMessage("no-such-session", []byte("x")); err == nil || !strings.Contains(err.Error(), "session not found") {
		t.Fatalf("err = %v, want session not found", err)
	}
}

func TestClientOptionsApply(t *testing.T) {
	c := &Client{}
	cm := &countingMetrics{}
	opts := []ClientOption{
		WithSignalURL("ws://127.0.0.1:7080/signal"),
		WithAuthorization("tok-1"),
		WithClientCodec("json"),
		WithClientPayloadType(PayloadTypeText),
		WithClientDataChannelLabel("ctrl"),
		WithClientWebRTCConfiguration(webrtc.Configuration{}),
		WithClientConnectTimeout(3 * time.Second),
		WithClientSignalTimeout(5 * time.Second),
		WithClientMetrics(cm),
	}
	for _, o := range opts {
		o(c)
	}
	if c.signalURL != "ws://127.0.0.1:7080/signal" || c.authorization != "tok-1" {
		t.Fatalf("signalURL/auth = %s/%s", c.signalURL, c.authorization)
	}
	if c.payloadType != PayloadTypeText || c.dataChannelLabel != "ctrl" || c.m == nil {
		t.Fatalf("payloadType=%d label=%q metrics nil=%v", c.payloadType, c.dataChannelLabel, c.m == nil)
	}
	if c.connectTimeout != 3*time.Second || c.signalTimeout != 5*time.Second {
		t.Fatalf("timeouts = %v/%v", c.connectTimeout, c.signalTimeout)
	}
	// 非正超时不覆盖默认。
	c2 := &Client{connectTimeout: 10 * time.Second, signalTimeout: 10 * time.Second}
	WithClientConnectTimeout(0)(c2)
	WithClientSignalTimeout(-1)(c2)
	if c2.connectTimeout != 10*time.Second || c2.signalTimeout != 10*time.Second {
		t.Fatalf("non-positive timeout overrode defaults: %v/%v", c2.connectTimeout, c2.signalTimeout)
	}
}

func TestWebrtcLoggerHelpers(t *testing.T) {
	// 帮助函数直跑（go-logr 桥接层的调用面）：不 panic 即为断言。
	logWarnf("warn %s", "x")
	logErrorf("err %s", "y")
	logError("plain", 1)
}
