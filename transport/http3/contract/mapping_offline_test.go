package contract

// mapping_offline_test.go —— server.http3 映射行的线上判据
// （真 UDP 监听、真 QUIC 握手、真 HTTP 往返；「offline」指不需要任何外部后端）。
//
// 为什么要这一层：contract_test.go 只证明「Option 被追加」，证明不了
// 「契约值落到了 socket 上」。§7.14 那一族的教训正是构造期绿、线上没生效。
//
// 覆盖的行：
//   - addr            → 契约地址上真的能完成一次 HTTP/3 往返（含业务 Option 覆盖落点）；
//   - 无 tls          → 实现自签证书仍能把请求服务完（QUIC 没有「关掉 TLS」这一项）；
//   - tls.file        → 服务端出示的是契约证书：按它校验通过，按别的根校验失败；
//   - tls.config      → 内联 PEM 同源；
//   - tls.file.ca     → 双向：不带客户端证书失败、带上即通（ca 不是摆设）。
//
// 校验姿势口径：只有「契约没配 tls」那一行用 InsecureSkipVerify——证书是实现
// 自签的，本机不可能有可信根。配了 tls 的三行一律真实校验（RootCAs 只认契约证书）。

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	quichttp3 "github.com/quic-go/quic-go/http3"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport/http3"
)

const (
	testBody  = "h3-pong"
	testWait  = 8 * time.Second
	h3Path    = "/ping"
	localhost = "127.0.0.1"
)

// runH3 走 Provider 构造并 Start（后台），返回产物。路由经 after-build 注册，
// 故必须在监听建立前完成——这正是 Provider 内 afterBuild 的执行时点。
func runH3(t *testing.T, opts []Option, cfg *bootstrapv1.Server) *http3.Server {
	t.Helper()
	opts = append([]Option{WithAfterBuild(func(_ context.Context, s *http3.Server) error {
		s.HandleFunc(h3Path, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, testBody)
		})
		return nil
	})}, opts...)

	srv, cleanup, err := Provider(opts...)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil")
	}
	hsrv, ok := srv.(*http3.Server)
	if !ok {
		t.Fatalf("srv = %T, want *http3.Server", srv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- hsrv.Start(ctx) }()
	t.Cleanup(func() {
		_ = hsrv.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("http3 Start 未在关闭后返回（残留监听 goroutine）")
		}
	})
	return hsrv
}

// getH3 用 quic-go 的 HTTP/3 Transport 发一次 GET（真实 TLS 校验按 cfg）。
func getH3(t *testing.T, desc, url string, tlsCfg *tls.Config) (int, string) {
	t.Helper()
	transport := &quichttp3.Transport{TLSClientConfig: tlsCfg}
	t.Cleanup(func() { _ = transport.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("%s: new request: %v", desc, err)
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		t.Fatalf("%s: GET %s: %v", desc, url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s: read body: %v", desc, err)
	}
	return resp.StatusCode, string(body)
}

// mustFailH3 请求必须失败（证书不被信任 / mTLS 缺客户端证书 / 地址没人听）。
func mustFailH3(t *testing.T, desc, url string, tlsCfg *tls.Config) {
	t.Helper()
	transport := &quichttp3.Transport{TLSClientConfig: tlsCfg}
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("%s: new request: %v", desc, err)
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("%s: 请求意外成功（HTTP %s）", desc, resp.Status)
	}
	t.Logf("%s: rejected as expected: %v", desc, err)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", localhost+":0")
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
	return fmt.Sprintf("%s:%d", localhost, freePort(t))
}

// --- addr 行 ---------------------------------------------------------------

// TestMappingOffline_AddrReachesWireNoTlsSection 契约 addr 上真的完成一次 HTTP/3
// 往返；tls 段缺席时实现自签证书仍服务得动（QUIC 无明文选项）。
func TestMappingOffline_AddrReachesWireNoTlsSection(t *testing.T) {
	addr := loopbackAddr(t)
	hsrv := runH3(t, nil, &bootstrapv1.Server{Http3: &bootstrapv1.Server_Http3{Addr: addr}})
	if ep := hsrv.Endpoint(); ep != addr {
		t.Fatalf("Endpoint() = %q, want %s", ep, addr)
	}

	// 自签证书不可能有本机可信根 → 这一行只能跳过校验（判的是 addr 落点，不是证书）。
	code, body := getH3(t, "GET over self-signed h3", "https://"+addr+h3Path,
		&tls.Config{InsecureSkipVerify: true})
	if code != http.StatusOK || body != testBody {
		t.Fatalf("(code, body) = (%d, %q), want (200, %q)", code, body, testBody)
	}
}

// TestMappingOffline_BusinessOptionOverridesAddrOnWire 业务 Option 覆盖契约 addr
// 之后，服务落在 Option 的地址上；契约地址不再有人应答。
func TestMappingOffline_BusinessOptionOverridesAddrOnWire(t *testing.T) {
	contractAddr := loopbackAddr(t)
	optionAddr := loopbackAddr(t)
	hsrv := runH3(t,
		[]Option{WithServerOptions(http3.WithAddress(optionAddr))},
		&bootstrapv1.Server{Http3: &bootstrapv1.Server_Http3{Addr: contractAddr}})
	if ep := hsrv.Endpoint(); ep != optionAddr {
		t.Fatalf("Endpoint() = %q, want override %s", ep, optionAddr)
	}

	code, body := getH3(t, "GET at override addr", "https://"+optionAddr+h3Path,
		&tls.Config{InsecureSkipVerify: true})
	if code != http.StatusOK || body != testBody {
		t.Fatalf("(code, body) = (%d, %q), want (200, %q)", code, body, testBody)
	}

	mustFailH3(t, "契约地址应无人应答", "https://"+contractAddr+h3Path,
		&tls.Config{InsecureSkipVerify: true})
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
		Subject:               pkix.Name{CommonName: "bald-http3-contract-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP(localhost)},
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

// trustPoolOf 只认这张证书（ServerName 走 IP SAN）。
func trustPoolOf(pki testPKI, withClientCert bool) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(pki.cert)
	cfg := &tls.Config{RootCAs: pool, ServerName: localhost, MinVersion: tls.VersionTLS13}
	if withClientCert {
		cert, err := tls.X509KeyPair(pki.certPEM, pki.keyPEM)
		if err != nil {
			panic("X509KeyPair: " + err.Error())
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg
}

// unrelatedPool 换一个与之无关的自签证书作根——服务端出示契约证书时必然校验失败。
func unrelatedPool(t *testing.T, withClientCert bool) (*tls.Config, testPKI) {
	other := newTestPKI(t)
	return trustPoolOf(other, withClientCert), other
}

// TestMappingOffline_TlsFileCertServed 契约 file 分支的证书真的被服务端出示：
// 按契约证书校验通过，按另一张根校验失败（不是实现自签那张在顶上）。
func TestMappingOffline_TlsFileCertServed(t *testing.T) {
	pki := newTestPKI(t)
	certPath, keyPath := writePKI(t, pki)
	addr := loopbackAddr(t)

	runH3(t, nil, &bootstrapv1.Server{
		Http3: &bootstrapv1.Server_Http3{
			Addr: addr,
			Tls:  &bootstrapv1.Server_TLS{File: &bootstrapv1.Server_TLS_File{CertPath: &certPath, KeyPath: &keyPath}},
		},
	})

	code, body := getH3(t, "GET with contract cert as root", "https://"+addr+h3Path, trustPoolOf(pki, false))
	if code != http.StatusOK || body != testBody {
		t.Fatalf("(code, body) = (%d, %q), want (200, %q)", code, body, testBody)
	}

	unrelated, _ := unrelatedPool(t, false)
	mustFailH3(t, "另一张根不该通过校验", "https://"+addr+h3Path, unrelated)
}

// TestMappingOffline_TlsConfigPemServed 契约 config 分支（内联 PEM）同源生效。
func TestMappingOffline_TlsConfigPemServed(t *testing.T) {
	pki := newTestPKI(t)
	addr := loopbackAddr(t)

	runH3(t, nil, &bootstrapv1.Server{
		Http3: &bootstrapv1.Server_Http3{
			Addr: addr,
			Tls:  &bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{CertPem: pki.certPEM, KeyPem: pki.keyPEM}},
		},
	})

	code, body := getH3(t, "GET with inline-pem cert as root", "https://"+addr+h3Path, trustPoolOf(pki, false))
	if code != http.StatusOK || body != testBody {
		t.Fatalf("(code, body) = (%d, %q), want (200, %q)", code, body, testBody)
	}

	unrelated, _ := unrelatedPool(t, false)
	mustFailH3(t, "另一张根不该通过校验（inline pem）", "https://"+addr+h3Path, unrelated)
}

// TestMappingOffline_TlsCaEnablesMutualAuth 契约 ca 分支不是摆设：
// 服务端按 ca 池要求客户端证书——裸客户端失败，带上证书才通。
func TestMappingOffline_TlsCaEnablesMutualAuth(t *testing.T) {
	pki := newTestPKI(t)
	certPath, keyPath := writePKI(t, pki)
	caPath := filepath.Join(filepath.Dir(certPath), "ca.crt")
	if err := os.WriteFile(caPath, pki.certPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	addr := loopbackAddr(t)

	runH3(t, nil, &bootstrapv1.Server{
		Http3: &bootstrapv1.Server_Http3{
			Addr: addr,
			Tls: &bootstrapv1.Server_TLS{File: &bootstrapv1.Server_TLS_File{
				CertPath: &certPath, KeyPath: &keyPath, CaPath: &caPath,
			}},
		},
	})

	// 就绪探活（带客户端证书），不消耗判据。
	waitUntilReady(t, "https://"+addr+h3Path, trustPoolOf(pki, true))

	mustFailH3(t, "mTLS 下不带客户端证书", "https://"+addr+h3Path, trustPoolOf(pki, false))

	code, body := getH3(t, "mTLS with client cert", "https://"+addr+h3Path, trustPoolOf(pki, true))
	if code != http.StatusOK || body != testBody {
		t.Fatalf("(code, body) = (%d, %q), want (200, %q)", code, body, testBody)
	}
}

// waitUntilReady 轮询直到服务端应答（Start 异步建 UDP 监听）。
func waitUntilReady(t *testing.T, url string, tlsCfg *tls.Config) {
	t.Helper()
	transport := &quichttp3.Transport{TLSClientConfig: tlsCfg}
	defer transport.Close()
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		var doErr error
		if err == nil {
			resp, err2 := (&http.Client{Transport: transport}).Do(req)
			if err2 == nil {
				_ = resp.Body.Close()
			}
			doErr = err2
		} else {
			doErr = err
		}
		cancel()
		if doErr == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("HTTP/3 服务未在 %s 内就绪: %s", testWait, url)
}
