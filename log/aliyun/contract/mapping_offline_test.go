package contract

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sls "github.com/aliyun/aliyun-log-go-sdk"
	"github.com/pierrec/lz4/v4"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
)

// 本文件是 §7.25 的运行期闸门：契约 backends[].aliyun 的六行
// （endpoint/project/logstore/access_key/access_secret/security_token）逐行判到
// 真线上——用本机假 SLS 线，签名、URL 拼装与 LZ4 压缩都走 SDK 的真实代码，只把
// 名字解析改道。假线做法：SDK 的 newDefaultTransport() 是
// http.DefaultTransport.(*http.Transport).Clone()，克隆继承 DialContext，于是换掉
// 进程默认的 Transport 就能把任意 <project>.<endpoint> 域名拨到本机监听。

const (
	gateProject  = "gate-project"
	gateLogstore = "gate-logstore"
	gateAK       = "gate-ak"
	gateSK       = "gate-sk"
	gateToken    = "gate-security-token"
)

type wireHit struct {
	method, host, path string
	header             http.Header
	body               []byte
}

// wire 收集假线两侧的观察：HTTP 请求（明文路径）与客户端首包（TLS 与否由首包
// 字节判定，明文 leg 同时也能看到请求）。
type wire struct {
	mu       sync.Mutex
	hits     []wireHit
	dialed   []string
	firstPkt [][]byte
}

func (w *wire) record(method, host, path string, h http.Header, b []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.hits = append(w.hits, wireHit{method, host, path, h, b})
}

func (w *wire) recordDial(host string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dialed = append(w.dialed, host)
}

func (w *wire) recordFirstPacket(b []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.firstPkt) == 0 {
		w.firstPkt = append(w.firstPkt, append([]byte(nil), b...))
	}
}

func (w *wire) snapshot() (hits []wireHit, dialed []string, first [][]byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]wireHit{}, w.hits...), append([]string{}, w.dialed...), append([][]byte{}, w.firstPkt...)
}

// newWire 起假线并把「任意域名都拨到它」装进 http.DefaultTransport。
// sniffAndDrop 为首包嗅探模式：记录客户端写出的第一段字节后立刻掐掉这条连接——
// TLS leg 用它（握手必然失败，我们要的只是「首包是 TLS 记录而不是 HTTP 请求行」）。
func newWire(t *testing.T, sniffAndDrop bool) (*wire, int) {
	t.Helper()
	w := &wire{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	if !sniffAndDrop {
		srv := &http.Server{Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			w.record(r.Method, r.Host, r.URL.Path, r.Header.Clone(), b)
			rw.Header().Set("x-log-requestid", "GATE")
			rw.WriteHeader(http.StatusOK)
		})}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { _ = srv.Close() })
	} else {
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				// 嗅探模式下不解析请求，只等客户端首包；拿到就断开。
				buf := make([]byte, 4096)
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				n, _ := conn.Read(buf)
				if n > 0 {
					w.recordFirstPacket(buf[:n])
				}
				_ = conn.Close()
			}
		}()
	}

	orig := http.DefaultTransport
	http.DefaultTransport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, _, err := net.SplitHostPort(addr); err == nil {
				w.recordDial(host)
			}
			return (&net.Dialer{}).DialContext(ctx, network, ln.Addr().String())
		},
	}
	t.Cleanup(func() { http.DefaultTransport = orig })
	return w, ln.Addr().(*net.TCPAddr).Port
}

// waitHit 等第一条落进假线的 HTTP 请求；超时即判红——判据不许依赖「没有请求就是对的」。
func (w *wire) waitHit(t *testing.T, d time.Duration) wireHit {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		hits, dialed, _ := w.snapshot()
		if len(hits) > 0 {
			return hits[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s 内假线上没有收到任何请求（dialed=%v）", d, dialed)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForFirstPacket 等 sniff 模式下客户端写出的首包。
func (w *wire) waitForFirstPacket(t *testing.T, d time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		_, dialed, first := w.snapshot()
		if len(first) > 0 {
			return first[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s 内假线上没有收到任何首包（dialed=%v）", d, dialed)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func fullSegment(port int) *bootstrapv1.Logger_Aliyun {
	return &bootstrapv1.Logger_Aliyun{
		Endpoint:      "http://127.0.0.1:" + strconv.Itoa(port),
		Project:       gateProject,
		Logstore:      gateLogstore,
		AccessKey:     gateAK,
		AccessSecret:  gateSK,
		SecurityToken: gateToken,
	}
}

// startGate 用契约段构造后端并写一条日志，返回假线与观察器；停机交给 t.Cleanup。
func startGate(t *testing.T, seg *bootstrapv1.Logger_Aliyun) {
	t.Helper()
	l, cleanup, err := Provider(context.Background(), &bootstrapv1.Logger_Backend{Type: Type, Aliyun: seg})
	if err != nil {
		t.Fatalf("Provider(完整合法段) 失败: %v", err)
	}
	t.Cleanup(cleanup)

	ctx := log.ContextWithAttrs(context.Background(), slog.String("trace_id", "gate-trace"))
	l.With("svc", "gate").Info(ctx, "gate-msg", "k1", "v1")
}

// signV1 独立复算 SLS V1 签名（规范串形状取自 SDK 的 signature.go，不复用其代码），
// 用来证明 access_secret 真的进了签名主体——只做「有 Authorization 头」是判不住的。
func signV1(t *testing.T, secret string, hit wireHit) string {
	t.Helper()
	var keys []string
	values := map[string]string{}
	for k, vals := range hit.header {
		lk := strings.ToLower(k)
		if !strings.HasPrefix(lk, "x-log-") && !strings.HasPrefix(lk, "x-acs-") {
			continue
		}
		if len(vals) != 1 {
			t.Fatalf("签名要求 x-log-*/x-acs-* 各只有一个值，得到 %s=%v", lk, vals)
		}
		values[lk] = strings.TrimSpace(vals[0])
		keys = append(keys, lk)
	}
	sort.Strings(keys)
	var canoHeaders string
	for i, k := range keys {
		canoHeaders += k + ":" + values[k]
		if i+1 < len(keys) {
			canoHeaders += "\n"
		}
	}

	signStr := strings.Join([]string{
		hit.method,
		hit.header.Get("Content-Md5"),
		hit.header.Get("Content-Type"),
		hit.header.Get("Date"),
		canoHeaders,
		hit.path,
	}, "\n")

	mac := hmac.New(sha1.New, []byte(secret))
	if _, err := mac.Write([]byte(signStr)); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// decodeLogGroup 解出线上正文：x-log-compresstype=lz4 是块格式（SDK 用
// lz4.CompressBlock，不可压数据退化为全字面量块），原文长度在 x-log-bodyrawsize。
func decodeLogGroup(t *testing.T, hit wireHit) *sls.LogGroup {
	t.Helper()
	if ct := hit.header.Get("X-Log-Compresstype"); ct != "lz4" {
		t.Fatalf("正文压缩类型 = %q，闸门按 lz4 解", ct)
	}
	rawSize, err := strconv.Atoi(hit.header.Get("X-Log-Bodyrawsize"))
	if err != nil {
		t.Fatalf("x-log-bodyrawsize = %q: %v", hit.header.Get("X-Log-Bodyrawsize"), err)
	}
	raw := make([]byte, rawSize)
	n, err := lz4.UncompressBlock(hit.body, raw)
	if err != nil {
		t.Fatalf("lz4 解压失败: %v", err)
	}
	if n != rawSize {
		t.Fatalf("lz4 解压长度 = %d, 头部声明 %d", n, rawSize)
	}
	group := &sls.LogGroup{}
	if err := group.Unmarshal(raw[:n]); err != nil {
		t.Fatalf("LogGroup 反序列化失败: %v", err)
	}
	return group
}

func contentOf(group *sls.LogGroup) map[string]string {
	out := map[string]string{}
	for _, l := range group.GetLogs() {
		for _, c := range l.GetContents() {
			out[c.GetKey()] = c.GetValue()
		}
	}
	return out
}

// TestMappingOffline_AllSixRowsReachWire 六行各判一处线上可见后果：
// endpoint 决定拨到哪个端口与传输方式，project 进 host 前缀，logstore 进 path，
// access_key 是 Authorization 的签名主体，access_secret 由独立复算的 V1 签名判定
// （附一条错密钥对照，证明这条判据有分辨力），security_token 进 x-acs-security-token。
func TestMappingOffline_AllSixRowsReachWire(t *testing.T) {
	w, port := newWire(t, false)
	startGate(t, fullSegment(port))
	hit := w.waitHit(t, 20*time.Second)

	// endpoint：拨号目标是假线端口（SDK 把 project 拼成 host 前缀后仍按该端口寻址），
	// 且 endpoint 的 http:// 落到了明文请求上——假线的 HTTP server 解析成功即为证据。
	_, dialed, _ := w.snapshot()
	if len(dialed) == 0 {
		t.Fatal("假线上没有拨号记录")
	}
	for _, host := range dialed {
		if want := gateProject + ".127.0.0.1"; !strings.HasSuffix(host, want) {
			t.Errorf("拨号的 host = %q，期望以 %q 结尾（project 前缀 + endpoint host）", host, want)
		}
	}

	// project + logstore：落点 = "<scheme>://<project>.<endpoint host>/logstores/<logstore>"
	if want := gateProject + ".127.0.0.1:" + strconv.Itoa(port); hit.host != want {
		t.Errorf("请求 host = %q, want %q", hit.host, want)
	}
	if want := "/logstores/" + gateLogstore; hit.path != want {
		t.Errorf("请求 path = %q, want %q", hit.path, want)
	}
	if hit.method != http.MethodPost {
		t.Errorf("请求 method = %q, want POST", hit.method)
	}

	// access_key：签名主体；access_secret：签名值（独立复算）
	auth := hit.header.Get("Authorization")
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || parts[0] != "LOG" {
		t.Fatalf("Authorization = %q，期望 \"LOG <ak>:<sig>\" 形状", auth)
	}
	idAndSig := strings.SplitN(parts[1], ":", 2)
	if len(idAndSig) != 2 {
		t.Fatalf("Authorization = %q，缺少签名分隔符", auth)
	}
	if idAndSig[0] != gateAK {
		t.Errorf("Authorization 的签名主体 = %q, want %q（access_key 未落到线上）", idAndSig[0], gateAK)
	}
	if got, want := idAndSig[1], signV1(t, gateSK, hit); got != want {
		t.Errorf("签名不匹配配置的 access_secret: got %q want %q", got, want)
	}
	if wrong := signV1(t, "not-the-configured-sk", hit); idAndSig[1] == wrong {
		t.Errorf("用错密钥也能复算出同一个签名，这条判据没有分辨力")
	}

	// security_token
	if got := hit.header.Get("X-Acs-Security-Token"); got != gateToken {
		t.Errorf("x-acs-security-token = %q, want %q", got, gateToken)
	}

	// 正文：Content-Md5 对得上收到的字节，且解出来的日志条目带着 level/msg/属性
	if got, want := hit.header.Get("Content-Md5"), strings.ToUpper(hex.EncodeToString(md5Sum(hit.body))); got != want {
		t.Errorf("Content-Md5 = %q, want %q（签名的正文与到达的正文不是同一份）", got, want)
	}
	contents := contentOf(decodeLogGroup(t, hit))
	if contents["level"] != "INFO" {
		t.Errorf("日志条目 level = %q, want INFO", contents["level"])
	}
	if contents["msg"] != "gate-msg" {
		t.Errorf("日志条目 msg = %q, want gate-msg", contents["msg"])
	}
	for key, want := range map[string]string{"svc": "gate", "trace_id": "gate-trace", "k1": "v1"} {
		if got := contents[key]; got != want {
			t.Errorf("日志条目 %s = %q, want %q（With/ctx 属性/调用参数三路合并）", key, got, want)
		}
	}
}

func md5Sum(b []byte) []byte {
	sum := md5.Sum(b)
	return sum[:]
}

// TestMappingOffline_SchemeAxis endpoint 的 scheme 决定传输是否上 TLS：
// 显式 http:// 走明文（假线的 HTTP server 能解析出请求），显式 https:// 走 TLS
// （首包是 TLS 握手记录，不是 HTTP 请求行）。裸 host 在这里判不了——它在构造期
// 就被挡（见 TestMappingOffline_RejectsIncompleteRows）。
func TestMappingOffline_SchemeAxis(t *testing.T) {
	t.Run("显式 http:// 走明文", func(t *testing.T) {
		w, port := newWire(t, false)
		startGate(t, fullSegment(port))
		hit := w.waitHit(t, 20*time.Second)
		if !strings.HasPrefix(hit.header.Get("Authorization"), "LOG ") {
			t.Fatalf("明文请求缺少签名，判据落空: %v", hit.header)
		}
	})

	t.Run("显式 https:// 走 TLS", func(t *testing.T) {
		w, port := newWire(t, true)
		seg := fullSegment(port)
		seg.Endpoint = "https://127.0.0.1:" + strconv.Itoa(port)
		startGate(t, seg)
		first := w.waitForFirstPacket(t, 20*time.Second)
		if first[0] != 0x16 {
			t.Fatalf("https:// 端点的首包不是 TLS 握手记录（content type = 0x%02x，前 16 字节 %x）——裸 host 明文那条形同虚设的传输安全又回来了", first[0], first[:min(16, len(first))])
		}
		hits, _, _ := w.snapshot()
		if len(hits) != 0 {
			t.Errorf("TLS leg 上假线的明文 HTTP server 不该解析出请求，得到 %d 条", len(hits))
		}
	})
}

// security_token 是六行里唯一可留空的：留空时构造必须成功，且线上不该出现
// x-acs-security-token（SDK 只在非空时写入该头）。
func TestMappingOffline_OmitsAbsentSecurityToken(t *testing.T) {
	w, port := newWire(t, false)
	seg := fullSegment(port)
	seg.SecurityToken = ""
	startGate(t, seg)
	hit := w.waitHit(t, 20*time.Second)
	if got := hit.header.Get("X-Acs-Security-Token"); got != "" {
		t.Errorf("契约留空仍上线上带 token: %q", got)
	}
	if !strings.HasPrefix(hit.header.Get("Authorization"), "LOG "+gateAK+":") {
		t.Errorf("长期 AK/SK 路径的签名主体丢失: %q", hit.header.Get("Authorization"))
	}
}

// TestMappingOffline_RejectsIncompleteRows 契约行留空必须在构造期失败并点名是哪
// 一行，且假线上零拨号零请求——「留空也构造成功」的那五种静默形状（空 endpoint、
// 无 scheme 的 endpoint、空 project、空 logstore、空凭证）都在这里挡。
// 空 project/logstore 不再回落到 options 层的占位默认，所以线上落点也判不出来。
func TestMappingOffline_RejectsIncompleteRows(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*bootstrapv1.Logger_Aliyun)
		want   string
	}{
		// want 取「那一条校验自己的措辞」而不是字段名：空 endpoint 若被摘掉，
		// scheme 那条也会回一个含 "endpoint" 的错误，只比字段名就判不出是哪一行漂了。
		{"endpoint 留空", func(c *bootstrapv1.Logger_Aliyun) { c.Endpoint = "" }, "endpoint is required"},
		{"endpoint 无 scheme", func(c *bootstrapv1.Logger_Aliyun) { c.Endpoint = "127.0.0.1:1" }, "must start with https://"},
		{"project 留空", func(c *bootstrapv1.Logger_Aliyun) { c.Project = "" }, "project is required"},
		{"logstore 留空", func(c *bootstrapv1.Logger_Aliyun) { c.Logstore = "" }, "logstore is required"},
		{"access_key 留空", func(c *bootstrapv1.Logger_Aliyun) { c.AccessKey = "" }, "access_key is required"},
		{"access_secret 留空", func(c *bootstrapv1.Logger_Aliyun) { c.AccessSecret = "" }, "access_secret is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, port := newWire(t, false)
			seg := fullSegment(port)
			tc.mutate(seg)

			l, cleanup, err := Provider(context.Background(), &bootstrapv1.Logger_Backend{Type: Type, Aliyun: seg})
			if err == nil {
				cleanup()
				t.Fatalf("%s 仍然构造成功，日志会静默投出去", tc.name)
			}
			if l != nil {
				t.Errorf("构造失败却返回了非 nil logger: %v", l)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误未点名有缺陷的配置项: err=%v want 包含 %q", err, tc.want)
			}

			hits, dialed, _ := w.snapshot()
			if len(hits) != 0 || len(dialed) != 0 {
				t.Errorf("构造失败后不该有线上活动: hits=%d dialed=%v", len(hits), dialed)
			}
		})
	}
}

// TestMappingOffline_NilSegment 契约段缺失时的形状错误：Provider 拿到 nil aliyun
// 段必须报错，而不是构造一个默认落点的后端（假线上零活动）。
func TestMappingOffline_NilSegment(t *testing.T) {
	w, _ := newWire(t, false)
	l, cleanup, err := Provider(context.Background(), &bootstrapv1.Logger_Backend{Type: Type})
	if err == nil {
		cleanup()
		t.Fatalf("aliyun 段为 nil 仍构造成功: %v", l)
	}
	if !strings.Contains(err.Error(), "aliyun segment is nil") {
		t.Errorf("错误未说明是段缺失: %v", err)
	}
	if l != nil {
		t.Errorf("构造失败却返回了非 nil logger: %v", l)
	}
	if hits, dialed, _ := w.snapshot(); len(hits) != 0 || len(dialed) != 0 {
		t.Errorf("段缺失不该有线上活动: hits=%d dialed=%d", len(hits), len(dialed))
	}
}
