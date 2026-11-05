package contract

// mapping_offline_test.go —— server.graphql 映射行的线上判据
// （真监听、真请求、真证书；「offline」指不需要任何外部后端）。
//
// 为什么要这一层：contract_test.go 只证明「Option 被追加」，证明不了
// 「契约值落到了 socket / mux 上」。§7.14 那一族的教训正是构造期绿、线上没生效。
//
// 覆盖的行：
//   - addr        → 契约地址接受 HTTP 请求，after-build 登记的路由真的有人挂上；
//   - tls.file    → 服务端出示的就是契约证书；明文 http:// 探测握手不过（无静默回落）；
//   - tls.config  → 内联 PEM 同源，且 Endpoint() 方案切成 https；
//   - tls.file.ca → 双向：无客户端证书拿不到响应、带上即通（ca 不是摆设）；
//   - 业务 Option 覆盖 → WithTLSConfig 顶掉契约证书后，线上生效的是业务那张。
//
// 口径记录：本包的映射行只有 addr / tls 两行（契约 graphql 段就这两个字段）。
// schema 路由经 Handle(path, ExecutableSchema) 接，那是 gqlgen 的执行面，
// 其正确性归 gqlgen；契约侧要判的是「Start 之前登记的路由在契约地址上可达」，
// 用 HandleFunc 判即可——Handle 与 HandleFunc 落的是同一个 mux。

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	graphql "github.com/kalandramo/bald/transport/graphql"
)

const testWait time.Duration = 5 * time.Second

// runGQL 走 Provider 构造并 Start（后台），返回产物与其 Endpoint。
func runGQL(t *testing.T, opts []Option, cfg *bootstrapv1.Server) (*graphql.Server, string) {
	t.Helper()
	srv, cleanup, err := Provider(opts...)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil")
	}
	gsrv, ok := srv.(*graphql.Server)
	if !ok {
		t.Fatalf("srv = %T, want *graphql.Server", srv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gsrv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			// graphql 的 Start 在 ctx 取消分支返回 Shutdown 的结果；
			// 若 Serve 先因监听关闭而返回，则可能是 ErrServerClosed——两者都算正常收尾。
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("graphql Start returned %v, want nil / context.Canceled / ErrServerClosed", err)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("graphql Start 未在取消后返回（残留监听 goroutine）")
		}
	})
	return gsrv, gsrv.Endpoint()
}

// healthRouteOption：after-build 里登记 /health 路由（证明接线时机早于监听建立）。
func healthRouteOption(body string) Option {
	return WithAfterBuild(func(_ context.Context, s *graphql.Server) error {
		s.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		})
		return nil
	})
}

func httpClient(cfg *tls.Config) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = cfg
	return &http.Client{Transport: tr, Timeout: testWait}
}

// get 在就绪轮询里发一次 GET（Start 异步建监听）。
func get(t *testing.T, desc, url string, client *http.Client) (string, int) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		defer resp.Body.Close()
		raw, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			t.Fatalf("%s: read body: %v", desc, readErr)
		}
		return string(raw), resp.StatusCode
	}
	t.Fatalf("%s: get %s: %v", desc, url, lastErr)
	return "", 0
}

// mustNotServe 请求必须被拒：要么传输层报错，要么非 2xx。
//
// 为什么 400 也算拒绝：Go 的 TLS 监听收到明文 HTTP/1 请求时，会**回一个 400**
// （"Client sent an HTTP request to an HTTPS server"）而不是断连——客户端拿到的是
// 正常 HTTP 响应。判据要的判别点是「业务响应没在明文上被服务」，不是「有没有报错」：
// tls 段被静默摘掉时这里会拿到 200 + 正文，必须判红。
func mustNotServe(t *testing.T, desc, url string, client *http.Client, body string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Logf("%s: rejected with %v", desc, err)
		return
	}
	raw, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatalf("%s: read body: %v", desc, readErr)
	}
	if resp.StatusCode == http.StatusOK && string(raw) == body {
		t.Fatalf("%s: 明文路径上服务了业务响应 %q (%s)——契约的 tls 段没落到线上", desc, raw, url)
	}
	t.Logf("%s: rejected with HTTP %s: %q", desc, resp.Status, string(raw))
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

// --- addr 行 ---------------------------------------------------------------

// TestMappingOffline_AddrAndWiredRouteReachWire 契约 addr 上真的能取到
// after-build 登记的路由；未登记路径必须 404——路由落在契约那台服务器上，不是装饰。
func TestMappingOffline_AddrAndWiredRouteReachWire(t *testing.T) {
	addr := loopbackAddr(t)
	gsrv, ep := runGQL(t, []Option{healthRouteOption("ok")}, &bootstrapv1.Server{
		Graphql: &bootstrapv1.Server_Graphql{Addr: addr},
	})
	if ep != "http://"+addr {
		t.Fatalf("Endpoint() = %q, want http://%s", ep, addr)
	}

	body, code := get(t, "contract addr", "http://"+addr+"/health", httpClient(nil))
	if code != http.StatusOK || body != "ok" {
		t.Fatalf("GET /health = %d %q, want 200 ok（after-build 登记的路由没生效？）", code, body)
	}
	if got := gsrv.Addr(); got != addr {
		t.Fatalf("Addr() = %q, want %s", got, addr)
	}
	if _, code := get(t, "unregistered path", "http://"+addr+"/absent", httpClient(nil)); code != http.StatusNotFound {
		t.Fatalf("GET /absent status = %d, want 404", code)
	}
}

// TestMappingOffline_MiddlewareFromBusinessOptionReachesWire WithServerOptions 传入的
// 中间件在线上真的包裹了处理器（graphql 的中间件链在 Start 里定形，故必须在构造前）。
func TestMappingOffline_MiddlewareFromBusinessOptionReachesWire(t *testing.T) {
	addr := loopbackAddr(t)
	tagged := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Contract-Mw", "applied")
			next.ServeHTTP(w, r)
		})
	}
	runGQL(t, []Option{
		WithServerOptions(graphql.WithMiddleware(tagged)),
		healthRouteOption("mw"),
	}, &bootstrapv1.Server{Graphql: &bootstrapv1.Server_Graphql{Addr: addr}})

	deadline := time.Now().Add(testWait)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := httpClient(nil).Get("http://" + addr + "/health")
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("X-Contract-Mw"); got != "applied" {
			t.Fatalf("X-Contract-Mw = %q, want applied（业务 Option 没落到中间件链？）", got)
		}
		return
	}
	t.Fatalf("middleware leg: get: %v", lastErr)
}

// --- tls 行 ---------------------------------------------------------------

type testPKI struct {
	certPEM, keyPEM []byte
	cert            *x509.Certificate
	cn              string
}

// newTestPKI 自签一张 127.0.0.1 的证书（同时可作 CA 与客户端证书）。
func newTestPKI(t *testing.T, cn string) testPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(20261106),
		Subject:               pkix.Name{CommonName: cn},
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
		cn:      cn,
	}
}

// testPEMs 供 contract_test.go 的映射腿取一组 PEM（cert / key / ca 同源）。
func testPEMs(t *testing.T) (certPEM, keyPEM, caPEM []byte) {
	t.Helper()
	pki := newTestPKI(t, "bald-graphql-contract-test")
	return pki.certPEM, pki.keyPEM, pki.certPEM
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

// clientTLS 造客户端：RootCAs 只认这张自签证书，ServerName 走 IP SAN 校验。
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

// peerCN 取服务端出示的证书 CN（必须是 TLS 连接）。
//
// 轮询到就绪为止：Start 在后台 goroutine 里建监听，一次 tlsDial 会撞在
// connection refused 上（0.00s 判红是假红，不是判据）。
func peerCN(t *testing.T, addr string, cfg *tls.Config) string {
	t.Helper()
	deadline := time.Now().Add(testWait)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := tlsDial(addr, cfg)
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		defer conn.Close()
		state := conn.(*tls.Conn).ConnectionState()
		if len(state.PeerCertificates) == 0 {
			t.Fatal("peer did not present a certificate")
		}
		return state.PeerCertificates[0].Subject.CommonName
	}
	t.Fatalf("tls dial %s: %v", addr, lastErr)
	return ""
}

// tlsHandshakeWait 握手单独有界，且必须明显短于各腿的轮询窗口——窗口内至少还能
// 重试一次，否则这条腿只剩「等一轮」而没有轮询语义。
const tlsHandshakeWait = 2 * time.Second

// tlsDial 拨号 + 有界握手。
//
// 为什么不用 tls.Dial：tls 段被静默摘掉时（变异验活里就是这一条），被测端口在
// 明文上永远不回 ServerHello，无界 tls.Dial 会挂到包级 -timeout——判红必须是被
// 点名的 FAIL，不能靠 panic: test timed out（那会把同包其它腿一起打死，真因也被埋掉）。
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
	// 握手后的读写由各腿自己带截止期，这里恢复为无界。
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tc, nil
}

// TestMappingOffline_TlsFileCertServed 契约 file 分支的证书真的被服务端出示：
// 客户端只用这张证书做校验，握手即通过；明文 http:// 探测不过（无静默回落）。
func TestMappingOffline_TlsFileCertServed(t *testing.T) {
	pki := newTestPKI(t, "bald-graphql-contract-test")
	certPath, keyPath := writePKI(t, pki)
	addr := loopbackAddr(t)

	runGQL(t, []Option{healthRouteOption("tls-ok")}, &bootstrapv1.Server{
		Graphql: &bootstrapv1.Server_Graphql{
			Addr: addr,
			Tls: &bootstrapv1.Server_TLS{File: &bootstrapv1.Server_TLS_File{
				CertPath: &certPath, KeyPath: &keyPath,
			}},
		},
	})

	if got := peerCN(t, addr, clientTLS(pki, false)); got != pki.cn {
		t.Fatalf("peer CN = %q, want %s（服务端出示的不是契约里那张证书）", got, pki.cn)
	}
	body, code := get(t, "https over contract cert", "https://"+addr+"/health", httpClient(clientTLS(pki, false)))
	if code != http.StatusOK || body != "tls-ok" {
		t.Fatalf("GET over TLS = %d %q, want 200 tls-ok", code, body)
	}

	mustNotServe(t, "明文打到 TLS 端口", "http://"+addr+"/health", httpClient(nil), "tls-ok")
}

// TestMappingOffline_TlsConfigPemServed 契约 config 分支（内联 PEM）同源生效，
// 且 Endpoint() 的方案随 tls 切成 https。
func TestMappingOffline_TlsConfigPemServed(t *testing.T) {
	pki := newTestPKI(t, "bald-graphql-inline-pem")
	addr := loopbackAddr(t)

	_, ep := runGQL(t, []Option{healthRouteOption("pem")}, &bootstrapv1.Server{
		Graphql: &bootstrapv1.Server_Graphql{
			Addr: addr,
			Tls: &bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{
				CertPem: pki.certPEM, KeyPem: pki.keyPEM,
			}},
		},
	})
	if ep != "https://"+addr {
		t.Fatalf("Endpoint() = %q, want https://%s（tls 段没切方案）", ep, addr)
	}

	body, code := get(t, "https (inline pem)", "https://"+addr+"/health", httpClient(clientTLS(pki, false)))
	if code != http.StatusOK || body != "pem" {
		t.Fatalf("GET = %d %q, want 200 pem", code, body)
	}
}

// TestMappingOffline_TlsCaEnablesMutualAuth 契约 ca 分支不是摆设：
// 服务端按 ca 池要求客户端证书——裸客户端拿不到响应，带上证书才通。
func TestMappingOffline_TlsCaEnablesMutualAuth(t *testing.T) {
	pki := newTestPKI(t, "bald-graphql-mtls")
	certPath, keyPath := writePKI(t, pki)
	caPath := filepath.Join(filepath.Dir(certPath), "ca.crt")
	if err := os.WriteFile(caPath, pki.certPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	addr := loopbackAddr(t)

	runGQL(t, []Option{healthRouteOption("mtls")}, &bootstrapv1.Server{
		Graphql: &bootstrapv1.Server_Graphql{
			Addr: addr,
			Tls: &bootstrapv1.Server_TLS{File: &bootstrapv1.Server_TLS_File{
				CertPath: &certPath, KeyPath: &keyPath, CaPath: &caPath,
			}},
		},
	})

	// 裸客户端：Go 的客户端在收到 CertificateRequest 后无证书可交，握手即失败。
	mustNotServe(t, "mTLS 下不带客户端证书", "https://"+addr+"/health", httpClient(clientTLS(pki, false)), "mtls")

	body, code := get(t, "mTLS with client cert", "https://"+addr+"/health", httpClient(clientTLS(pki, true)))
	if code != http.StatusOK || body != "mtls" {
		t.Fatalf("GET over mTLS = %d %q, want 200 mtls", code, body)
	}
}

// TestMappingOffline_BusinessOptionOverridesTls 业务 WithTLSConfig 覆盖契约证书之后，
// 线上出示的是业务那张，契约那张不再被信任（顺序判据的线上版）。
func TestMappingOffline_BusinessOptionOverridesTls(t *testing.T) {
	contractPKI := newTestPKI(t, "bald-graphql-contract-cert")
	businessPKI := newTestPKI(t, "bald-graphql-business-cert")
	addr := loopbackAddr(t)

	businessCfg, err := tls.X509KeyPair(businessPKI.certPEM, businessPKI.keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	runGQL(t, []Option{
		healthRouteOption("override"),
		WithServerOptions(graphql.WithTLSConfig(&tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{businessCfg},
		})),
	}, &bootstrapv1.Server{
		Graphql: &bootstrapv1.Server_Graphql{
			Addr: addr,
			Tls: &bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{
				CertPem: contractPKI.certPEM, KeyPem: contractPKI.keyPEM,
			}},
		},
	})

	if got := peerCN(t, addr, clientTLS(businessPKI, false)); got != businessPKI.cn {
		t.Fatalf("peer CN = %q, want %s（业务 Option 没覆盖契约证书）", got, businessPKI.cn)
	}
	mustNotServe(t, "契约证书应已被业务 Option 覆盖", "https://"+addr+"/health", httpClient(clientTLS(contractPKI, false)), "override")
}
