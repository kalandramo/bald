package contract

// mapping_offline_test.go —— server.websocket 映射行的线上判据
// （真监听、真握手、真帧往返；「offline」指不需要任何外部后端）。
//
// 为什么要这一层：contract_test.go 只证明「Option 被追加」，证明不了
// 「契约值落到了 socket / mux 上」。§7.14 那一族的教训正是构造期绿、线上没生效。
//
// 覆盖的行：
//   - addr  → 契约端口接受 WS 握手并按帧回显（含会话登记）；
//   - path  → 握手落在契约路径上，实现默认 /ws 同时失效（路径真的换掉了）；
//   - path 空 → 保留默认 /ws（空串没把路径设成 ""）；
//   - tls.file   → 服务端出示的就是契约证书；明文 ws:// 探测握手不过（无静默回落）；
//   - tls.config → 内联 PEM 同源，且 Endpoint() 方案切成 wss；
//   - tls.file.ca → 双向：无客户端证书拿不到回显、带上即通（ca 不是摆设）。
//
// after-build 通道同步判定：消息处理器在监听建立前登记完并真的被调用。

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	ws "github.com/kalandramo/bald/transport/websocket"
)

const (
	testMsgType ws.NetMessageType = 7
	testWait    time.Duration     = 5 * time.Second
)

// encodeFrame 按 BinaryNetPacket 布局编码：4 字节 LE uint32 类型 + 原样 payload。
// 处理器以 nil Creator 注册 → 载荷按原始字节递交（不经 codec），
// 因此这条判据不依赖任何编解码器注册。
func encodeFrame(msgType ws.NetMessageType, body string) []byte {
	frame := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(frame, uint32(msgType))
	copy(frame[4:], body)
	return frame
}

// echoOption：after-build 里登记回显处理器（证明接线时机早于监听建立）。
func echoOption() Option {
	return WithAfterBuild(func(_ context.Context, s *ws.Server) error {
		s.RegisterMessageHandler(testMsgType, func(sid ws.SessionID, payload ws.MessagePayload) error {
			raw, ok := payload.([]byte)
			if !ok {
				return fmt.Errorf("payload = %T, want []byte (nil Creator 应原样递交)", payload)
			}
			return s.SendRawMessage(sid, raw)
		}, nil)
		return nil
	})
}

// runWs 走 Provider 构造并 Start（后台），返回产物与其 Endpoint。
func runWs(t *testing.T, opts []Option, cfg *bootstrapv1.Server) (*ws.Server, string) {
	t.Helper()
	srv, cleanup, err := Provider(opts...)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil")
	}
	wsrv, ok := srv.(*ws.Server)
	if !ok {
		t.Fatalf("srv = %T, want *websocket.Server", srv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- wsrv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("websocket Start returned %v, want nil or context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("websocket Start 未在取消后返回（残留监听 goroutine）")
		}
	})
	return wsrv, wsrv.Endpoint()
}

// dialWS 在就绪轮询里做一次 WS 握手（Start 异步建监听）。
func dialWS(t *testing.T, desc, url string, d *gorilla.Dialer) *gorilla.Conn {
	t.Helper()
	deadline := time.Now().Add(testWait)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, _, err := d.Dial(url, nil)
		if err == nil {
			return conn
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: dial %s: %v", desc, url, lastErr)
	return nil
}

// mustNotDialWS 握手必须失败（路径不存在 / 明文打到 TLS 端口 / mTLS 缺证书）。
func mustNotDialWS(t *testing.T, desc, url string, d *gorilla.Dialer) {
	t.Helper()
	conn, resp, err := d.Dial(url, nil)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("%s: 握手意外成功 (%s)", desc, url)
	}
	if resp != nil {
		t.Logf("%s: rejected with HTTP %s", desc, resp.Status)
	}
}

// exchangeWS 发一帧、读一次回包。
func exchangeWS(t *testing.T, conn *gorilla.Conn, body string) string {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(testWait)); err != nil {
		t.Fatalf("SetWriteDeadline: %v", err)
	}
	if err := conn.WriteMessage(gorilla.BinaryMessage, encodeFrame(testMsgType, body)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(testWait)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	return string(data)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

func loopbackAddr(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("127.0.0.1:%d", freePort(t))
}

// --- addr / path 行 --------------------------------------------------------

// TestMappingOffline_AddrAndPathReachWire 契约 addr+path 上真的能握手并回显；
// 同端口的实现默认路径 /ws 必须 404——path 落到了 mux 注册上，不是装饰。
func TestMappingOffline_AddrAndPathReachWire(t *testing.T) {
	addr := loopbackAddr(t)
	wsrv, ep := runWs(t, []Option{echoOption()}, &bootstrapv1.Server{
		Websocket: &bootstrapv1.Server_Websocket{Addr: addr, Path: "/live"},
	})
	if ep != "ws://"+addr {
		t.Fatalf("Endpoint() = %q, want ws://%s", ep, addr)
	}

	conn := dialWS(t, "handshake at contract path", "ws://"+addr+"/live", &gorilla.Dialer{HandshakeTimeout: testWait})
	defer conn.Close()
	if got := exchangeWS(t, conn, "ping"); got != "ping" {
		t.Fatalf("echo = %q, want ping（after-build 登记的处理器没被调用？）", got)
	}
	if n := wsrv.SessionCount(); n != 1 {
		t.Fatalf("SessionCount() = %d, want 1（连接没进会话管理？）", n)
	}

	mustNotDialWS(t, "默认路径不该再接管", "ws://"+addr+"/ws", &gorilla.Dialer{HandshakeTimeout: testWait})
}

// TestMappingOffline_EmptyPathKeepsDefault 空 path 段 → 握手仍落在默认 /ws。
func TestMappingOffline_EmptyPathKeepsDefault(t *testing.T) {
	addr := loopbackAddr(t)
	runWs(t, []Option{echoOption()}, &bootstrapv1.Server{
		Websocket: &bootstrapv1.Server_Websocket{Addr: addr},
	})

	conn := dialWS(t, "handshake at default path", "ws://"+addr+"/ws", &gorilla.Dialer{HandshakeTimeout: testWait})
	defer conn.Close()
	if got := exchangeWS(t, conn, "default-path"); got != "default-path" {
		t.Fatalf("echo = %q, want default-path", got)
	}
}

// TestMappingOffline_BusinessOptionOverridesPath 业务 Option 覆盖契约 path 之后，
// 握手只认 Option 的路径，契约路径不再有人挂（顺序判据的线上版）。
func TestMappingOffline_BusinessOptionOverridesPath(t *testing.T) {
	addr := loopbackAddr(t)
	runWs(t,
		[]Option{echoOption(), WithServerOptions(ws.WithPath("/override"))},
		&bootstrapv1.Server{Websocket: &bootstrapv1.Server_Websocket{Addr: addr, Path: "/contract"}})

	conn := dialWS(t, "handshake at override path", "ws://"+addr+"/override", &gorilla.Dialer{HandshakeTimeout: testWait})
	defer conn.Close()
	if got := exchangeWS(t, conn, "override"); got != "override" {
		t.Fatalf("echo = %q, want override", got)
	}
	mustNotDialWS(t, "契约路径应已被业务 Option 覆盖", "ws://"+addr+"/contract", &gorilla.Dialer{HandshakeTimeout: testWait})
}

// --- tls 行 ---------------------------------------------------------------

type testPKI struct {
	certPEM, keyPEM []byte
	cert            *x509.Certificate
}

// newTestPKI 自签一张 127.0.0.1 的证书（同时可作 CA 与客户端证书）。
func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(20261106),
		Subject:               pkix.Name{CommonName: "bald-ws-contract-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	return testPKI{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		cert:    cert,
	}
}

// writePKI 把证书/私钥落到临时目录，返回路径（契约 file 分支的输入）。
func writePKI(t *testing.T, pki testPKI) (certPath, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	certPath = filepath.Join(dir, "server.crt")
	keyPath = filepath.Join(dir, "server.key")
	if err := os.WriteFile(certPath, pki.certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pki.keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

// clientTLS 造 WS 客户端：RootCAs 只认这张自签证书，ServerName 走 IP SAN 校验。
func clientTLS(pki testPKI, withClientCert bool) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(pki.cert)
	cfg := &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	if withClientCert {
		cert, err := tls.X509KeyPair(pki.certPEM, pki.keyPEM)
		if err != nil {
			panic("X509KeyPair: " + err.Error())
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg
}

func wssDialer(pki testPKI, withClientCert bool) *gorilla.Dialer {
	return &gorilla.Dialer{
		HandshakeTimeout: testWait,
		TLSClientConfig:  clientTLS(pki, withClientCert),
	}
}

// TestMappingOffline_TlsFileCertServed 契约 file 分支的证书真的被服务端出示：
// 客户端只用这张证书做校验，握手即通过；明文 ws:// 探测握手不过（无静默回落）。
func TestMappingOffline_TlsFileCertServed(t *testing.T) {
	pki := newTestPKI(t)
	certPath, keyPath := writePKI(t, pki)
	addr := loopbackAddr(t)

	runWs(t, []Option{echoOption()}, &bootstrapv1.Server{
		Websocket: &bootstrapv1.Server_Websocket{
			Addr: addr, Path: "/secure",
			Tls: &bootstrapv1.Server_TLS{File: &bootstrapv1.Server_TLS_File{
				CertPath: &certPath, KeyPath: &keyPath,
			}},
		},
	})

	conn := dialWS(t, "wss handshake (contract cert)", "wss://"+addr+"/secure", wssDialer(pki, false))
	defer conn.Close()
	if rawConn, ok := conn.UnderlyingConn().(*tls.Conn); ok {
		state := rawConn.ConnectionState()
		if len(state.PeerCertificates) == 0 {
			t.Fatal("peer did not present a certificate")
		}
		if got := state.PeerCertificates[0].Subject.CommonName; got != "bald-ws-contract-test" {
			t.Fatalf("peer CN = %q, want bald-ws-contract-test（服务端出示的不是契约里那张证书）", got)
		}
	} else {
		t.Fatalf("underlying conn = %T, want *tls.Conn", conn.UnderlyingConn())
	}
	if got := exchangeWS(t, conn, "tls-ping"); got != "tls-ping" {
		t.Fatalf("echo over TLS = %q, want tls-ping", got)
	}

	mustNotDialWS(t, "明文打到 TLS 端口", "ws://"+addr+"/secure", &gorilla.Dialer{HandshakeTimeout: 2 * time.Second})
}

// TestMappingOffline_TlsConfigPemServed 契约 config 分支（内联 PEM）同源生效，
// 且 Endpoint() 的方案随 tls 切成 wss。
func TestMappingOffline_TlsConfigPemServed(t *testing.T) {
	pki := newTestPKI(t)
	addr := loopbackAddr(t)

	_, ep := runWs(t, []Option{echoOption()}, &bootstrapv1.Server{
		Websocket: &bootstrapv1.Server_Websocket{
			Addr: addr, Path: "/pem",
			Tls: &bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{
				CertPem: pki.certPEM, KeyPem: pki.keyPEM,
			}},
		},
	})
	if ep != "wss://"+addr {
		t.Fatalf("Endpoint() = %q, want wss://%s（tls 段没切方案）", ep, addr)
	}

	conn := dialWS(t, "wss handshake (inline pem)", "wss://"+addr+"/pem", wssDialer(pki, false))
	defer conn.Close()
	if got := exchangeWS(t, conn, "inline-pem"); got != "inline-pem" {
		t.Fatalf("echo = %q, want inline-pem", got)
	}
}

// TestMappingOffline_TlsCaEnablesMutualAuth 契约 ca 分支不是摆设：
// 服务端按 ca 池要求客户端证书——裸客户端拿不到回显，带上证书才通。
func TestMappingOffline_TlsCaEnablesMutualAuth(t *testing.T) {
	pki := newTestPKI(t)
	certPath, keyPath := writePKI(t, pki)
	caPath := filepath.Join(filepath.Dir(certPath), "ca.crt")
	if err := os.WriteFile(caPath, pki.certPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	addr := loopbackAddr(t)

	runWs(t, []Option{echoOption()}, &bootstrapv1.Server{
		Websocket: &bootstrapv1.Server_Websocket{
			Addr: addr, Path: "/mtls",
			Tls: &bootstrapv1.Server_TLS{File: &bootstrapv1.Server_TLS_File{
				CertPath: &certPath, KeyPath: &keyPath, CaPath: &caPath,
			}},
		},
	})

	// 就绪探活（带证书），不消耗判据。
	ready := dialWS(t, "mtls ready", "wss://"+addr+"/mtls", wssDialer(pki, true))
	_ = ready.Close()

	// 裸客户端：握手可能先过（证书在服务端校验），判据落在「拿不到回显」。
	bareFails := func() bool {
		conn, _, err := wssDialer(pki, false).Dial("wss://"+addr+"/mtls", nil)
		if err != nil {
			return true
		}
		defer conn.Close()
		if err := conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("SetWriteDeadline: %v", err)
		}
		if err := conn.WriteMessage(gorilla.BinaryMessage, encodeFrame(testMsgType, "should-not-pass")); err != nil {
			return true
		}
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		_, _, err = conn.ReadMessage()
		return err != nil
	}
	if !bareFails() {
		t.Fatal("mTLS 下不带客户端证书的连接全程成功，ca_path 没落到线上")
	}

	withCert := dialWS(t, "mtls handshake with client cert", "wss://"+addr+"/mtls", wssDialer(pki, true))
	defer withCert.Close()
	if got := exchangeWS(t, withCert, "mtls"); got != "mtls" {
		t.Fatalf("echo over mTLS = %q, want mtls", got)
	}
}
