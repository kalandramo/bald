package websocket

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ws "github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// tempTLSCertFiles 生成自签证书并落到临时文件，返回 cert/key 路径。
func tempTLSCertFiles(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "ws-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "c.pem")
	keyFile = filepath.Join(dir, "k.pem")
	cf := func(p string, b []byte, typ string) {
		f, err := os.Create(p)
		if err != nil {
			t.Fatalf("create %s: %v", p, err)
		}
		_ = pem.Encode(f, &pem.Block{Type: typ, Bytes: b})
		_ = f.Close()
	}
	cf(certFile, der, "CERTIFICATE")
	cf(keyFile, x509.MarshalPKCS1PrivateKey(priv), "RSA PRIVATE KEY")
	return certFile, keyFile
}

// wsPair 建立一对已握手的 websocket 连接（服务端 + 客户端），走 httptest。
func wsPair(t *testing.T) (client, server *ws.Conn) {
	t.Helper()
	up := ws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ready := make(chan *ws.Conn, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			ready <- nil
			return
		}
		ready <- c
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	dialer := ws.Dialer{}
	client, _, err := dialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	select {
	case server = <-ready:
		if server == nil {
			t.Fatal("server upgrade failed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no server conn")
	}
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

// binaryFrame 构造 [4 字节 LE type][payload] 二进制包。
func binaryFrame(t *testing.T, typ NetMessageType, payload any) []byte {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint32(typ))
	buf.Write(b)
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// options
// ---------------------------------------------------------------------------

func TestOptions_TLSAndPanic(t *testing.T) {
	certFile, keyFile := tempTLSCertFiles(t)
	srv := NewServer(":0", WithTLS(certFile, keyFile))
	if srv.tlsConfig == nil || len(srv.tlsConfig.Certificates) != 1 {
		t.Fatal("WithTLS did not load certificate")
	}

	// 坏路径 → panic
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("WithTLS with bad paths should panic")
			}
		}()
		NewServer(":0", WithTLS("/nope/cert", "/nope/key"))
	}()
}

func TestOptions_SimpleSetters(t *testing.T) {
	var mwCalled int32
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&mwCalled, 1)
			next.ServeHTTP(w, r)
		})
	}
	srv := NewServer(":0",
		WithMiddleware(mw),
		WithCheckOrigin("example.com"),
		WithEnableCompression(true),
		WithHandshakeTimeout(5*time.Second),
		WithCodec("json"),
	)
	if len(srv.middlewares) != 1 {
		t.Fatalf("middlewares = %d, want 1", len(srv.middlewares))
	}
	if !srv.upgrader.CheckOrigin(mustOriginReq(t, "example.com")) {
		t.Error("CheckOrigin should accept matching origin")
	}
	if srv.upgrader.CheckOrigin(mustOriginReq(t, "evil.com")) {
		t.Error("CheckOrigin should reject mismatched origin")
	}
	if !srv.upgrader.EnableCompression {
		t.Error("EnableCompression not set")
	}
	if srv.upgrader.HandshakeTimeout != 5*time.Second {
		t.Error("HandshakeTimeout not set")
	}
	if srv.codec == nil {
		t.Error("WithCodec(json) left codec nil")
	}

	// WithCodec 空名不改
	srv2 := NewServer(":0", WithCodec(""))
	if srv2.codec == nil {
		t.Error("default codec should be set by NewServer")
	}

	// WithChannelBufferSize 命中全局
	WithChannelBufferSize(16)(srv)
	if channelBufSize != 16 {
		t.Errorf("channelBufSize = %d, want 16", channelBufSize)
	}
	WithChannelBufferSize(256)(srv) // 还原
}

func mustOriginReq(t *testing.T, origin string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", origin)
	return r
}

func TestOptions_MarshalersAndRawHandler(t *testing.T) {
	mm := func(NetMessageType, MessagePayload) ([]byte, error) { return []byte("m"), nil }
	um := func([]byte) (*MessageHandlerData, MessagePayload, error) { return nil, nil, nil }
	raw := func(SessionID, []byte) error { return nil }

	srv := NewServer(":0",
		WithMessageMarshaler(mm),
		WithMessageUnmarshaler(um),
		WithSocketRawDataHandler(raw),
	)
	b, err := srv.netPacketMarshaler(1, "x")
	if err != nil || string(b) != "m" {
		t.Errorf("custom marshaler not wired: %q %v", b, err)
	}
	// WithSocketRawDataHandler(nil) 不覆盖已有
	WithSocketRawDataHandler(nil)(srv)
	if srv.socketRawDataHandler == nil {
		t.Error("nil raw handler should not overwrite")
	}
}

// ---------------------------------------------------------------------------
// message.go
// ---------------------------------------------------------------------------

func TestBinaryNetPacket_UnmarshalShort(t *testing.T) {
	var m BinaryNetPacket
	if err := m.Unmarshal([]byte{1, 2}); err == nil {
		t.Error("Unmarshal of <4 byte buffer should error")
	}
}

func TestTextNetPacket_UnmarshalBad(t *testing.T) {
	var m TextNetPacket
	if err := m.Unmarshal([]byte("{not json")); err == nil {
		t.Error("Unmarshal of bad JSON should error")
	}
}

// ---------------------------------------------------------------------------
// types.go
// ---------------------------------------------------------------------------

func TestMessageHandlerData_CreateNil(t *testing.T) {
	h := &MessageHandlerData{}
	if h.Create() != nil {
		t.Error("Create with nil Creator should return nil")
	}
}

// ---------------------------------------------------------------------------
// session_manager.go
// ---------------------------------------------------------------------------

func TestSessionManager_GetAndRemoveGuards(t *testing.T) {
	sm := NewSessionManager(nil)
	if sm.GetSession("missing") != nil {
		t.Error("GetSession missing should be nil")
	}
	sm.AddSession(nil) // nil guard
	sm.RemoveSession(nil)
	sm.RemoveSession(&Session{id: "ghost"}) // 不存在，不通知

	var added, removed int32
	sm.RegisterObserver(observerFuncs{
		onAdd:    func(*Session) { atomic.AddInt32(&added, 1) },
		onRemove: func(*Session) { atomic.AddInt32(&removed, 1) },
	})
	s := &Session{id: "live"}
	sm.AddSession(s)
	sm.AddSession(s) // 重复，不通知
	if atomic.LoadInt32(&added) != 1 {
		t.Errorf("added = %d, want 1", added)
	}
	if sm.GetSession("live") != s {
		t.Error("GetSession should return stored session")
	}

	// RangeSessions: fn 返回 false 提前中断
	count := 0
	sm.RangeSessions(func(SessionID, *Session) bool {
		count++
		return false
	})

	sm.RemoveSession(s)
	if atomic.LoadInt32(&removed) != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	sm.Clean()
}

type observerFuncs struct {
	onAdd    func(*Session)
	onRemove func(*Session)
}

func (o observerFuncs) OnSessionAdded(s *Session) {
	if o.onAdd != nil {
		o.onAdd(s)
	}
}
func (o observerFuncs) OnSessionRemoved(s *Session) {
	if o.onRemove != nil {
		o.onRemove(s)
	}
}

// ---------------------------------------------------------------------------
// server.go: Endpoint / lifecycle
// ---------------------------------------------------------------------------

func TestEndpoint_NoPort(t *testing.T) {
	srv := NewServer("weird-addr") // SplitHostPort 失败
	if ep := srv.Endpoint(); ep != "ws://weird-addr" {
		t.Errorf("Endpoint = %q, want ws://weird-addr", ep)
	}
}

func TestStart_TLSListener(t *testing.T) {
	certFile, keyFile := tempTLSCertFiles(t)
	srv := NewServer("127.0.0.1:0", WithTLS(certFile, keyFile))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	waitServerAddr(t, srv)
	cancel()
	if err := <-done; err != nil && !strings.Contains(err.Error(), "use of closed") {
		// TLS listener 下 Shutdown 正常返回 nil
		t.Logf("Start returned: %v", err)
	}
	_ = srv.Stop(context.Background())
}

func TestStart_ListenError(t *testing.T) {
	srv := NewServer("256.256.256.256:99999")
	if err := srv.Start(context.Background()); err == nil {
		t.Error("Start on invalid addr should error")
	}
}

// ---------------------------------------------------------------------------
// server.go: RegisterServerMessageHandler / Send
// ---------------------------------------------------------------------------

type echoPayload struct {
	S string `json:"s"`
}

func TestRegisterServerMessageHandler_BothBranches(t *testing.T) {
	srv := NewServer(":0")
	var got *echoPayload
	RegisterServerMessageHandler(srv, 42, func(_ SessionID, p *echoPayload) error {
		got = p
		return nil
	})
	h := srv.GetMessageHandler(42)
	if h == nil {
		t.Fatal("generic handler not registered")
	}
	// 正确类型
	if err := h.Handler("s", &echoPayload{S: "ok"}); err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if got == nil || got.S != "ok" {
		t.Errorf("payload not delivered: %+v", got)
	}
	// 错误类型 → default 分支
	if err := h.Handler("s", "not-a-ptr"); err == nil {
		t.Error("wrong payload type should error")
	}
	// Creator 创建 *echoPayload
	if _, ok := h.Create().(*echoPayload); !ok {
		t.Error("generic Creator should return *echoPayload")
	}
}

func TestSendRawMessage_NotFound(t *testing.T) {
	srv := NewServer(":0")
	if err := srv.SendRawMessage("nope", []byte("x")); err == nil {
		t.Error("SendRawMessage to missing session should error")
	}
}

func TestSendMessage_MarshalError(t *testing.T) {
	srv := NewServer(":0") // codec = json
	// chan 无法 JSON 序列化 → marshalMessage 出错
	if err := srv.SendMessage("sid", 1, make(chan int)); err == nil {
		t.Error("SendMessage with unmarshalable payload should error")
	}
}

func TestBroadcast_MarshalError(t *testing.T) {
	srv := NewServer(":0")
	srv.Broadcast(1, make(chan int)) // 记录日志后返回，不 panic
}

// ---------------------------------------------------------------------------
// defaultMarshalNetPacket / defaultUnmarshalNetPacket / extractMessageType
// ---------------------------------------------------------------------------

func TestDefaultMarshal_Branches(t *testing.T) {
	// codec nil
	srv := &Server{}
	if _, err := srv.defaultMarshalNetPacket(1, "x"); err == nil {
		t.Error("nil codec should error")
	}

	// codec.Marshal error
	srv2 := NewServer(":0")
	if _, err := srv2.defaultMarshalNetPacket(1, make(chan int)); err == nil {
		t.Error("codec.Marshal error should propagate")
	}

	// text 分支
	srvT := NewServer(":0", WithPayloadType(PayloadTypeText))
	b, err := srvT.defaultMarshalNetPacket(7, echoPayload{S: "hi"})
	if err != nil {
		t.Fatalf("text marshal: %v", err)
	}
	var tn TextNetPacket
	if err := json.Unmarshal(b, &tn); err != nil || tn.Type != 7 {
		t.Errorf("text marshal decode = %+v err=%v", tn, err)
	}

	// unknown payload type
	srvBad := NewServer(":0")
	srvBad.payloadType = PayloadType(99)
	if _, err := srvBad.defaultMarshalNetPacket(1, "x"); err == nil {
		t.Error("unknown payload type should error")
	}
}

func TestDefaultUnmarshal_Branches(t *testing.T) {
	// binary unmarshal 太短
	srvB := NewServer(":0")
	if _, _, err := srvB.defaultUnmarshalNetPacket([]byte{1}); err == nil {
		t.Error("short binary should error")
	}

	// text unmarshal 坏 JSON
	srvT := NewServer(":0", WithPayloadType(PayloadTypeText))
	if _, _, err := srvT.defaultUnmarshalNetPacket([]byte("{bad")); err == nil {
		t.Error("bad text JSON should error")
	}

	// handler not found
	srvNF := NewServer(":0")
	if _, _, err := srvNF.defaultUnmarshalNetPacket(binaryFrame(t, 999, "x")); err == nil {
		t.Error("unregistered type should error")
	}

	// nil Creator → payload = raw
	srvRaw := NewServer(":0")
	var got MessagePayload
	srvRaw.RegisterMessageHandler(5, func(_ SessionID, p MessagePayload) error {
		got = p
		return nil
	}, nil)
	h, payload, err := srvRaw.defaultUnmarshalNetPacket(binaryFrame(t, 5, "rawbytes"))
	if err != nil || h == nil {
		t.Fatalf("raw unmarshal: %v", err)
	}
	if _, ok := payload.([]byte); !ok {
		t.Errorf("nil-Creator payload should be raw []byte, got %T", payload)
	}

	// Creator 非 nil 但 codec nil
	srvNC := &Server{messageHandlers: make(NetMessageHandlerMap)}
	srvNC.RegisterMessageHandler(6, func(SessionID, MessagePayload) error { return nil },
		func() any { return &echoPayload{} })
	if _, _, err := srvNC.defaultUnmarshalNetPacket(binaryFrame(t, 6, echoPayload{S: "a"})); err == nil {
		t.Error("non-nil Creator with nil codec should error")
	}

	// codec.Unmarshal error
	srvErr := NewServer(":0")
	srvErr.RegisterMessageHandler(8, func(SessionID, MessagePayload) error { return nil },
		func() any { return &echoPayload{} })
	// payload 是字符串 "not-an-object"，无法反序列化进 *echoPayload
	if _, _, err := srvErr.defaultUnmarshalNetPacket(binaryFrame(t, 8, "not-an-object")); err == nil {
		t.Error("codec.Unmarshal error should propagate")
	}
	_ = got
}

func TestDefaultHandleSocketRawData_And_Extract(t *testing.T) {
	srv := NewServer(":0")
	var hit int32
	srv.RegisterMessageHandler(11, func(SessionID, MessagePayload) error {
		atomic.AddInt32(&hit, 1)
		return nil
	}, nil)
	if err := srv.defaultHandleSocketRawData("s", binaryFrame(t, 11, "x")); err != nil {
		t.Fatalf("handle raw: %v", err)
	}
	if atomic.LoadInt32(&hit) != 1 {
		t.Error("handler not invoked")
	}
	// unmarshal error path
	if err := srv.defaultHandleSocketRawData("s", []byte{1}); err == nil {
		t.Error("short buf should error")
	}

	// extractMessageType default
	if got := extractMessageType(nil, PayloadType(99)); got != 0 {
		t.Errorf("extract default = %d, want 0", got)
	}
	if got := extractMessageType(binaryFrame(t, 12, "x"), PayloadTypeBinary); got != 12 {
		t.Errorf("extract binary = %d, want 12", got)
	}
	tj, _ := (&TextNetPacket{Type: 13}).Marshal()
	if got := extractMessageType(tj, PayloadTypeText); got != 13 {
		t.Errorf("extract text = %d, want 13", got)
	}
}

// ---------------------------------------------------------------------------
// wsHandler
// ---------------------------------------------------------------------------

func TestWsHandler_UpgradeError(t *testing.T) {
	srv := NewServer(":0")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil) // 非握手请求
	srv.wsHandler(rec, req)                                // 命中 upgrade 失败分支
	if rec.Code == http.StatusSwitchingProtocols {
		t.Error("plain GET should not upgrade")
	}
}

func TestWsHandler_TokenInjection(t *testing.T) {
	srv := NewServer("127.0.0.1:0", WithPath("/ws"), WithInjectTokenToQuery(true, "token"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	addr := waitServerAddr(t, srv)

	dialer := ws.Dialer{Subprotocols: []string{"my-secret-token"}}
	conn, _, err := dialer.Dial(fmt.Sprintf("ws://%s/ws", addr), nil)
	if err != nil {
		t.Fatalf("dial with subprotocol: %v", err)
	}
	defer conn.Close()

	// 会话建立后其 queries 应含注入的 token
	deadline := time.Now().Add(2 * time.Second)
	var found url.Values
	for time.Now().Before(deadline) {
		srv.sessionManager.RangeSessions(func(_ SessionID, s *Session) bool {
			found = s.Queries()
			return false
		})
		if found != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if found == nil || found.Get("token") != "my-secret-token" {
		t.Errorf("token not injected into queries: %v", found)
	}
}

// ---------------------------------------------------------------------------
// session: NewSession / send* / pumps
// ---------------------------------------------------------------------------

func TestNewSession_NilConnPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewSession(nil conn) should panic")
		}
	}()
	NewSession(nil, nil, url.Values{})
}

func TestSession_SendMessages_NilConn(t *testing.T) {
	client, server := wsPair(t)
	_ = client
	srv := NewServer(":0")
	s := NewSession(srv, server, url.Values{})
	s.closeConnect() // conn 置 nil

	if err := s.sendTextMessage("x"); err != nil {
		t.Errorf("sendTextMessage nil conn: %v", err)
	}
	if err := s.sendBinaryMessage([]byte("x")); err != nil {
		t.Errorf("sendBinaryMessage nil conn: %v", err)
	}
	if err := s.sendPongMessage("x"); err != nil {
		t.Errorf("sendPongMessage nil conn: %v", err)
	}
}

func TestSession_CloseConnect_Error(t *testing.T) {
	client, server := wsPair(t)
	_ = client
	srv := NewServer(":0")
	s := NewSession(srv, server, url.Values{})
	// 先粗暴关闭底层 net.Conn，再走 closeConnect：conn.Close() 对已关闭连接返回错误，
	// 命中 closeConnect 的日志分支。
	if err := server.UnderlyingConn().Close(); err != nil {
		t.Fatalf("close underlying: %v", err)
	}
	s.closeConnect()
}

func TestSession_SendMessages_Success(t *testing.T) {
	client, server := wsPair(t)
	srv := NewServer(":0")
	s := NewSession(srv, server, url.Values{})

	if err := s.sendTextMessage("hello"); err != nil {
		t.Fatalf("sendTextMessage: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, data, err := client.ReadMessage()
	if err != nil || mt != ws.TextMessage || string(data) != "hello" {
		t.Errorf("client got %v %q %v", mt, data, err)
	}

	if err := s.sendBinaryMessage([]byte("bin")); err != nil {
		t.Fatalf("sendBinaryMessage: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, data, err = client.ReadMessage()
	if err != nil || mt != ws.BinaryMessage || string(data) != "bin" {
		t.Errorf("client got %v %q %v", mt, data, err)
	}

	if err := s.sendPongMessage("pong"); err != nil {
		t.Fatalf("sendPongMessage: %v", err)
	}
}

func TestSession_SendMessage_DoneCase(t *testing.T) {
	_, server := wsPair(t)
	srv := NewServer(":0")
	s := NewSession(srv, server, url.Values{})
	// 填满发送通道（不 Listen，无消费者），随后关闭 done。
	// SendMessage 时 send 满（阻塞）、done 就绪 → 恒命中 <-s.done 分支。
	for i := 0; i < cap(s.send); i++ {
		s.send <- []byte{}
	}
	s.Close()
	s.SendMessage([]byte("late"))
}

func TestSend_RawAndTyped_Success(t *testing.T) {
	srv := NewServer(":0", WithPayloadType(PayloadTypeBinary))
	_, server := wsPair(t)
	s := NewSession(srv, server, url.Values{})
	srv.sessionManager.AddSession(s)

	if err := srv.SendRawMessage(s.SessionID(), []byte("hi")); err != nil {
		t.Errorf("SendRawMessage success: %v", err)
	}
	if err := srv.SendMessage(s.SessionID(), 1, echoPayload{S: "x"}); err != nil {
		t.Errorf("SendMessage success: %v", err)
	}
	// 未知会话
	if err := srv.SendMessage("ghost", 1, echoPayload{S: "x"}); err == nil {
		t.Error("SendMessage to ghost session should error (session not found)")
	}
}

func TestStart_ServeErrorPropagates(t *testing.T) {
	srv := NewServer("127.0.0.1:0", WithPath("/ws"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	waitServerAddr(t, srv)

	// 直接关闭 listener：Serve 收到非 ErrServerClosed 的 accept 错误 →
	// errChan<-err → select 的 errChan 分支 return err。
	srv.stateMu.RLock()
	ln := srv.listener
	srv.stateMu.RUnlock()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected non-nil error after listener closed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after listener closed")
	}
}

func TestSession_ReadPump_BinaryAndUnexpectedClose(t *testing.T) {
	client, server := wsPair(t)
	srv := NewServer(":0", WithPayloadType(PayloadTypeBinary))
	var binHits int32
	srv.RegisterMessageHandler(21, func(SessionID, MessagePayload) error {
		atomic.AddInt32(&binHits, 1)
		return nil
	}, nil)

	s := NewSession(srv, server, url.Values{})
	s.Listen()

	// 客户端发送 binary frame → readPump 数据分支 → handleSocketRawData
	if err := client.WriteMessage(ws.BinaryMessage, binaryFrame(t, 21, "x")); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&binHits) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&binHits) != 1 {
		t.Errorf("binary handler hits = %d, want 1", binHits)
	}

	// 客户端以「非预期」关闭码(4000)发关闭帧 → 服务端 ReadMessage 得到 *CloseError{4000}，
	// IsUnexpectedCloseError 返回真 → readPump 记录日志并退出（命中 211 分支）。
	_ = client.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(4000, "bye"), time.Now().Add(time.Second))
	s.Wait()
}

func TestSession_ReadPump_DoneFirst(t *testing.T) {
	_, server := wsPair(t)
	srv := NewServer(":0")
	s := NewSession(srv, server, url.Values{})
	// 通过 Close() 关闭 done（走 closeOnce），再同步跑 readPump：
	// 首轮 select 命中 <-s.done return（199 分支）；其 defer 里的 Close 因 once 已执行而无副作用。
	s.Close()
	s.wg.Add(1)
	s.readPump()
}

func TestSession_ReadPump_NilConn(t *testing.T) {
	_, server := wsPair(t)
	srv := NewServer(":0")
	s := NewSession(srv, server, url.Values{})
	// Listen 之前把 conn 置 nil：readPump 首轮 Conn()==nil 直接 return。
	s.connMu.Lock()
	s.conn = nil
	s.connMu.Unlock()
	s.Listen()
	s.Wait()
}

func TestSession_WritePump_BinaryError(t *testing.T) {
	_, server := wsPair(t)
	srv := NewServer(":0", WithPayloadType(PayloadTypeBinary))
	s := NewSession(srv, server, url.Values{})
	// 关闭底层连接但保留 *ws.Conn：写出报错。预置一条待发消息后单独跑 writePump，
	// done 尚未关闭 → select 只可能取 send 分支 → sendBinaryMessage 报错 → 178-180 日志并退出。
	_ = server.UnderlyingConn().Close()
	s.send <- binaryFrame(t, 1, "dead")
	s.wg.Add(1)
	s.writePump()
}

func TestSession_WritePump_TextError(t *testing.T) {
	_, server := wsPair(t)
	srv := NewServer(":0", WithPayloadType(PayloadTypeText))
	s := NewSession(srv, server, url.Values{})
	_ = server.UnderlyingConn().Close()
	s.send <- []byte("dead")
	s.wg.Add(1)
	s.writePump()
}
