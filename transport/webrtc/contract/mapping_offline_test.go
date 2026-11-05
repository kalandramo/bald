package contract

// mapping_offline_test.go —— server.webrtc 映射行的线上判据
// （真监听、真信令请求、真证书；「offline」指不需要任何外部后端）。
//
// 为什么要这一层：contract_test.go 只证明「Option 被追加」，证明不了
// 「契约值落到了 socket / mux 上」。§7.14 那一族的教训正是构造期绿、线上没生效。
//
// 覆盖的行：
//   - addr    → 契约地址接受信令 POST（400 = 走进了 signalHandler 的 offer 分支）；
//   - path    → 契约路径挂上 mux，默认 /signal 让位（404）；
//   - network → 显式 tcp 在契约地址上服务成功（udp 等对 HTTP 信令面不适用，
//     判据只到「映射没把监听弄坏」）；
//   - tls     → 服务端出示契约证书，**且 Endpoint() 方案随协议切成 https**
//     （缺陷修复①的判据腿：硬编 http 的注册地址打不通 TLS 端口）；
//   - Use 接线时机 → NewServer **之后**（afterBuild）注册的中间件真的在服务链上
//     （缺陷修复②的判据腿：链在 init 定形后再也没重建过）。
//
// 口径记录：codec 行离线观测不到——它只作用于 DataChannel 打开后的消息编解码，
// 而信令面往返不经过它；故 codec 判在 contract_test 的「未注册即装配失败」与
// buildOpts 计数上。Origin/CORS/payloadType 等非契约能力经 WithServerOptions
// 注入，各留一条线上反证腿（见 Origin 腿）。

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
	"strings"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	webrtc "github.com/kalandramo/bald/transport/webrtc"
)

const testWait time.Duration = 5 * time.Second

// runWebRTC 走 Provider 构造并 Start（后台），等一次真实信令往返后返回。
//
// 就绪判据必须是一次**业务往返**而不是 TCP 拨通：listenAndEndpoint 先绑监听、
// Start 后置 running 再进 Serve ——拨通只证明 bind，此时 Stop 会因 running==false
// 直接空转返回，服务永久挂在 Serve 上（cleanup 假绿）。收到响应 ⇒ Serve 已在服务
// ⇒ running 已置位 ⇒ Stop 走 Shutdown 真正收口。
//
// 清理走 Stop 而不是 cancel：webrtc 的 Serve 不看 BaseContext 的取消，
// 只有 Shutdown 能让它返回（Start 会把 ErrServerClosed 映射成 nil）。
func runWebRTC(t *testing.T, opts []Option, cfg *bootstrapv1.Server, effPath string, clientCfg *tls.Config) (*webrtc.Server, string) {
	t.Helper()
	srv, cleanup, err := Provider(opts...)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil")
	}
	wsrv, ok := srv.(*webrtc.Server)
	if !ok {
		t.Fatalf("srv = %T, want *webrtc.Server", srv)
	}

	startDone := make(chan error, 1)
	go func() { startDone <- wsrv.Start(context.Background()) }()

	ep := wsrv.Endpoint()
	if ep == "" {
		t.Fatalf("unexpected empty endpoint")
	}

	// webrtc 的 Endpoint() 自带信令路径（url.URL{..., Path: s.path}），
	// 就绪探测直接打整个 ep；其它路径的腿从 TrimSuffix 出的 base 走。
	wantScheme := "http://"
	if cfg.GetWebrtc().GetTls() != nil {
		wantScheme = "https://"
	}
	if !strings.HasPrefix(ep, wantScheme) || !strings.HasSuffix(ep, effPath) {
		t.Fatalf("Endpoint() = %q, want %s<host:port>%s", ep, wantScheme, effPath)
	}
	base := strings.TrimSuffix(ep, effPath)
	body, code := postSignal(t, "readiness", base, effPath, httpClient(clientCfg), nil)
	if code == http.StatusForbidden {
		// 业务 WithCheckOrigin 的腿：探针没带 Origin 直接撞 403 —— 但这同样证明
		// signalHandler 在线（running 已置位）。403 语义归 Origin 腿自己判。
		t.Logf("readiness: 403 (custom checkOrigin), handler chain is live")
	} else if code != http.StatusBadRequest || !strings.Contains(body, "invalid offer") {
		t.Fatalf("readiness POST %s = %d %q, want 400 invalid offer", effPath, code, body)
	}

	t.Cleanup(func() {
		if err := wsrv.Stop(context.Background()); err != nil {
			t.Errorf("webrtc Stop: %v", err)
		}
		select {
		case err := <-startDone:
			if err != nil {
				t.Errorf("webrtc Start returned %v, want nil (Shutdown 收口)", err)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("webrtc Start 未在 Stop 后返回（残留 Serve goroutine）")
		}
	})
	return wsrv, base
}

// --- HTTP 收发助手 ----------------------------------------------------------

func httpClient(cfg *tls.Config) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg != nil {
		tr.TLSClientConfig = cfg
	}
	return &http.Client{Transport: tr, Timeout: testWait}
}

// postSignal 在就绪轮询里向 base+path POST 一个空信令 JSON（"{}"）。
// 任何 HTTP 响应都算「服务在跑」；ready 与否由各腿判状态码/正文。
func postSignal(t *testing.T, desc, base, path string, client *http.Client, headers map[string]string) (string, int) {
	t.Helper()
	url := base + path
	deadline := time.Now().Add(testWait)
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("%s: new request: %v", desc, err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatalf("%s: read body: %v", desc, readErr)
		}
		return string(raw), resp.StatusCode
	}
	t.Fatalf("%s: post %s: %v", desc, url, lastErr)
	return "", 0
}

// get 在就绪轮询里发一次 GET。
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

// mustNotServeBusiness 信令面必须没在明文/未授权路径上被服务：
// 要么传输层报错，要么响应正文不含业务判别串 "invalid offer"。
//
// 为什么判正文而不是判错误：Go 的 TLS 监听收到明文 HTTP/1 请求时会**回一个 400**
// （"Client sent an HTTP request to an HTTPS server"）而不是断连；而合法信令 POST
// 同样回 400。状态码在这里没有判别力，判别点是「signalHandler 的 offer 分支有没有
// 被执行过」——tls 段被静默摘掉时这里会拿到 {"error":"invalid offer"}，必须判红。
func mustNotServeBusiness(t *testing.T, desc, url string, client *http.Client) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("%s: new request: %v", desc, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Logf("%s: rejected with %v", desc, err)
		return
	}
	raw, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatalf("%s: read body: %v", desc, readErr)
	}
	if strings.Contains(string(raw), "invalid offer") {
		t.Fatalf("%s: 业务信令面在 %s 上被服务了（%d %q）——契约的 tls/证书判定没落到线上", desc, url, resp.StatusCode, string(raw))
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

// --- addr / path / network 行 ------------------------------------------------

// TestMappingOffline_AddrPathMuxReachWire 契约 addr 上 signalHandler 真的在服务：
// 空 POST 走进 offer 校验（400 + invalid offer）、GET 撞方法门（405）、
// 默认 OPTIONS 预检（204 + CORS 头）、未注册路径 404。
func TestMappingOffline_AddrPathMuxReachWire(t *testing.T) {
	addr := loopbackAddr(t)
	wsrv, base := runWebRTC(t, nil, &bootstrapv1.Server{
		Webrtc: &bootstrapv1.Server_Webrtc{Addr: addr},
	}, "/signal", nil)
	if got, want := wsrv.Endpoint(), "http://"+addr+"/signal"; got != want {
		t.Fatalf("Endpoint() = %q, want %s", got, want)
	}

	body, code := get(t, "GET on signal path", base+"/signal", httpClient(nil))
	if code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /signal = %d %q, want 405", code, body)
	}

	req, err := http.NewRequest(http.MethodOptions, base+"/signal", nil)
	if err != nil {
		t.Fatalf("new OPTIONS: %v", err)
	}
	req.Header.Set("Origin", "https://example.invalid")
	resp, err := httpClient(nil).Do(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Fatalf("preflight = %d, ACAO=%q, want 204 with CORS header (default enableCORS)", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}

	if _, code := postSignal(t, "unregistered path", base, "/absent", httpClient(nil), nil); code != http.StatusNotFound {
		t.Fatalf("POST /absent status = %d, want 404 (mux 只有契约路径)", code)
	}
}

// TestMappingOffline_ContractPathOnWire 契约 path 换掉默认 /signal：
// 新路径服务、旧路径 404——path 行落到了 mux 注册上，不是装饰。
func TestMappingOffline_ContractPathOnWire(t *testing.T) {
	addr := loopbackAddr(t)
	_, base := runWebRTC(t, nil, &bootstrapv1.Server{
		Webrtc: &bootstrapv1.Server_Webrtc{Addr: addr, Path: "/wrtc-signal"},
	}, "/wrtc-signal", nil)

	if _, code := postSignal(t, "contract path", base, "/wrtc-signal", httpClient(nil), nil); code != http.StatusBadRequest {
		t.Fatalf("POST /wrtc-signal status = %d, want 400", code)
	}
	if _, code := postSignal(t, "default path displaced", base, "/signal", httpClient(nil), nil); code != http.StatusNotFound {
		t.Fatalf("POST /signal status = %d, want 404（契约 path 没顶掉默认值）", code)
	}
}

// TestMappingOffline_NetworkTcpOnWire 显式 network: tcp 在契约地址上正常服务。
// udp 之类对 HTTP 信令面没有意义（net.Listen("udp") 能成功但 Serve 必炸），
// 契约值域里合法网络就是 tcp 族——这里判「映射没弄坏监听」。
func TestMappingOffline_NetworkTcpOnWire(t *testing.T) {
	addr := loopbackAddr(t)
	wsrv, _ := runWebRTC(t, nil, &bootstrapv1.Server{
		Webrtc: &bootstrapv1.Server_Webrtc{Addr: addr, Network: "tcp"},
	}, "/signal", nil)
	if got, want := wsrv.Endpoint(), "http://"+addr+"/signal"; got != want {
		t.Fatalf("Endpoint() = %q, want %s", got, want)
	}
}

// TestMappingOffline_BusinessPathOverridesContract 业务 WithPath 在契约 path 之后
// 应用并生效（覆盖序的线上版）。
func TestMappingOffline_BusinessPathOverridesContract(t *testing.T) {
	addr := loopbackAddr(t)
	wsrv, base := runWebRTC(t, []Option{
		WithServerOptions(webrtc.WithPath("/biz-signal")),
	}, &bootstrapv1.Server{
		Webrtc: &bootstrapv1.Server_Webrtc{Addr: addr, Path: "/contract-signal"},
	}, "/biz-signal", nil)
	if got, want := wsrv.Endpoint(), "http://"+addr+"/biz-signal"; got != want {
		t.Fatalf("Endpoint() = %q, want %s（业务 path 没盖过契约 path？）", got, want)
	}

	if _, code := postSignal(t, "business path", base, "/biz-signal", httpClient(nil), nil); code != http.StatusBadRequest {
		t.Fatalf("POST /biz-signal status = %d, want 400", code)
	}
	if _, code := postSignal(t, "contract path overridden", base, "/contract-signal", httpClient(nil), nil); code != http.StatusNotFound {
		t.Fatalf("POST /contract-signal status = %d, want 404", code)
	}
}

// --- 缺陷修复②的判据腿：Use 的接线时机 --------------------------------------

// TestMappingOffline_UseFromAfterBuildReachesWire 构造后（afterBuild 时机）注册的
// 中间件必须出现在服务链上。修复前链在 init 定形，装配期 Use 永远进不了 Serve。
func TestMappingOffline_UseFromAfterBuildReachesWire(t *testing.T) {
	addr := loopbackAddr(t)
	tagged := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Contract-Mw", "applied")
			next.ServeHTTP(w, r)
		})
	}
	_, base := runWebRTC(t, []Option{
		WithAfterBuild(func(_ context.Context, s *webrtc.Server) error {
			s.Use(tagged)
			return nil
		}),
	}, &bootstrapv1.Server{
		Webrtc: &bootstrapv1.Server_Webrtc{Addr: addr},
	}, "/signal", nil)

	req, err := http.NewRequest(http.MethodPost, base+"/signal", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := httpClient(nil).Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("X-Contract-Mw"); got != "applied" {
		t.Fatalf("X-Contract-Mw = %q, want applied（构造后 Use 的中间件没进服务链？）", got)
	}
}

// --- tls 行（含缺陷修复①的判据腿） -------------------------------------------

type testPKI struct {
	certPEM, keyPEM []byte
	cert            *x509.Certificate
	cn              string
}

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

// testPEMs 供 contract_test.go 的映射腿取一组 PEM（cert / key 同源）。
func testPEMs(t *testing.T) (certPEM, keyPEM, caPEM []byte) {
	t.Helper()
	pki := newTestPKI(t, "bald-webrtc-contract-test")
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

// tlsHandshakeWait 握手单独有界，且必须明显短于各腿的轮询窗口——窗口内至少还能
// 重试一次，否则这条腿只剩「等一轮」而没有轮询语义。
const tlsHandshakeWait = 2 * time.Second

// tlsDial 拨号 + 有界握手（tls 段被静默摘掉时它必须在窗口内报错，而不是把
// 整包拖到 -timeout——判红必须是被点名的 FAIL）。
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
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tc, nil
}

// peerCN 取服务端出示的证书 CN（轮询到就绪；一次直拨会撞 connection refused 假红）。
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

// TestMappingOffline_TlsCertServedAndEndpointSchemeHttps 契约 tls（内联 PEM）落到
// 线上：服务端出示契约证书、信令面走 https，**且 Endpoint() 的方案是 https://**
// ——缺陷①：修复前硬编 http，注册/服务发现出去的地址直接打不通 TLS 端口。
func TestMappingOffline_TlsCertServedAndEndpointSchemeHttps(t *testing.T) {
	pki := newTestPKI(t, "bald-webrtc-tls-config")
	addr := loopbackAddr(t)

	wsrv, _ := runWebRTC(t, nil, &bootstrapv1.Server{
		Webrtc: &bootstrapv1.Server_Webrtc{
			Addr: addr,
			Tls: &bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{
				CertPem: pki.certPEM, KeyPem: pki.keyPEM,
			}},
		},
	}, "/signal", clientTLS(pki, false))

	if got, want := wsrv.Endpoint(), "https://"+addr+"/signal"; got != want {
		t.Fatalf("Endpoint() = %q, want %s（缺陷①回归：方案没跟随实际协议）", got, want)
	}
	if got := peerCN(t, addr, clientTLS(pki, false)); got != pki.cn {
		t.Fatalf("peer CN = %q, want %s（服务端出示的不是契约里那张证书）", got, pki.cn)
	}
	mustNotServeBusiness(t, "明文打到 TLS 端口", "http://"+addr+"/signal", httpClient(nil))
}

// TestMappingOffline_TlsFileBranchMtls 契约 file 分支 + ca：
// 无客户端证书握手即断、带上后信令面可服务——ca 不是摆设。
func TestMappingOffline_TlsFileBranchMtls(t *testing.T) {
	pki := newTestPKI(t, "bald-webrtc-mtls")
	certPath, keyPath := writePKI(t, pki)
	caPath := filepath.Join(filepath.Dir(certPath), "ca.crt")
	if err := os.WriteFile(caPath, pki.certPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	addr := loopbackAddr(t)

	wsrv, _ := runWebRTC(t, nil, &bootstrapv1.Server{
		Webrtc: &bootstrapv1.Server_Webrtc{
			Addr: addr,
			Tls: &bootstrapv1.Server_TLS{File: &bootstrapv1.Server_TLS_File{
				CertPath: &certPath, KeyPath: &keyPath, CaPath: &caPath,
			}},
		},
	}, "/signal", clientTLS(pki, true))
	if got, want := wsrv.Endpoint(), "https://"+addr+"/signal"; got != want {
		t.Fatalf("Endpoint() = %q, want %s", got, want)
	}

	mustNotServeBusiness(t, "mTLS 下不带客户端证书", "https://"+addr+"/signal", httpClient(clientTLS(pki, false)))

	body, code := postSignal(t, "mTLS with client cert", "https://"+addr, "/signal", httpClient(clientTLS(pki, true)), nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "invalid offer") {
		t.Fatalf("POST over mTLS = %d %q, want 400 invalid offer", code, body)
	}
}

// --- 非契约能力经 WithServerOptions 的线上反证 -------------------------------

// TestMappingOffline_BusinessOriginOnWire WithCheckOrigin 不在契约面上，
// 经业务 Option 注入后线上真的按 Origin 分派：错 Origin 403、对 Origin 进信令门（400）。
func TestMappingOffline_BusinessOriginOnWire(t *testing.T) {
	addr := loopbackAddr(t)
	_, base := runWebRTC(t, []Option{
		WithServerOptions(webrtc.WithCheckOrigin("https://allowed.example")),
	}, &bootstrapv1.Server{
		Webrtc: &bootstrapv1.Server_Webrtc{Addr: addr},
	}, "/signal", nil)

	if _, code := postSignal(t, "wrong origin", base, "/signal", httpClient(nil),
		map[string]string{"Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Fatalf("POST with wrong origin status = %d, want 403", code)
	}
	if _, code := postSignal(t, "allowed origin", base, "/signal", httpClient(nil),
		map[string]string{"Origin": "https://allowed.example"}); code != http.StatusBadRequest {
		t.Fatalf("POST with allowed origin status = %d, want 400（进信令 offer 门）", code)
	}
}
