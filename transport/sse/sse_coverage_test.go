// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// sse_coverage_test.go 补齐 SSE 包离线可测分支：六个 0% 选项构造器、事件流读取器
// （NewEventStreamReader/ReadEvent/minPosInt/containsDoubleNewline/writeData 各错误
// 返回）、Event.clone(nil)/EventLog.Replay/StreamManager.Remove、marshalEvent 的
// codec-nil 与编码失败、process 的 base64 分支、Publish/Notify 系列的编码错误返回、
// auth/subscriber 取 token 的兜底分支，以及用可控 Flusher 写体直接驱动 ServeHTTP
// 的事件编码主循环（splitData、":" 前缀、event/retry/comment 字段、eventTTL 跳过、
// 空事件 break）。全部无需真实监听端口。

package sse

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kalandramo/bald/encoding"
)

// ---------------------------------------------------------------------------
// options
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
		Subject:      pkix.Name{CommonName: "sse-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile
}

func TestWithTLS(t *testing.T) {
	certFile, keyFile := tempTLSCertFiles(t)
	srv := NewServer(":0")
	WithTLS(certFile, keyFile)(srv)
	if srv.tlsConfig == nil || len(srv.tlsConfig.Certificates) == 0 {
		t.Fatal("WithTLS with valid files must load a certificate")
	}

	// 非法路径 → LoadX509KeyPair 失败 → panic 分支。
	srv2 := NewServer(":0")
	func() {
		defer func() {
			if recover() == nil {
				t.Error("WithTLS with bad paths should panic")
			}
		}()
		WithTLS("nope.pem", "nope-key.pem")(srv2)
	}()
}

func TestWithCodec(t *testing.T) {
	srv := NewServer(":0")
	WithCodec("json")(srv)
	if srv.codec == nil {
		t.Fatal("WithCodec(\"json\") should set a codec")
	}
	before := srv.codec
	WithCodec("")(srv) // 空名不改动
	if srv.codec != before {
		t.Error("WithCodec(\"\") must leave codec unchanged")
	}
}

func TestWithEventTTL(t *testing.T) {
	srv := NewServer(":0")
	WithEventTTL(3 * time.Second)(srv)
	if srv.eventTTL != 3*time.Second {
		t.Errorf("eventTTL = %v, want 3s", srv.eventTTL)
	}
}

func TestWithHeaders(t *testing.T) {
	srv := NewServer(":0")
	WithHeaders(map[string]string{"X-Trace": "abc"})(srv)
	if srv.headers["X-Trace"] != "abc" {
		t.Errorf("headers = %v, want X-Trace=abc", srv.headers)
	}
}

func TestWithSubscriberFunction(t *testing.T) {
	srv := NewServer(":0")
	called := false
	WithSubscriberFunction(func(StreamID, *Subscriber) { called = true })(srv)
	if srv.subscribeFunc == nil {
		t.Fatal("subscribeFunc not set")
	}
	srv.subscribeFunc("s", &Subscriber{})
	if !called {
		t.Error("subscribeFunc body did not run")
	}
}

func TestWithTokenExtractor(t *testing.T) {
	srv := NewServer(":0")
	WithTokenExtractor(func(*http.Request) string { return "tk" })(srv)
	if srv.tokenExtractor == nil {
		t.Fatal("tokenExtractor not set")
	}
	if got := srv.tokenExtractor(httptest.NewRequest(http.MethodGet, "/", nil)); got != "tk" {
		t.Errorf("tokenExtractor = %q, want tk", got)
	}
}

// ---------------------------------------------------------------------------
// event reader / helpers
// ---------------------------------------------------------------------------

// failingReader 读取即返回给定错误，用于驱动 ReadEvent 的错误分支。
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestEventStreamReader(t *testing.T) {
	r := NewEventStreamReader(strings.NewReader("data: a\n\ndata: b\n\n"), 8192)
	b1, err := r.ReadEvent()
	if err != nil || string(b1) != "data: a" {
		t.Fatalf("event1 = %q err=%v, want \"data: a\"", b1, err)
	}
	b2, err := r.ReadEvent()
	if err != nil || string(b2) != "data: b" {
		t.Fatalf("event2 = %q err=%v, want \"data: b\"", b2, err)
	}
	// 耗尽 → io.EOF。
	if _, err := r.ReadEvent(); !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got %v", err)
	}

	// scanner.Err()==context.Canceled → 归一化为 io.EOF。
	rc := NewEventStreamReader(failingReader{err: context.Canceled}, 1024)
	if _, err := rc.ReadEvent(); !errors.Is(err, io.EOF) {
		t.Errorf("canceled reader want io.EOF, got %v", err)
	}

	// scanner.Err()==其它错误 → 透传。
	boom := errors.New("boom")
	re := NewEventStreamReader(failingReader{err: boom}, 1024)
	if _, err := re.ReadEvent(); !errors.Is(err, boom) {
		t.Errorf("generic reader want %v, got %v", boom, err)
	}
}

// chunkedReader 每次只交付 1 字节，迫使 split 在未达事件边界时走 atEOF=false 分支。
type chunkedReader struct {
	s []byte
	i int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	p[0] = r.s[r.i]
	r.i++
	return 1, nil
}

func TestEventStreamReaderEdgePaths(t *testing.T) {
	// 尾部无分隔双换行 → atEOF 时返回残余数据（split 的 return len(data) 分支）。
	r := NewEventStreamReader(strings.NewReader("data: a\n\ntrailing"), 1024)
	first, err := r.ReadEvent()
	if err != nil || string(first) != "data: a" {
		t.Fatalf("first = %q err=%v", first, err)
	}
	tail, err := r.ReadEvent()
	if err != nil || string(tail) != "trailing" {
		t.Fatalf("tail = %q err=%v (want raw trailing at EOF)", tail, err)
	}

	// 逐字节喂入 → 未达 "\n\n" 时 split 返回 0,nil,nil（需更多数据）分支。
	c := NewEventStreamReader(&chunkedReader{s: []byte("x\n\ny\n\n")}, 16)
	b1, _ := c.ReadEvent()
	b2, _ := c.ReadEvent()
	if string(b1) != "x" || string(b2) != "y" {
		t.Errorf("chunked events = %q,%q want x,y", b1, b2)
	}
}

func TestMinPosInt(t *testing.T) {
	cases := []struct {
		a, b, want int
	}{
		{4096, 8192, 4096}, // a<b → a
		{8192, 4096, 4096}, // a>=b → b
		{3, 3, 3},          // 相等 → b
		{-1, 5, 0},         // 负 → 0
		{5, -1, 0},         // 负 → 0
	}
	for _, tc := range cases {
		if got := minPosInt(tc.a, tc.b); got != tc.want {
			t.Errorf("minPosInt(%d,%d)=%d want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestContainsDoubleNewline(t *testing.T) {
	if i, n := containsDoubleNewline([]byte("a\r\n\r\nb")); i != 1 || n != 4 {
		t.Errorf("CRLF: got (%d,%d) want (1,4)", i, n)
	}
	if i, n := containsDoubleNewline([]byte("a\n\nb")); i != 1 || n != 2 {
		t.Errorf("LF: got (%d,%d) want (1,2)", i, n)
	}
	if i, n := containsDoubleNewline([]byte("no newline")); i != -1 || n != 0 {
		t.Errorf("none: got (%d,%d) want (-1,0)", i, n)
	}
}

// failWriter 在第 failOn 次 Write 返回错误。
type failWriter struct {
	failOn int
	n      int
}

func (w *failWriter) Write(p []byte) (int, error) {
	w.n++
	if w.n == w.failOn {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func TestWriteDataBranches(t *testing.T) {
	// 成功：字段 + 数据 + 换行。
	var buf bytes.Buffer
	if _, err := writeData(&buf, []byte("id:"), []byte("x")); err != nil {
		t.Fatalf("success: %v", err)
	}
	if got := buf.String(); got != "id: x\n" {
		t.Errorf("writeData(id:, x) = %q, want \"id: x\\n\"", got)
	}
	// 空字段（注释路径）。
	buf.Reset()
	if _, err := writeData(&buf, nil, []byte("c")); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if buf.String() != "c\n" {
		t.Errorf("writeData(nil, c) = %q, want \"c\\n\"", buf.String())
	}
	// 字段以空格结尾：跳过补空格分支。
	buf.Reset()
	if _, err := writeData(&buf, []byte("data "), []byte("y")); err != nil {
		t.Fatalf("trailing-space field: %v", err)
	}

	// 各写入失败分支（field/space/data/newline）。
	for _, failOn := range []int{1, 2, 3, 4} {
		if _, err := writeData(&failWriter{failOn: failOn}, []byte("id:"), []byte("x")); err == nil {
			t.Errorf("failOn=%d: want error", failOn)
		}
	}
}

func TestEventClone(t *testing.T) {
	if (*Event)(nil).clone() != nil {
		t.Error("clone(nil) want nil")
	}
	e := &Event{Data: []byte("x"), ID: []byte("1")}
	cp := e.clone()
	if cp == e || string(cp.Data) != "x" {
		t.Errorf("clone returned %p (orig %p) data=%q", cp, e, cp.Data)
	}
}

// ---------------------------------------------------------------------------
// EventLog.Replay / StreamManager.Remove
// ---------------------------------------------------------------------------

func TestEventLogReplay(t *testing.T) {
	var log EventLog
	log.Add(&Event{Data: []byte("a")})
	log.Add(&Event{Data: []byte("b")})

	sub := &Subscriber{connection: make(chan *Event, 2)}
	log.Replay(sub)
	close(sub.connection)

	var got []string
	for ev := range sub.connection {
		got = append(got, string(ev.Data))
	}
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("replayed = %v, want [a b]", got)
	}
}

func TestStreamManagerRemove(t *testing.T) {
	mgr := NewStreamManager()

	s := newStream("x", 16, DefaultEventLogSize, true, false, nil, nil)
	s.run()
	mgr.Add(s)
	mgr.Remove(s) // 命中 → close + delete + return
	if mgr.Exist("x") {
		t.Error("stream still present after Remove")
	}

	// 未命中：循环遍历完不匹配，函数正常返回。
	other := newStream("y", 16, DefaultEventLogSize, true, false, nil, nil)
	other.run()
	mgr.Remove(other)
	if mgr.Count() != 0 {
		t.Errorf("count = %d, want 0", mgr.Count())
	}
}

// ---------------------------------------------------------------------------
// marshalEvent / process（直接构造 Server 覆盖内部编码分支）
// ---------------------------------------------------------------------------

func TestMarshalEventBranches(t *testing.T) {
	// data == nil → 返回空事件，无错误。
	empty, err := (&Server{}).marshalEvent(nil)
	if err != nil || empty == nil || len(empty.Data) != 0 {
		t.Errorf("nil payload: ev=%v err=%v", empty, err)
	}

	// codec 为 nil（绕过 NewServer 默认注入）→ 返回 codec-nil 错误。
	if _, err := (&Server{}).marshalEvent(map[string]string{"a": "b"}); err == nil {
		t.Error("nil codec must error")
	}

	// codec.Marshal 失败（chan 无法 JSON 编码）→ 返回编码错误。
	failing := &Server{codec: encoding.GetCodec("json")}
	if _, err := failing.marshalEvent(make(chan int)); err == nil {
		t.Error("unmarshalable payload must error")
	}
}

func TestProcessEncodeBase64(t *testing.T) {
	ev := (&Server{encodeBase64: true}).process(&Event{Data: []byte("hi")})
	if string(ev.Data) == "hi" {
		t.Error("encodeBase64=true should transform Data")
	}
	// 未开启时不改写。
	ev2 := (&Server{}).process(&Event{Data: []byte("hi")})
	if string(ev2.Data) != "hi" {
		t.Errorf("encodeBase64=false must leave Data, got %q", ev2.Data)
	}
}

// ---------------------------------------------------------------------------
// Publish/Notify 编码失败返回 + Notify*WithMeta 全链路
// ---------------------------------------------------------------------------

func TestPublishNotify_MetaAndErrors(t *testing.T) {
	srv := NewServer(":0", WithCodec("json"))
	srv.CreateStream("m")
	defer srv.RemoveStream("m")

	// 正常：带元数据发布/广播。
	if err := srv.PublishDataWithMeta(context.Background(), "m", map[string]string{"k": "v"}, WithEventName("e")); err != nil {
		t.Errorf("PublishDataWithMeta: %v", err)
	}
	if err := srv.NotifyDataWithEventName(context.Background(), "e", map[string]string{"k": "v"}); err != nil {
		t.Errorf("NotifyDataWithEventName: %v", err)
	}
	if err := srv.NotifyDataWithMeta(context.Background(), map[string]string{"k": "v"}, WithEventID("id1")); err != nil {
		t.Errorf("NotifyDataWithMeta: %v", err)
	}

	// 编码失败：chan 无法 JSON 编码 → 各入口返回错误。
	bad := make(chan int)
	if err := srv.PublishData(context.Background(), "m", bad); err == nil {
		t.Error("PublishData bad payload must error")
	}
	if err := srv.PublishDataWithMeta(context.Background(), "m", bad); err == nil {
		t.Error("PublishDataWithMeta bad payload must error")
	}
	if err := srv.NotifyData(context.Background(), bad); err == nil {
		t.Error("NotifyData bad payload must error")
	}
	if err := srv.NotifyDataWithMeta(context.Background(), bad); err == nil {
		t.Error("NotifyDataWithMeta bad payload must error")
	}
}

// ---------------------------------------------------------------------------
// auth / subscriber token 兜底分支
// ---------------------------------------------------------------------------

func TestExtractBearerTokenBranches(t *testing.T) {
	if got := extractBearerToken("Bearer abc"); got != "abc" {
		t.Errorf("bearer: got %q want abc", got)
	}
	if got := extractBearerToken("raw-token"); got != "raw-token" {
		t.Errorf("non-bearer: got %q want raw-token", got)
	}
	if got := extractBearerToken("short"); got != "short" {
		t.Errorf("short: got %q want short", got)
	}
	if got := extractBearerToken("   "); got != "" {
		t.Errorf("blank: got %q want empty", got)
	}
}

func TestSubscriberTokenFallbacks(t *testing.T) {
	// BearerToken 非 Bearer 前缀 → 原样返回。
	sub := &Subscriber{Header: http.Header{"Authorization": []string{"rawtoken"}}}
	if got := sub.BearerToken(); got != "rawtoken" {
		t.Errorf("BearerToken raw = %q, want rawtoken", got)
	}

	// Token 走 URL query（无 header token、无 bearer）。
	req := httptest.NewRequest(http.MethodGet, "/events?token=qtk", nil)
	sub2 := &Subscriber{URL: req.URL, Header: http.Header{}}
	if got := sub2.Token("X-Missing"); got != "qtk" {
		t.Errorf("Token query = %q, want qtk", got)
	}

	// Token 全空且 URL nil → 返回空串。
	sub3 := &Subscriber{Header: http.Header{}}
	if got := sub3.Token(""); got != "" {
		t.Errorf("Token empty = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// ServeHTTP 事件编码主循环（可控 Flusher 写体，直接调用不经网络）
// ---------------------------------------------------------------------------

// flushWriter 是并发安全、实现 http.Flusher 的最小 ResponseWriter。
type flushWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	header http.Header
	status int
}

func newFlushWriter() *flushWriter { return &flushWriter{header: http.Header{}} }

func (f *flushWriter) Header() http.Header { return f.header }

func (f *flushWriter) WriteHeader(code int) {
	f.mu.Lock()
	f.status = code
	f.mu.Unlock()
}

func (f *flushWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(p)
}

func (f *flushWriter) Flush() {}

func (f *flushWriter) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.String()
}

// serveAndDrain 运行 ServeHTTP 于后台：等订阅者就位后由 emit 发布事件，
// 最后发布空事件让主循环 break，join 后返回写体。
func serveAndDrain(t *testing.T, srv *Server, streamID StreamID, emit func(*Stream)) string {
	t.Helper()
	stream := srv.CreateStream(streamID)

	fw := newFlushWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/events?stream="+string(streamID), nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(fw, req)
		close(done)
	}()

	eventually(t, 2*time.Second, func() bool { return stream.getSubscriberCount() >= 1 })
	emit(stream)
	// 空事件触发主循环 break（len(Data)==0 && len(Comment)==0）。
	srv.Publish(context.Background(), streamID, &Event{})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ServeHTTP did not finish")
	}
	return fw.String()
}

func TestServeHTTP_FieldWrites(t *testing.T) {
	srv := NewServer(":0", WithHeaders(map[string]string{"X-Extra": "1"}))
	body := serveAndDrain(t, srv, "w", func(stream *Stream) {
		srv.Publish(context.Background(), stream.StreamID(), &Event{
			Data:    []byte("payload"),
			Event:   []byte("message"),
			Retry:   []byte("5000"),
			Comment: []byte("note"),
		})
	})
	for _, want := range []string{"data: payload", "event: message", "retry: 5000", "note"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestServeHTTP_SplitData(t *testing.T) {
	srv := NewServer(":0", WithSplitData(true))
	body := serveAndDrain(t, srv, "split", func(stream *Stream) {
		srv.Publish(context.Background(), stream.StreamID(), &Event{Data: []byte("line1\nline2")})
	})
	if !strings.Contains(body, "data: line1\n") || !strings.Contains(body, "data: line2\n") {
		t.Errorf("split data not written as two lines:\n%s", body)
	}
}

func TestServeHTTP_ColonPrefixedData(t *testing.T) {
	srv := NewServer(":0")
	body := serveAndDrain(t, srv, "colon", func(stream *Stream) {
		srv.Publish(context.Background(), stream.StreamID(), &Event{Data: []byte(":keepalive")})
	})
	if !strings.Contains(body, ":keepalive\n") {
		t.Errorf("colon-prefixed data not passed through:\n%s", body)
	}
}

func TestServeHTTP_TTLSkipsExpired(t *testing.T) {
	// 关闭回放让事件保留零时间戳；正 TTL 下 now > 零值+TTL → 跳过写体。
	srv := NewServer(":0", WithAutoReplay(false), WithEventTTL(time.Second))
	body := serveAndDrain(t, srv, "ttl", func(stream *Stream) {
		srv.Publish(context.Background(), stream.StreamID(), &Event{Data: []byte("should-be-skipped")})
	})
	if strings.Contains(body, "should-be-skipped") {
		t.Errorf("expired event (zero timestamp) should be skipped by TTL:\n%s", body)
	}
}

// ---------------------------------------------------------------------------
// ServeHTTP 前置分支：非 Flusher、Last-Event-ID
// ---------------------------------------------------------------------------

// plainWriter 不实现 http.Flusher → ServeHTTP 走 "Streaming unsupported!" 分支。
type plainWriter struct {
	header http.Header
	status int
}

func (p *plainWriter) Header() http.Header         { return p.header }
func (p *plainWriter) Write(b []byte) (int, error) { return len(b), nil }
func (p *plainWriter) WriteHeader(code int)        { p.status = code }

func TestServeHTTP_NonFlusherWriter(t *testing.T) {
	srv := NewServer(":0")
	pw := &plainWriter{header: http.Header{}}
	req := httptest.NewRequest(http.MethodGet, "/events?stream=x", nil)
	srv.ServeHTTP(pw, req) // 断言不 panic；命中 500 分支
	if pw.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for non-flusher writer", pw.status)
	}
}

func TestServeHTTP_LastEventID(t *testing.T) {
	srv := NewServer(":0", WithAutoReplay(true))
	stream := srv.CreateStream("leid")

	// 用固定的 Last-Event-ID 订阅触发回放路径（eventId 非空 → addSubscriber/replay）。
	// 不读取 stream.eventLog：该字段由 run 协程独占（EventLog 无锁），测试并发读会与
	// Add 的 append 竞争。事件是否已入日志取决于调度，但 replay() 分支恒被执行。
	srv.Publish(context.Background(), "leid", &Event{Data: []byte("hist")})

	fw := newFlushWriter()
	req := httptest.NewRequest(http.MethodGet, "/events?stream=leid", nil)
	req.Header.Set("Last-Event-ID", "0")
	done := make(chan struct{})
	go func() { srv.ServeHTTP(fw, req); close(done) }()

	eventually(t, 2*time.Second, func() bool { return stream.getSubscriberCount() >= 1 })
	srv.Publish(context.Background(), "leid", &Event{}) // 空事件让循环 break
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ServeHTTP did not finish")
	}
}

func TestServeHTTP_AutoStreamCleanup(t *testing.T) {
	// autoStream=true、autoReplay=false：取消后清理协程在订阅者归零时摘除流。
	srv := NewServer(":0", WithAutoStream(true), WithAutoReplay(false))
	stream := srv.CreateStream("ac") // 预置在映射里，确保 ServeHTTP 取到同一 stream

	fw := newFlushWriter()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events?stream=ac", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { srv.ServeHTTP(fw, req); close(done) }()

	eventually(t, 2*time.Second, func() bool { return stream.getSubscriberCount() >= 1 })
	// 关闭并摘除流：run 循环退出前同步把计数清 0，随后 ServeHTTP range 结束。
	srv.RemoveStream("ac")
	<-done

	// 取消触发清理协程：此时 getSubscriberCount()==0 → 走 RemoveWithID 分支。
	cancel()
	time.Sleep(100 * time.Millisecond)
}

// ---------------------------------------------------------------------------
func TestStart_ListenError(t *testing.T) {
	srv := NewServer("127.0.0.1:notaport")
	if err := srv.Start(context.Background()); err == nil {
		t.Fatal("Start with invalid addr must return listen error")
	}
}

func TestStart_TLSListener(t *testing.T) {
	srv := NewServer("127.0.0.1:0", WithTLSConfig(&tls.Config{}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	waitServerAddr(t, srv) // 确认已在 TLS 监听器上服务
}

func TestStop_AfterServerSet(t *testing.T) {
	srv := NewServer(":0")
	srv.server = &http.Server{} // 模拟已 Start
	if err := srv.Stop(context.Background()); err != nil {
		t.Errorf("Stop with a server set: %v", err)
	}
}

func TestEndpoint_NoPort(t *testing.T) {
	srv := NewServer("127.0.0.1") // 无端口 → SplitHostPort 失败
	ep := srv.Endpoint()
	if ep != "http://127.0.0.1" {
		t.Errorf("Endpoint = %q, want http://127.0.0.1", ep)
	}
}

func TestStart_ServeErrorPropagates(t *testing.T) {
	srv := NewServer("127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	waitServerAddr(t, srv)

	// 直接关闭底层监听器 → Serve 返回非 ErrServerClosed 错误 → Start 透传该错误。
	srv.stateMu.RLock()
	ln := srv.listener
	srv.stateMu.RUnlock()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Start should propagate Serve error on abrupt listener close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return")
	}
}

// ---------------------------------------------------------------------------
// Publish/Notify：流已关闭（quit 就绪、事件通道无接收方）→ 命中丢弃事件分支
// ---------------------------------------------------------------------------

func TestPublishNotify_ClosedStreamDrops(t *testing.T) {
	// bufferSize=0 → stream.event 无缓冲；关闭流后 run 循环退出，
	// select 仅剩 <-quit 就绪，Publish/Notify 走丢弃分支而不阻塞。
	srv := NewServer(":0", WithBufferSize(0))
	stream := srv.CreateStream("gone")
	stream.close()                    // quit 关闭；流仍在 mgr 映射中
	time.Sleep(50 * time.Millisecond) // 等 run 循环退出（消费完 quit）

	srv.Publish(context.Background(), "gone", &Event{Data: []byte("x")})
	if ok := srv.TryPublish(context.Background(), "gone", &Event{Data: []byte("x")}); ok {
		t.Error("TryPublish to closed/unbuffered stream should return false")
	}
	srv.Notify(context.Background(), &Event{Data: []byte("y")})
}

// ---------------------------------------------------------------------------
// Notify：流映射里的 nil 值触发防御分支
// ---------------------------------------------------------------------------

func TestNotify_SkipsNilStreamInMap(t *testing.T) {
	srv := NewServer(":0")
	srv.streamMgr.streams["ghost"] = nil // 直接注入 nil（绕过 Add 的守卫）
	srv.Notify(context.Background(), &Event{Data: []byte("x")})
	delete(srv.streamMgr.streams, "ghost")
}

// ---------------------------------------------------------------------------
// replay：慢订阅者写缓冲满 → 回放中断（同步驱动，不经 run 循环）
// ---------------------------------------------------------------------------

func TestReplay_SlowSubscriberDisconnect(t *testing.T) {
	var unsub int32
	stream := newStream("rp", 16, 0, true, false, nil, func(StreamID, *Subscriber) {
		atomic.AddInt32(&unsub, 1)
	})
	// 不 run()：直接填充事件日志并注册一个连接已写满的订阅者，同步调 replay。
	for i := 0; i < defaultSubscriberBuffer+3; i++ {
		stream.eventLog.Add(&Event{Data: []byte("e")})
	}
	sub := &Subscriber{
		connection: make(chan *Event, defaultSubscriberBuffer),
		eventId:    "",
	}
	for i := 0; i < defaultSubscriberBuffer; i++ {
		sub.connection <- &Event{} // 预填满缓冲，使 trySend 立即失败
	}
	stream.subscribers = append(stream.subscribers, sub)
	atomic.StoreInt32(&stream.subscriberCount, 1)

	stream.replay(sub)

	if i := stream.getSubIndex(sub); i != -1 {
		t.Error("slow subscriber not removed during replay")
	}
	eventually(t, time.Second, func() bool { return atomic.LoadInt32(&unsub) == 1 })
}

// ---------------------------------------------------------------------------
// onSubscribe / deregister-onUnsubscribe 回调
// ---------------------------------------------------------------------------

func TestStream_SubscribeAndUnsubscribeCallbacks(t *testing.T) {
	var joined, left int32
	srv := NewServer(":0",
		WithSubscriberFunction(func(StreamID, *Subscriber) { atomic.AddInt32(&joined, 1) }),
		WithUnSubscriberFunction(func(StreamID, *Subscriber) { atomic.AddInt32(&left, 1) }),
	)
	stream := srv.CreateStream("cb")
	defer srv.RemoveStream("cb")

	sub := stream.addSubscriber("", nil)
	eventually(t, time.Second, func() bool { return atomic.LoadInt32(&joined) == 1 })

	sub.close() // 正常注销 → run 循环 deregister 分支触发 onUnsubscribe
	eventually(t, time.Second, func() bool { return atomic.LoadInt32(&left) == 1 })
}

// ---------------------------------------------------------------------------
// StreamManager.Add：nil 与重复 ID 守卫
// ---------------------------------------------------------------------------

func TestStreamManagerAddGuards(t *testing.T) {
	mgr := NewStreamManager()
	mgr.Add(nil) // nil 守卫
	if mgr.Count() != 0 {
		t.Errorf("count after Add(nil) = %d, want 0", mgr.Count())
	}

	s1 := newStream("dup", 16, DefaultEventLogSize, true, false, nil, nil)
	s1.run()
	mgr.Add(s1)

	s2 := newStream("dup", 16, DefaultEventLogSize, true, false, nil, nil)
	s2.run()
	mgr.Add(s2) // 已存在同 ID → 直接返回，不覆盖
	if mgr.Get("dup") != s1 {
		t.Error("duplicate Add must not overwrite the existing stream")
	}
}
