package contract

// mapping_offline_test.go —— server.tcp 映射行的线上判据（真监听、真握手、真往返）。
//
// 为什么要这一层：contract_test.go 只证明「Option 被追加」，证明不了
// 「契约值落到了 socket 上」。§7.14 那一族的教训正是构造期绿、线上没生效。
//
// 覆盖的行：
//   - addr          → 契约端口接受连接并按帧回显（含业务 Option 覆盖端口的线上证据）；
//   - tls.file      → 服务端出示的就是契约里那张证书（客户端按它校验通过）；
//                     同端口收明文不再回显（TLS 真开着，没有静默回落）；
//   - tls.config    → 内联 PEM 同源（K8s Secret 挂载路径）；
//   - tls.file.ca   → 双向：无客户端证书握手失败、带上即成功（ca 不是摆设）。
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

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport/tcp"
)

const (
	testMsgType tcp.NetMessageType = 7
	testWait    time.Duration      = 5 * time.Second
)

// --- 线上判据用的最小协议夹具 -------------------------------------------
//
// tcp 的帧格式（message.go）：4 字节 Little-Endian uint32 类型 + 原样 payload。
// 处理器以 nil Creator 注册 → 载荷按原始字节递交（不经 codec），
// 因此这条判据不依赖任何编解码器注册。

func encodeFrame(msgType tcp.NetMessageType, body string) []byte {
	frame := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(frame, uint32(msgType))
	copy(frame[4:], body)
	return frame
}

// echoOption：after-build 里登记回显处理器（证明接线时机早于监听建立）。
func echoOption() Option {
	return WithAfterBuild(func(_ context.Context, s *tcp.Server) error {
		s.RegisterMessageHandler(testMsgType, func(sid tcp.SessionID, payload tcp.NetMessagePayload) error {
			raw, ok := payload.([]byte)
			if !ok {
				return fmt.Errorf("payload = %T, want []byte (nil Creator 应原样递交)", payload)
			}
			return s.SendRawData(sid, raw)
		}, nil)
		return nil
	})
}

// runTcp 走 Provider 构造并 Start（后台），返回产物与其 Endpoint。
func runTcp(t *testing.T, opts []Option, cfg *bootstrapv1.Server) (*tcp.Server, string) {
	t.Helper()
	srv, cleanup, err := Provider(opts...)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil")
	}
	ts, ok := srv.(*tcp.Server)
	if !ok {
		t.Fatalf("srv = %T, want *tcp.Server", srv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ts.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("tcp Start returned %v, want nil or context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("tcp Start 未在取消后返回（残留监听 goroutine）")
		}
	})
	return ts, ts.Endpoint()
}

// exchange 发一帧、读一次回包（读到的即处理器 SendRawData 的原始字节）。
func exchange(t *testing.T, conn net.Conn, body string) string {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(testWait)); err != nil {
		t.Fatalf("SetWriteDeadline: %v", err)
	}
	if _, err := conn.Write(encodeFrame(testMsgType, body)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(testWait)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	return string(buf[:n])
}

// dialUntilReady 轮询拨号直到成功或超时（Start 异步建监听）。
func dialUntilReady(dial func() (net.Conn, error)) (net.Conn, error) {
	deadline := time.Now().Add(testWait)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := dial()
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	return nil, lastErr
}

// mustDial 拨不通即判红。
func mustDial(t *testing.T, desc string, dial func() (net.Conn, error)) net.Conn {
	t.Helper()
	conn, err := dialUntilReady(dial)
	if err != nil {
		t.Fatalf("%s: %v", desc, err)
	}
	return conn
}

// tlsHandshakeWait 握手单独有界，且必须明显短于 dialUntilReady 的窗口——
// 窗口内至少还能重试一次，否则这条腿只剩「等一轮」而没有轮询语义。
const tlsHandshakeWait = 2 * time.Second

// tlsDial 拨号 + 有界握手。
//
// 为什么不用 tls.Dial：tls 段被静默摘掉时（变异验活里就是这一条），被测端口
// 在明文上永远不回 ServerHello，无界 tls.Dial 会挂到包级 -timeout——判红必须是
// 被点名的 FAIL，不能靠 panic: test timed out（那会把同包其它腿一起打死，
// 真因也被埋掉）。
func tlsDial(addr string, cfg *tls.Config) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", addr, testWait)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(tlsHandshakeWait)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	tc := tls.Client(conn, cfg)
	if err := tc.Handshake(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	// 握手后的读写由各腿自己带截止期（exchange / 探测分支），这里恢复为无界。
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tc, nil
}

// freePort 取一个刚释放的本地端口（bind→close→交给被测服务）。
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

// --- addr 行 ---------------------------------------------------------------

// TestMappingOffline_AddrReachesWire 契约 addr 上的端口真的在听，且帧能进出。
func TestMappingOffline_AddrReachesWire(t *testing.T) {
	addr := loopbackAddr(t)
	ts, ep := runTcp(t, []Option{echoOption()}, &bootstrapv1.Server{
		Tcp: &bootstrapv1.Server_Tcp{Addr: addr},
	})
	if ep != "tcp://"+addr {
		t.Fatalf("Endpoint() = %q, want tcp://%s", ep, addr)
	}

	conn := mustDial(t, "dial contract addr "+addr, func() (net.Conn, error) {
		return net.DialTimeout("tcp", addr, testWait)
	})
	defer conn.Close()

	if got := exchange(t, conn, "ping"); got != "ping" {
		t.Fatalf("echo = %q, want ping（after-build 登记的处理器没被调用？）", got)
	}
	if n := ts.SessionCount(); n != 1 {
		t.Fatalf("SessionCount() = %d, want 1（连接没进会话管理？）", n)
	}
}

// TestMappingOffline_BusinessOptionOverridesAddrOnWire 业务 Option 覆盖契约 addr
// 之后，线上落点是 Option 的地址、契约地址不再有人听（顺序判据的线上版）。
func TestMappingOffline_BusinessOptionOverridesAddrOnWire(t *testing.T) {
	contractAddr := loopbackAddr(t)
	optionAddr := loopbackAddr(t)
	_, ep := runTcp(t,
		[]Option{echoOption(), WithServerOptions(tcp.WithAddress(optionAddr))},
		&bootstrapv1.Server{Tcp: &bootstrapv1.Server_Tcp{Addr: contractAddr}})
	if ep != "tcp://"+optionAddr {
		t.Fatalf("Endpoint() = %q, want %q", ep, optionAddr)
	}

	conn := mustDial(t, "dial override addr "+optionAddr, func() (net.Conn, error) {
		return net.DialTimeout("tcp", optionAddr, testWait)
	})
	defer conn.Close()
	if got := exchange(t, conn, "override"); got != "override" {
		t.Fatalf("echo = %q, want override", got)
	}

	// 契约地址不该在听：同一进程内没人监听它 → 连接立即被拒。
	if c, err := net.DialTimeout("tcp", contractAddr, 500*time.Millisecond); err == nil {
		_ = c.Close()
		t.Fatalf("契约 addr %s 仍可连接，业务 Option 没覆盖落点", contractAddr)
	}
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
		SerialNumber:          big.NewInt(20261105),
		Subject:               pkix.Name{CommonName: "bald-tcp-contract-test"},
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

// clientTLS 造客户端配置：RootCAs 只认这张自签证书，ServerName 走 IP SAN 校验。
func clientTLS(t *testing.T, pki testPKI, withClientCert bool) *tls.Config {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(pki.cert)
	cfg := &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	if withClientCert {
		cert, err := tls.X509KeyPair(pki.certPEM, pki.keyPEM)
		if err != nil {
			t.Fatalf("X509KeyPair: %v", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg
}

// TestMappingOffline_TlsFileCertServed 契约 file 分支的证书真的被服务端出示：
// 客户端只用这张证书做校验，握手即通过（换成别的证书不可能过）。
// 同端口收明文不得回显——证明 TLS 真开着，不是「配了 tls 却仍走明文」。
func TestMappingOffline_TlsFileCertServed(t *testing.T) {
	pki := newTestPKI(t)
	certPath, keyPath := writePKI(t, pki)
	addr := loopbackAddr(t)

	runTcp(t, []Option{echoOption()}, &bootstrapv1.Server{
		Tcp: &bootstrapv1.Server_Tcp{
			Addr: addr,
			Tls: &bootstrapv1.Server_TLS{File: &bootstrapv1.Server_TLS_File{
				CertPath: &certPath, KeyPath: &keyPath,
			}},
		},
	})

	conn := mustDial(t, "tls dial "+addr, func() (net.Conn, error) {
		return tlsDial(addr, clientTLS(t, pki, false))
	})
	defer conn.Close()
	tc := conn.(*tls.Conn)
	if len(tc.ConnectionState().PeerCertificates) == 0 {
		t.Fatal("peer did not present a certificate")
	}
	if got := tc.ConnectionState().PeerCertificates[0].Subject.CommonName; got != "bald-tcp-contract-test" {
		t.Fatalf("peer CN = %q, want bald-tcp-contract-test（服务端出示的不是契约里那张证书）", got)
	}
	if got := exchange(t, tc, "tls-ping"); got != "tls-ping" {
		t.Fatalf("echo over TLS = %q, want tls-ping", got)
	}

	// 明文探测：同端口发裸帧，不得拿到回显。
	plain, err := net.DialTimeout("tcp", addr, testWait)
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	defer plain.Close()
	if err := plain.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if _, err := plain.Write(encodeFrame(testMsgType, "plaintext")); err != nil {
		t.Fatalf("plain write: %v", err)
	}
	buf := make([]byte, 64)
	n, readErr := plain.Read(buf)
	if readErr == nil && string(buf[:n]) == "plaintext" {
		t.Fatal("明文连接拿到了回显：tls 段没落到线上（静默回落明文）")
	}
}

// TestMappingOffline_TlsConfigPemServed 契约 config 分支（内联 PEM）同源生效。
func TestMappingOffline_TlsConfigPemServed(t *testing.T) {
	pki := newTestPKI(t)
	addr := loopbackAddr(t)

	runTcp(t, []Option{echoOption()}, &bootstrapv1.Server{
		Tcp: &bootstrapv1.Server_Tcp{
			Addr: addr,
			Tls: &bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{
				CertPem: pki.certPEM, KeyPem: pki.keyPEM,
			}},
		},
	})

	conn := mustDial(t, "tls dial (inline pem) "+addr, func() (net.Conn, error) {
		return tlsDial(addr, clientTLS(t, pki, false))
	})
	defer conn.Close()
	if got := exchange(t, conn, "inline-pem"); got != "inline-pem" {
		t.Fatalf("echo = %q, want inline-pem", got)
	}
}

// TestMappingOffline_TlsCaEnablesMutualAuth 契约 ca 分支不是摆设：
// 无客户端证书握手必须失败，带上才通（服务端按 ca 池要求并校验客户端证书）。
func TestMappingOffline_TlsCaEnablesMutualAuth(t *testing.T) {
	pki := newTestPKI(t)
	certPath, keyPath := writePKI(t, pki)
	caPath := filepath.Join(filepath.Dir(certPath), "ca.crt")
	if err := os.WriteFile(caPath, pki.certPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	addr := loopbackAddr(t)

	runTcp(t, []Option{echoOption()}, &bootstrapv1.Server{
		Tcp: &bootstrapv1.Server_Tcp{
			Addr: addr,
			Tls: &bootstrapv1.Server_TLS{File: &bootstrapv1.Server_TLS_File{
				CertPath: &certPath, KeyPath: &keyPath, CaPath: &caPath,
			}},
		},
	})

	// 服务端要求客户端证书：不带证书的连接要么拨不通、要么握手报错。
	bareErr := func() error {
		conn, err := tlsDial(addr, clientTLS(t, pki, false))
		if err != nil {
			return err
		}
		defer conn.Close()
		// 握手在客户端已完成；服务端的「要求证书」报错在读写时暴露。
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write(encodeFrame(testMsgType, "should-not-pass")); err != nil {
			return err
		}
		buf := make([]byte, 64)
		if _, err := conn.Read(buf); err != nil {
			return err
		}
		return nil
	}
	// 等监听就绪（用带证书的连接探活，不消耗判据）。
	ready := mustDial(t, "mtls ready "+addr, func() (net.Conn, error) {
		return tlsDial(addr, clientTLS(t, pki, true))
	})
	_ = ready.Close()

	if err := bareErr(); err == nil {
		t.Fatal("mTLS 下不带客户端证书的连接全程成功，ca_path 没落到线上")
	}

	// 带上客户端证书 → 握手成功且能往返。
	withCert := mustDial(t, "mtls dial with client cert "+addr, func() (net.Conn, error) {
		return tlsDial(addr, clientTLS(t, pki, true))
	})
	defer withCert.Close()
	if got := exchange(t, withCert, "mtls"); got != "mtls" {
		t.Fatalf("echo over mTLS = %q, want mtls", got)
	}
}
