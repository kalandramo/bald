package websocket

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	ws "github.com/gorilla/websocket"

	"github.com/kalandramo/bald/encoding"
	jsoncodec "github.com/kalandramo/bald/encoding/json"
)

// 按仓内惯例显式注册 JSON codec（encoding/json 设计为不做 init 自注册）。
func init() {
	encoding.MustRegister(jsoncodec.New())
}

// ---------------------------------------------------------------------------
// NewServer + defaults
// ---------------------------------------------------------------------------

func TestNewServer_Defaults(t *testing.T) {
	srv := NewServer(":0")
	if srv.Addr() != ":0" {
		t.Errorf("Addr() = %q, want %q", srv.Addr(), ":0")
	}
	if srv.path != "/ws" {
		t.Errorf("path = %q, want %q", srv.path, "/ws")
	}
	if srv.payloadType != PayloadTypeBinary {
		t.Errorf("payloadType = %v, want %v", srv.payloadType, PayloadTypeBinary)
	}
	if !srv.injectToken {
		t.Errorf("injectToken = false, want true")
	}
	if srv.tokenKey != "token" {
		t.Errorf("tokenKey = %q, want %q", srv.tokenKey, "token")
	}
	if srv.SessionCount() != 0 {
		t.Errorf("SessionCount() = %d, want 0", srv.SessionCount())
	}
}

func TestNewServer_Options(t *testing.T) {
	srv := NewServer(":0",
		WithPath("/wschat"),
		WithPayloadType(PayloadTypeText),
		WithReadBufferSize(2048),
		WithWriteBufferSize(2048),
		WithInjectTokenToQuery(false, "auth"),
	)
	if srv.path != "/wschat" {
		t.Errorf("path = %q, want %q", srv.path, "/wschat")
	}
	if srv.payloadType != PayloadTypeText {
		t.Errorf("payloadType = %v, want %v", srv.payloadType, PayloadTypeText)
	}
	if srv.upgrader.ReadBufferSize != 2048 {
		t.Errorf("ReadBufferSize = %d, want %d", srv.upgrader.ReadBufferSize, 2048)
	}
	if srv.upgrader.WriteBufferSize != 2048 {
		t.Errorf("WriteBufferSize = %d, want %d", srv.upgrader.WriteBufferSize, 2048)
	}
	if srv.injectToken {
		t.Errorf("injectToken = true, want false")
	}
	if srv.tokenKey != "auth" {
		t.Errorf("tokenKey = %q, want %q", srv.tokenKey, "auth")
	}
}

// ---------------------------------------------------------------------------
// Endpoint
// ---------------------------------------------------------------------------

func TestServer_Endpoint(t *testing.T) {
	srv := NewServer(":9090")
	ep := srv.Endpoint()
	if !strings.HasPrefix(ep, "ws://") {
		t.Errorf("Endpoint() = %q, want ws:// prefix", ep)
	}
}

func TestServer_Endpoint_WSS(t *testing.T) {
	srv := NewServer(":9090", WithTLSConfig(&tls.Config{}))
	ep := srv.Endpoint()
	if !strings.HasPrefix(ep, "wss://") {
		t.Errorf("Endpoint() = %q, want wss:// prefix", ep)
	}
}

func TestServer_Endpoint_NormalizeWildcard(t *testing.T) {
	srv := NewServer("0.0.0.0:9090")
	ep := srv.Endpoint()
	if !strings.HasPrefix(ep, "ws://localhost:") {
		t.Errorf("Endpoint() = %q, want ws://localhost: prefix", ep)
	}
}

// ---------------------------------------------------------------------------
// Message handler registration
// ---------------------------------------------------------------------------

func TestServer_RegisterMessageHandler(t *testing.T) {
	srv := NewServer(":0")

	const MsgTypeTest NetMessageType = 100
	srv.RegisterMessageHandler(MsgTypeTest,
		func(sid SessionID, payload MessagePayload) error {
			return nil
		},
		nil,
	)

	h := srv.GetMessageHandler(MsgTypeTest)
	if h == nil {
		t.Fatal("GetMessageHandler returned nil after registration")
	}

	// Deregister
	srv.DeregisterMessageHandler(MsgTypeTest)
	if srv.GetMessageHandler(MsgTypeTest) != nil {
		t.Error("handler should be nil after deregister")
	}
}

func TestServer_RegisterMessageHandler_Duplicate(t *testing.T) {
	srv := NewServer(":0")

	const MsgTypeTest NetMessageType = 100
	srv.RegisterMessageHandler(MsgTypeTest,
		func(sid SessionID, payload MessagePayload) error { return nil },
		nil,
	)
	// Duplicate registration should be ignored
	srv.RegisterMessageHandler(MsgTypeTest,
		func(sid SessionID, payload MessagePayload) error { return fmt.Errorf("should not replace") },
		nil,
	)

	h := srv.GetMessageHandler(MsgTypeTest)
	err := h.Handler("test", []byte("data"))
	if err != nil {
		t.Errorf("duplicate registration should not replace handler, got error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// BinaryNetPacket
// ---------------------------------------------------------------------------

func TestBinaryNetPacket_MarshalUnmarshal(t *testing.T) {
	msg := BinaryNetPacket{
		Type:    10000,
		Payload: []byte("Hello World"),
	}

	buf, err := msg.Marshal()
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var msg2 BinaryNetPacket
	if err := msg2.Unmarshal(buf); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if msg2.Type != 10000 {
		t.Errorf("Type = %d, want %d", msg2.Type, 10000)
	}
	if string(msg2.Payload) != "Hello World" {
		t.Errorf("Payload = %q, want %q", string(msg2.Payload), "Hello World")
	}
}

// ---------------------------------------------------------------------------
// TextNetPacket
// ---------------------------------------------------------------------------

func TestTextNetPacket_MarshalUnmarshal(t *testing.T) {
	msg := TextNetPacket{
		Type:    5000,
		Payload: "Hello Text",
	}

	buf, err := msg.Marshal()
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var msg2 TextNetPacket
	if err := msg2.Unmarshal(buf); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if msg2.Type != 5000 {
		t.Errorf("Type = %d, want %d", msg2.Type, 5000)
	}
	if msg2.Payload != "Hello Text" {
		t.Errorf("Payload = %q, want %q", msg2.Payload, "Hello Text")
	}
}

// ---------------------------------------------------------------------------
// Start/Stop lifecycle
// ---------------------------------------------------------------------------

func TestServer_StartStop(t *testing.T) {
	srv := NewServer("127.0.0.1:0", WithPath("/ws"))

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- srv.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop within 5s")
	}
}

func TestServer_StopBeforeStart(t *testing.T) {
	srv := NewServer(":0")
	if err := srv.Stop(context.Background()); err != nil {
		t.Errorf("Stop before Start returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// WebSocket integration: connect and send/receive
// ---------------------------------------------------------------------------

func TestServer_WS_ConnectBinary(t *testing.T) {
	const MsgTypeEcho NetMessageType = 1

	srv := NewServer("127.0.0.1:0",
		WithPath("/ws"),
		WithPayloadType(PayloadTypeBinary),
	)

	received := make(chan []byte, 1)
	srv.RegisterMessageHandler(MsgTypeEcho,
		func(sid SessionID, payload MessagePayload) error {
			received <- payload.([]byte)
			return nil
		},
		nil,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("ws://%s/ws", addr)

	// Connect a client
	dialer := ws.Dialer{}
	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Dial error: %v", err)
	}
	defer conn.Close()

	time.Sleep(100 * time.Millisecond)

	if srv.SessionCount() != 1 {
		t.Errorf("SessionCount() = %d, want 1", srv.SessionCount())
	}

	// Send a binary message: [4-byte LE type][payload]
	msgTypeBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(msgTypeBuf, uint32(MsgTypeEcho))
	sendData := append(msgTypeBuf, []byte("hello-binary")...)

	if err := conn.WriteMessage(ws.BinaryMessage, sendData); err != nil {
		t.Fatalf("WriteMessage error: %v", err)
	}

	select {
	case data := <-received:
		if string(data) != "hello-binary" {
			t.Errorf("received = %q, want %q", string(data), "hello-binary")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive message within 3s")
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestServer_WS_ConnectText(t *testing.T) {
	const MsgTypeEcho NetMessageType = 2

	srv := NewServer("127.0.0.1:0",
		WithPath("/ws"),
		WithPayloadType(PayloadTypeText),
	)

	received := make(chan string, 1)
	srv.RegisterMessageHandler(MsgTypeEcho,
		func(sid SessionID, payload MessagePayload) error {
			received <- string(payload.([]byte))
			return nil
		},
		nil, // nil Creator: payload = raw bytes
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("ws://%s/ws", addr)

	conn, _, err := ws.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Dial error: %v", err)
	}
	defer conn.Close()

	time.Sleep(100 * time.Millisecond)

	// Send text message: JSON {"type":2,"payload":"hello-text"}
	sendJSON := `{"type":2,"payload":"hello-text"}`
	if err := conn.WriteMessage(ws.TextMessage, []byte(sendJSON)); err != nil {
		t.Fatalf("WriteMessage error: %v", err)
	}

	select {
	case data := <-received:
		if data != "hello-text" {
			t.Errorf("received = %q, want %q", data, "hello-text")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive text message within 3s")
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestServer_WS_Broadcast(t *testing.T) {
	const MsgTypeBroad NetMessageType = 3

	srv := NewServer("127.0.0.1:0",
		WithPath("/ws"),
		WithPayloadType(PayloadTypeBinary),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("ws://%s/ws", addr)

	// Connect 2 clients
	conn1, _, err := ws.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Dial1 error: %v", err)
	}
	defer conn1.Close()

	conn2, _, err := ws.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Dial2 error: %v", err)
	}
	defer conn2.Close()

	time.Sleep(150 * time.Millisecond)

	if srv.SessionCount() != 2 {
		t.Errorf("SessionCount() = %d, want 2", srv.SessionCount())
	}

	// Broadcast to all sessions. Use string so JSON codec produces a clean JSON string.
	srv.Broadcast(MsgTypeBroad, "broadcast-hello")

	// Both clients should receive the message
	readTimeout := func(conn *ws.Conn) ([]byte, error) {
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, data, err := conn.ReadMessage()
		return data, err
	}

	for i, conn := range []*ws.Conn{conn1, conn2} {
		data, err := readTimeout(conn)
		if err != nil {
			t.Fatalf("client %d ReadMessage error: %v", i, err)
		}
		// Binary format: [4-byte LE type][payload]
		if len(data) < 4 {
			t.Fatalf("client %d: message too short", i)
		}
		msgType := binary.LittleEndian.Uint32(data[:4])
		payload := string(data[4:])

		if msgType != uint32(MsgTypeBroad) {
			t.Errorf("client %d msgType = %d, want %d", i, msgType, MsgTypeBroad)
		}
		// JSON codec marshals string to "broadcast-hello" (with quotes)
		if payload != `"broadcast-hello"` {
			t.Errorf("client %d payload = %q, want %q", i, payload, `"broadcast-hello"`)
		}
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// Connect handler
// ---------------------------------------------------------------------------

func TestServer_ConnectHandler(t *testing.T) {
	connected := make(chan SessionID, 1)
	disconnected := make(chan SessionID, 1)

	srv := NewServer("127.0.0.1:0",
		WithPath("/ws"),
		WithSocketConnectHandler(func(sid SessionID, _ url.Values, connect bool) {
			if connect {
				connected <- sid
			} else {
				disconnected <- sid
			}
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("ws://%s/ws", addr)

	conn, _, err := ws.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Dial error: %v", err)
	}

	// Wait for connect callback
	select {
	case sid := <-connected:
		if sid == "" {
			t.Error("connect handler returned empty session ID")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive connect callback within 3s")
	}

	// Disconnect
	conn.Close()

	// Wait for disconnect callback
	select {
	case <-disconnected:
		// success
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive disconnect callback within 3s")
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

func TestServer_Middleware(t *testing.T) {
	headerSet := false
	srv := NewServer("127.0.0.1:0", WithPath("/ws"))
	srv.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Custom", "ws-middleware")
			headerSet = true
			next.ServeHTTP(w, r)
		})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("ws://%s/ws", addr)

	conn, _, err := ws.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Dial error: %v", err)
	}
	conn.Close()

	if !headerSet {
		t.Error("middleware was not invoked")
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// HandleFunc (custom routes)
// ---------------------------------------------------------------------------

func TestServer_HandleFunc(t *testing.T) {
	srv := NewServer("127.0.0.1:0")
	srv.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	addr := srv.listener.Addr().String()
	url := fmt.Sprintf("http://%s/health", addr)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("HTTP GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// SessionManager
// ---------------------------------------------------------------------------

func TestSessionManager(t *testing.T) {
	sm := NewSessionManager(nil)
	if sm.Count() != 0 {
		t.Errorf("Count() = %d, want 0", sm.Count())
	}
}
