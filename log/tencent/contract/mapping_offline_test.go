package contract

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/tls"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pierrec/lz4"
	cls "github.com/tencentcloud/tencentcloud-cls-sdk-go"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
)

// 本文件是 §7.26 的运行期闸门：契约 backends[].tencent 的四行
// （endpoint/topic_id/access_key/access_secret）逐行判到真线上。
//
// 假线做法与 §7.25（aliyun）不同，这一点必须先说清，否则下一格会照抄错姿势：
// CLS 客户端**不克隆** http.DefaultTransport，它在 NewCLSClient 里自建
// *http.Transport，所以换进程默认 Transport 对它毫无作用。可行的改道口是
// endpoint 本身——SDK 会按 http:// 前缀剥出 host 并拼出
// "<scheme>://<host>/structuredlog?topic_id=<id>"，于是 endpoint 直接写
// httptest 监听的本机地址就能把日志投进假线。签名（q-sign-* 那套 HMAC-SHA1）
// 与 LZ4 块压缩全走 SDK 真码。

const (
	gateTopic = "gate-topic"
	gateAK    = "gate-ak"
	gateSK    = "gate-sk"
)

type wireHit struct {
	method, host, path string
	query              url.Values
	header             http.Header
	body               []byte
	conn               *tls.ConnectionState
}

// wire 收集假线上的请求；hits 由 handler 在写响应之前追加，因此「客户端的 Do
// 已返回」必然蕴含「这条 hit 已被记录」——cleanup 那条判据靠这个时序成立。
type wire struct {
	mu   sync.Mutex
	hits []wireHit
}

func (w *wire) record(h wireHit) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.hits = append(w.hits, h)
}

func (w *wire) snapshot() []wireHit {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]wireHit{}, w.hits...)
}

// waitHits 等假线上攒够 n 条请求，返回第 n 条；超时即判红——判据不许依赖
// 「没有请求就是对的」。
func (w *wire) waitHits(t *testing.T, d time.Duration, n int) wireHit {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if hits := w.snapshot(); len(hits) >= n {
			return hits[n-1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s 内假线上不足 %d 条请求（已收到 %d 条）", d, n, len(w.snapshot()))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (w *wire) waitHit(t *testing.T, d time.Duration) wireHit {
	t.Helper()
	return w.waitHits(t, d, 1)
}

// newWire 起明文假线（httptest 的 URL 天然带 http:// 前缀与 127.0.0.1 地址，
// 正是 CLS 客户端肯认的 endpoint 形状）。
func newWire(t *testing.T) (*wire, *httptest.Server) {
	t.Helper()
	return newWireOn(t, false)
}

// newTLSWire 起自签名证书的 TLS 假线——用来判「https:// 端点到底有没有验证书」。
func newTLSWire(t *testing.T) (*wire, *httptest.Server) {
	t.Helper()
	return newWireOn(t, true)
}

func newWireOn(t *testing.T, secure bool) (*wire, *httptest.Server) {
	t.Helper()
	w := &wire{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		h := wireHit{
			method: r.Method,
			host:   r.Host,
			path:   r.URL.Path,
			query:  r.URL.Query(),
			header: r.Header.Clone(),
			conn:   r.TLS,
		}
		// 正文是 lz4 块格式的 LogGroupList，Content-Length 已知，读满即可。
		size, _ := strconv.Atoi(r.Header.Get("Content-Length"))
		h.body = make([]byte, size)
		for off := 0; off < size; {
			n, err := r.Body.Read(h.body[off:])
			if err != nil && n == 0 {
				break
			}
			off += n
		}
		w.record(h)
		rw.WriteHeader(http.StatusOK)
	}))
	if secure {
		srv.StartTLS() // 自签名证书：SDK 在 https 下硬编 InsecureSkipVerify，所以它敢连
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return w, srv
}

// startGate 用契约段构造后端并写一条日志，返回 cleanup；不在此注册 t.Cleanup——
// cleanup 的「冲刷到返回」本身是被判的对象，必须由调用方在写入之后立刻执行。
func startGate(t *testing.T, endpoint string) func() {
	t.Helper()
	l, cleanup, err := Provider(context.Background(), &bootstrapv1.Logger_Backend{
		Type:    Type,
		Tencent: fullSegment(endpoint),
	})
	if err != nil {
		t.Fatalf("Provider(完整合法段) 失败: %v", err)
	}

	ctx := log.ContextWithAttrs(context.Background(), slog.String("trace_id", "gate-trace"))
	l.With("svc", "gate").Info(ctx, "gate-msg", "k1", "v1")
	return cleanup
}

func fullSegment(endpoint string) *bootstrapv1.Logger_Tencent {
	return &bootstrapv1.Logger_Tencent{
		Endpoint:     endpoint,
		TopicId:      gateTopic,
		AccessKey:    gateAK,
		AccessSecret: gateSK,
	}
}

// signQ 独立复算 CLS 的 q-sign-* 签名（形状取自 SDK 的 signature.go，不复用其
// 代码），用来证明 access_secret 真的进了签名主体。规范串的四个入参全部从线上
// 读回：method、path、query、以及参与签名的 Host 与 Content-Type。
func signQ(t *testing.T, secret string, hit wireHit) (string, string) {
	t.Helper()
	headers := url.Values{
		"content-type": {hit.header.Get("Content-Type")},
		"host":         {hit.host},
	}
	params := url.Values{}
	for k, vs := range hit.query {
		for _, v := range vs {
			params.Add(strings.ToLower(k), v)
		}
	}
	formatString := strings.ToLower(hit.method) + "\n" + hit.path + "\n" +
		params.Encode() + "\n" + headers.Encode() + "\n"

	// signTime 必须用线上那一份——它同时是 HMAC 的密钥，不在 Authorization 之外。
	signTime := authField(hit.header.Get("Authorization"), "q-sign-time")
	stringToSign := "sha1\n" + signTime + "\n" + sha1Hex([]byte(formatString)) + "\n"
	signKey := hmacSha1Hex([]byte(secret), []byte(signTime))
	return hmacSha1Hex([]byte(signKey), []byte(stringToSign)), signTime
}

func sha1Hex(b []byte) string {
	sum := sha1.Sum(b)
	return hex.EncodeToString(sum[:])
}

func hmacSha1Hex(key, msg []byte) string {
	mac := hmac.New(sha1.New, key)
	if _, err := mac.Write(msg); err != nil {
		panic(err)
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// authField 从 Authorization 的 k=v&k=v 串里取一个字段。
func authField(authorization, key string) string {
	for _, part := range strings.Split(authorization, "&") {
		if k, v, ok := strings.Cut(part, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// decodeLogGroupList 解出线上正文：SDK 用 lz4.CompressBlock（块格式，不可压数据
// 退化为全字面量块），头部只有 x-cls-compress-type，没有原文长度，所以按递增缓冲
// 试解——「解不出来」与「缓冲区太小」在这里必须区分开，后者直接判红。
func decodeLogGroupList(t *testing.T, hit wireHit) *cls.LogGroupList {
	t.Helper()
	if ct := hit.header.Get("X-Cls-Compress-Type"); ct != "lz4" {
		t.Fatalf("正文压缩类型 = %q，闸门按 lz4 解", ct)
	}
	dst := make([]byte, 4<<20)
	n, err := lz4.UncompressBlock(hit.body, dst)
	if err != nil {
		t.Fatalf("lz4 解压失败: %v", err)
	}
	list := &cls.LogGroupList{}
	if err := list.Unmarshal(dst[:n]); err != nil {
		t.Fatalf("LogGroupList 反序列化失败: %v", err)
	}
	return list
}

func contentsOf(list *cls.LogGroupList) map[string]string {
	out := map[string]string{}
	for _, g := range list.LogGroupList {
		for _, l := range g.Logs {
			for _, c := range l.Contents {
				out[c.GetKey()] = c.GetValue()
			}
		}
	}
	return out
}

// TestMappingOffline_AllFourRowsReachWire 四行各判一处线上可见后果：
// endpoint 决定落点的 host（以及传输方式，见 SchemeAxis），topic_id 进 URL 查询，
// access_key 是 Authorization 的 q-ak，access_secret 由独立复算的 q-signature 判定
// （附一条错密钥对照，证明这条判据有分辨力）。
//
// 另有一条不是「映射行」的判据：cleanup 返回时假线上必须已经有这条请求——Close 真的
// 等到投递任务归零，而不是把没冲刷的批次丢在后台。改空 cleanup 时这里会红，因为
// producer 的 LingerMs 默认 2000，写入后立刻返回的 cleanup 来不及让批次自己超时。
func TestMappingOffline_AllFourRowsReachWire(t *testing.T) {
	w, srv := newWire(t)
	cleanup := startGate(t, srv.URL)
	cleanup()

	hits := w.snapshot()
	if len(hits) == 0 {
		t.Fatal("cleanup 返回时假线上还没有请求——冲刷没有同步等到投递完成")
	}
	hit := hits[0]

	// endpoint：落点就是假线的 host（httptest 的地址），路径由 SDK 固定为 /structuredlog。
	if want := strings.TrimPrefix(srv.URL, "http://"); hit.host != want {
		t.Errorf("请求 host = %q, want %q（endpoint 未落到线上）", hit.host, want)
	}
	if hit.path != "/structuredlog" {
		t.Errorf("请求 path = %q, want /structuredlog", hit.path)
	}
	if hit.method != http.MethodPost {
		t.Errorf("请求 method = %q, want POST", hit.method)
	}
	if hit.conn != nil {
		t.Errorf("明文 endpoint 的请求却带 TLS 状态: %+v", hit.conn)
	}

	// topic_id
	if got := hit.query.Get("topic_id"); got != gateTopic {
		t.Errorf("topic_id = %q, want %q", got, gateTopic)
	}

	// access_key + access_secret
	auth := hit.header.Get("Authorization")
	if auth == "" {
		t.Fatalf("缺少 Authorization，签名主体与签名值都判不了: %v", hit.header)
	}
	if got := authField(auth, "q-ak"); got != gateAK {
		t.Errorf("q-ak = %q, want %q（access_key 未落到线上）", got, gateAK)
	}
	if got := authField(auth, "q-sign-algorithm"); got != "sha1" {
		t.Errorf("q-sign-algorithm = %q, want sha1", got)
	}
	// 参与签名的头与查询参数只有这两组，多一个少一个都意味着线上形状变了。
	if got := authField(auth, "q-header-list"); got != "content-type;host" {
		t.Errorf("q-header-list = %q, want content-type;host", got)
	}
	if got := authField(auth, "q-url-param-list"); got != "topic_id" {
		t.Errorf("q-url-param-list = %q, want topic_id", got)
	}
	gotSig := authField(auth, "q-signature")
	want, signTime := signQ(t, gateSK, hit)
	if gotSig != want {
		t.Errorf("签名不匹配配置的 access_secret: got %q want %q（signTime=%s）", gotSig, want, signTime)
	}
	if wrong, _ := signQ(t, "not-the-configured-sk", hit); gotSig == wrong {
		t.Errorf("用错密钥也能复算出同一个签名，这条判据没有分辨力")
	}
	if keyTime := authField(auth, "q-key-time"); keyTime != signTime {
		t.Errorf("q-key-time = %q, want 与 q-sign-time 相同的 %q", keyTime, signTime)
	}

	// 正文：解出来的日志条目带着 level/msg/属性，说明四行落对了目标而不是投了空包
	if got := hit.header.Get("Content-Type"); got != "application/x-protobuf" {
		t.Errorf("Content-Type = %q, want application/x-protobuf（参与签名，改它等于改规范串）", got)
	}
	contents := contentsOf(decodeLogGroupList(t, hit))
	for key, want := range map[string]string{
		"level":    "INFO",
		"msg":      "gate-msg",
		"svc":      "gate",
		"trace_id": "gate-trace",
		"k1":       "v1",
	} {
		if got := contents[key]; got != want {
			t.Errorf("日志条目 %s = %q, want %q（With/ctx 属性/调用参数三路合并）", key, got, want)
		}
	}
}

// TestMappingOffline_SchemeAxis endpoint 的 scheme 决定传输：显式 http:// 走明文
// （假线的明文 server 解析出请求、TLS 状态为空），显式 https:// 走 TLS。
//
// TLS 那条同时判一件不该成立的事：CLS 客户端在 scheme 为 https 时硬编
// tls.Config{InsecureSkipVerify: true}（SDK 的 NewCLSClient）。服务端看不到客户端的
// 验证链（它自己的 r.TLS 里 PeerCertificates 是客户端证书，这里没有），所以「不验证书」
// 只能两侧合起来判：先用默认 http.Client 打同一条假线，证明这张自签名证书**本来会被
// 拒绝**；再证明 CLS 客户端照样把日志投了进来。两者同时成立才是 InsecureSkipVerify 在
// 起作用。锁住它不是「认可这个行为」，而是让登记册 §7.26 那句「https 不验证书」有实测
// 出处——上游哪天修好，第一条子判据（SDK 的请求）会红而不是悄悄变绿。
func TestMappingOffline_SchemeAxis(t *testing.T) {
	t.Run("显式 http:// 走明文", func(t *testing.T) {
		w, srv := newWire(t)
		cleanup := startGate(t, srv.URL)
		defer cleanup()
		hit := w.waitHit(t, 20*time.Second)
		if hit.conn != nil {
			t.Fatalf("http:// 端点的请求带上了 TLS 状态: %+v", hit.conn)
		}
		if authField(hit.header.Get("Authorization"), "q-signature") == "" {
			t.Fatalf("明文请求缺少签名，判据落空: %v", hit.header)
		}
	})

	t.Run("显式 https:// 走 TLS 且不验证书", func(t *testing.T) {
		w, srv := newTLSWire(t)

		// 反面：默认客户端拒绝这张证书——没有这一步，「SDK 连上了」可能只是假线
		// 的证书本来就受信任，那条判据什么也没判。
		_, err := (&http.Client{Timeout: 5 * time.Second}).Get(srv.URL)
		if err == nil || !strings.Contains(err.Error(), "x509") {
			t.Fatalf("默认客户端本应因证书不受信任而失败，得到 err=%v——假线不再能分辨「验证书」与「不验证书」，本条需要重写", err)
		}

		cleanup := startGate(t, srv.URL)
		defer cleanup()
		hit := w.waitHit(t, 20*time.Second)
		if hit.conn == nil {
			t.Fatalf("https:// 端点的请求没有 TLS 状态（落回明文了？）: host=%s path=%s", hit.host, hit.path)
		}
		if !hit.conn.HandshakeComplete {
			t.Fatalf("TLS 握手未完成: %+v", hit.conn)
		}
		// 签名与正文一起上了 TLS——这是「scheme 决定传输」那一半的正向证据。
		if authField(hit.header.Get("Authorization"), "q-signature") == "" {
			t.Fatalf("TLS 请求缺少签名: %v", hit.header)
		}
	})
}

// TestMappingOffline_RejectsIncompleteRows 契约行留空必须在构造期失败并点名是哪
// 一行：空 endpoint、无 scheme 的 endpoint、空 topic_id、空凭证两项——五种形状今天
// 全都能「构造成功」并走到运行期才失败，而投递路径不消费错误。
// 无 scheme 那条的后果最隐蔽：SDK 按自己的默认把裸 host 投成明文 HTTP。
func TestMappingOffline_RejectsIncompleteRows(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*bootstrapv1.Logger_Tencent)
		want   string
	}{
		// want 取「那一条校验自己的措辞」而不是字段名：空 endpoint 若被摘掉，
		// scheme 那条也会回一个含 "endpoint" 的错误，只比字段名就判不出是哪一行漂了。
		{"endpoint 留空", func(c *bootstrapv1.Logger_Tencent) { c.Endpoint = "" }, "endpoint is required"},
		{"endpoint 无 scheme", func(c *bootstrapv1.Logger_Tencent) { c.Endpoint = "127.0.0.1:1" }, "must start with https://"},
		{"topic_id 留空", func(c *bootstrapv1.Logger_Tencent) { c.TopicId = "" }, "topic_id is required"},
		{"access_key 留空", func(c *bootstrapv1.Logger_Tencent) { c.AccessKey = "" }, "access_key is required"},
		{"access_secret 留空", func(c *bootstrapv1.Logger_Tencent) { c.AccessSecret = "" }, "access_secret is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, srv := newWire(t)
			seg := fullSegment(srv.URL)
			tc.mutate(seg)

			l, cleanup, err := Provider(context.Background(), &bootstrapv1.Logger_Backend{Type: Type, Tencent: seg})
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
			if hits := w.snapshot(); len(hits) != 0 {
				t.Errorf("构造失败后不该有线上活动: hits=%d", len(hits))
			}
		})
	}
}

// TestMappingOffline_NilSegment 契约段缺失时的形状错误：Provider 拿到 nil tencent
// 段必须报错，而不是构造一个默认落点的后端（假线上零活动）。
func TestMappingOffline_NilSegment(t *testing.T) {
	w, _ := newWire(t)
	l, cleanup, err := Provider(context.Background(), &bootstrapv1.Logger_Backend{Type: Type})
	if err == nil {
		cleanup()
		t.Fatalf("tencent 段为 nil 仍构造成功: %v", l)
	}
	if !strings.Contains(err.Error(), "tencent segment is nil") {
		t.Errorf("错误未说明是段缺失: %v", err)
	}
	if l != nil {
		t.Errorf("构造失败却返回了非 nil logger: %v", l)
	}
	if hits := w.snapshot(); len(hits) != 0 {
		t.Errorf("段缺失不该有线上活动: hits=%d", len(hits))
	}
}

// TestSdkAssumption_EmptyCredentialsSendUnsigned 锁住「把凭证升为必给」这条口径的
// 前提，判的是上游而不是本仓的映射：CLS 客户端的 validateOptions 里那段凭证校验是
// 注释掉的，Send 只在 SecretID 与 SecretKEY 都非空时才加 Authorization 头——所以
// 空凭证得到的不是「带空身份的签名」而是「一个完全未签名的请求」。
//
// 这条与 §7.25 的 aliyun 不同形（那里空 AK 仍能签出 "LOG :<sig>"），所以不能类推，
// 只能各判一次。绕过 bald 的构造期校验直接喂 SDK，是为了在本仓改动之后仍然能读出
// SDK 的真实行为；若上游补上凭证校验或改成签空身份，这里会红。
func TestSdkAssumption_EmptyCredentialsSendUnsigned(t *testing.T) {
	w, srv := newWire(t)
	host := strings.TrimPrefix(srv.URL, "http://")

	sendWith := func(t *testing.T, nth int, cred cls.Credentials) wireHit {
		t.Helper()
		client, clsErr := cls.NewCLSClient(&cls.Options{Host: host, Credentials: cred})
		if clsErr != nil {
			t.Fatalf("NewCLSClient: %v", clsErr)
		}
		if err := client.Send(context.Background(), gateTopic, &cls.LogGroup{}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		return w.waitHits(t, 10*time.Second, nth)
	}

	unsigned := sendWith(t, 1, cls.Credentials{})
	if got := unsigned.header.Get("Authorization"); got != "" {
		t.Fatalf("空凭证竟仍带了签名头，本条前提需要重写: %q", got)
	}

	// 对照：同一套代码在有凭证时确实会加 Authorization——否则「没有头」这条判据
	// 可能只是假线根本没收到请求。
	signed := sendWith(t, 2, cls.Credentials{SecretID: gateAK, SecretKEY: gateSK})
	if authField(signed.header.Get("Authorization"), "q-signature") == "" {
		t.Fatalf("有凭证时也没签出签名，对照不成立: %v", signed.header)
	}
}
