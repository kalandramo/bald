package tlsconf

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/bconf/tlsconf/tlstest"
)

// 证书材料由 tlstest 现签（同 transport/bootstrap 的 TLS 用例共用），本包只测
// 「契约 tls 段 → tls.Config」的翻译：字段归属、缺料报错、真实握手。

// mustCerts 签一套测试证书；fileTLS 另把材料落进临时目录以覆盖 file 来源。
func mustCerts(t *testing.T) *tlstest.Certs {
	t.Helper()
	c, err := tlstest.Generate()
	if err != nil {
		t.Fatalf("tlstest.Generate: %v", err)
	}
	return c
}

// fileTLS 把材料落进临时目录，返回契约的 file 段（覆盖 file 来源）。
func fileTLS(t *testing.T, c *tlstest.Certs) *bootstrapv1.Server_TLS {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	return &bootstrapv1.Server_TLS{File: &bootstrapv1.Server_TLS_File{
		CertPath: ptr(write("cert.pem", c.ServerCert)),
		KeyPath:  ptr(write("key.pem", c.ServerKey)),
		CaPath:   ptr(write("ca.pem", c.CA)),
	}}
}

func ptr(s string) *string { return &s }

// TestNilMeansNoTLS 契约缺 tls 段 → (nil, nil)：调用方据此走明文，不是错误。
func TestNilMeansNoTLS(t *testing.T) {
	if cfg, err := Server(nil); cfg != nil || err != nil {
		t.Fatalf("Server(nil) = (%v, %v), want (nil, nil)", cfg, err)
	}
	if cfg, err := Client(nil); cfg != nil || err != nil {
		t.Fatalf("Client(nil) = (%v, %v), want (nil, nil)", cfg, err)
	}
}

// TestServerCertSources 断言 file 与 config 两种来源产出等价配置，且 ca 段
// 在双向校验下落到 ClientCAs（服务端的对端是客户端，不是 RootCAs）。
func TestServerCertSources(t *testing.T) {
	c := mustCerts(t)
	for name, tlsCfg := range map[string]*bootstrapv1.Server_TLS{
		"file":   fileTLS(t, c),
		"config": c.ServerTLS(),
	} {
		got, err := Server(tlsCfg)
		if err != nil {
			t.Fatalf("%s: Server: %v", name, err)
		}
		if len(got.Certificates) != 1 {
			t.Fatalf("%s: certificates = %d, want 1", name, len(got.Certificates))
		}
		if got.MinVersion != tls.VersionTLS12 {
			t.Errorf("%s: MinVersion = %d, want TLS1.2", name, got.MinVersion)
		}
		if got.ClientCAs == nil {
			t.Fatalf("%s: 配了 ca 应启用双向校验，ClientCAs 却是 nil", name)
		}
		if got.ClientAuth != tls.RequireAndVerifyClientCert {
			t.Errorf("%s: ClientAuth = %v, want RequireAndVerifyClientCert", name, got.ClientAuth)
		}
		if got.RootCAs != nil {
			t.Errorf("%s: 服务端不得设置 RootCAs（校验客户端用 ClientCAs）", name)
		}
	}
}

// TestServerWithoutCA 未配 ca → 单向 TLS：ClientAuth 不得被设为要求客户端证书。
func TestServerWithoutCA(t *testing.T) {
	c := mustCerts(t)
	got, err := Server(&bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{
		CertPem: c.ServerCert, KeyPem: c.ServerKey,
	}})
	if err != nil {
		t.Fatalf("Server: %v", err)
	}
	if got.ClientAuth != tls.NoClientCert {
		t.Fatalf("ClientAuth = %v, want NoClientCert（未配 ca）", got.ClientAuth)
	}
}

// TestServerIncompleteMaterial 证书材料不完整必须启动期报错。「写了 tls 段却
// 配不出证书」若静默退回明文，就是 §6.5 的成因——这里断言它只能报错。
func TestServerIncompleteMaterial(t *testing.T) {
	c := mustCerts(t)
	cases := map[string]*bootstrapv1.Server_TLS{
		"空段":        {File: &bootstrapv1.Server_TLS_File{}},
		"有证书无私钥":    {Config: &bootstrapv1.Server_TLS_Config{CertPem: c.ServerCert}},
		"有私钥无证书":    {Config: &bootstrapv1.Server_TLS_Config{KeyPem: c.ServerKey}},
		"证书非法 PEM":  {Config: &bootstrapv1.Server_TLS_Config{CertPem: []byte("not a pem"), KeyPem: c.ServerKey}},
		"私钥非法 PEM":  {Config: &bootstrapv1.Server_TLS_Config{CertPem: c.ServerCert, KeyPem: []byte("not a pem")}},
		"ca 非法 PEM": {Config: &bootstrapv1.Server_TLS_Config{CertPem: c.ServerCert, KeyPem: c.ServerKey, CaPem: []byte("not a pem")}},
		"文件不存在": {File: &bootstrapv1.Server_TLS_File{
			CertPath: ptr(filepath.Join(t.TempDir(), "missing.pem")),
		}},
	}
	for name, tlsCfg := range cases {
		if got, err := Server(tlsCfg); err == nil {
			t.Errorf("%s: Server 应报错，got cfg=%v", name, got)
		}
	}
}

// TestClientPoolSelection 断言 ca / 系统根 / 跳过校验三条路径的 RootCAs 归属，
// 以及客户端证书只在声明了 cert+key 时提供。
func TestClientPoolSelection(t *testing.T) {
	c := mustCerts(t)

	withCA, err := Client(c.ClientTLS())
	if err != nil {
		t.Fatalf("Client(ca): %v", err)
	}
	if withCA.RootCAs == nil {
		t.Fatal("ca 应成为 RootCAs（校验服务端链）")
	}
	if len(withCA.Certificates) != 1 {
		t.Errorf("配了 cert+key 应提供客户端证书，got %d", len(withCA.Certificates))
	}

	bare, err := Client(&bootstrapv1.Server_TLS{File: &bootstrapv1.Server_TLS_File{}})
	if err != nil {
		t.Fatalf("Client(bare): %v", err)
	}
	if bare.RootCAs == nil {
		t.Error("未声明 ca 且未跳过校验时应回退到系统根证书池")
	}
	if len(bare.Certificates) != 0 {
		t.Errorf("未声明客户端证书时不应提供，got %d", len(bare.Certificates))
	}

	skip, err := Client(&bootstrapv1.Server_TLS{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("Client(skip): %v", err)
	}
	if !skip.InsecureSkipVerify {
		t.Error("insecure_skip_verify 未透传")
	}
	if skip.RootCAs != nil {
		t.Error("显式跳过校验时不应再加载根证书池")
	}
}

// TestHandshake 用一次真实双向握手证明「契约 tls 段 → 可用 tls.Config」这条链
// 成立：mTLS 下带客户端证书的连接成功，且服务端也确认握手成功。
func TestHandshake(t *testing.T) {
	c := mustCerts(t)
	serverCfg, err := Server(c.ServerTLS())
	if err != nil {
		t.Fatalf("Server: %v", err)
	}
	clientCfg, err := Client(c.ClientTLS())
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	ln, handshakes := serveTLS(t, serverCfg)

	conn, err := tls.Dial("tcp", ln.Addr().String(), clientCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := conn.VerifyHostname("localhost"); err != nil {
		t.Errorf("VerifyHostname(localhost): %v", err)
	}
	if err := <-handshakes; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
}

// TestServerRejectsNoClientCert 断言 ca 段不只是配置成功：mTLS 服务端必须拒绝
// 没有客户端证书的连接。
//
// 断言点在服务端而非客户端：TLS 1.3 是 1-RTT，客户端发完自己的（空）证书就认为
// 握手成功，服务端的拒绝稍后才到——只断言 Dial 失败会得到恒真用例。
func TestServerRejectsNoClientCert(t *testing.T) {
	c := mustCerts(t)
	serverCfg, err := Server(c.ServerTLS())
	if err != nil {
		t.Fatalf("Server: %v", err)
	}
	ln, handshakes := serveTLS(t, serverCfg)

	noClientCert, err := Client(&bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{CaPem: c.CA}})
	if err != nil {
		t.Fatalf("Client(no cert): %v", err)
	}
	conn, err := tls.Dial("tcp", ln.Addr().String(), noClientCert)
	if err != nil {
		t.Logf("拒绝发生在客户端握手：%v", err)
		return
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("无客户端证书的连接读到了数据")
	}
	select {
	case serr := <-handshakes:
		if serr == nil {
			t.Fatal("服务端握手成功接受了无客户端证书的连接")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待服务端握手结果超时")
	}
}

// TestClientRejectsUntrustedServer 断言客户端只认 ca 池：另一套 CA 签发的
// 服务端证书（同一 SAN，不同密钥）必须被拒。
func TestClientRejectsUntrustedServer(t *testing.T) {
	c := mustCerts(t)
	serverCfg, err := Server(c.ServerTLS())
	if err != nil {
		t.Fatalf("Server: %v", err)
	}
	ln, _ := serveTLS(t, serverCfg)

	other := mustCerts(t)
	untrusted, err := Client(&bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{CaPem: other.CA}})
	if err != nil {
		t.Fatalf("Client(untrusted ca): %v", err)
	}
	if conn, err := tls.Dial("tcp", ln.Addr().String(), untrusted); err == nil {
		_ = conn.Close()
		t.Error("客户端接受了不在 ca 池里的服务端证书")
	}
}

// serveTLS 监听并握手每个连接。握手在服务端是惰性的（tls.Listener.Accept 只
// 包装连接、不做握手），必须显式触发才能看到 ca/ClientAuth 的拒绝结果。
func serveTLS(t *testing.T, serverCfg *tls.Config) (net.Listener, <-chan error) {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	handshakes := make(chan error, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // 监听器已关闭
			}
			go func() {
				defer conn.Close()
				handshakes <- conn.(*tls.Conn).HandshakeContext(context.Background())
			}()
		}
	}()
	// 关闭与等待放在同一个 cleanup：拆成两次注册会按 LIFO 先等 goroutine 退出，
	// 而监听器还开着 → 死锁。
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return ln, handshakes
}
