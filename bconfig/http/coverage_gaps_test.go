package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failingBody struct{ err error }

func (b failingBody) Read([]byte) (int, error) { return 0, b.err }
func (b failingBody) Close() error             { return nil }

func fakeResponse(status int, body io.Reader) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(body),
	}
}

func TestNew_WithInjectedClient(t *testing.T) {
	hc := &http.Client{}
	src, err := New(WithURL("http://cfg.invalid"), WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	if src.client != hc || src.owned {
		t.Errorf("client=%p owned=%v，期望使用注入 client 且不接管关闭", src.client, src.owned)
	}
	if err := src.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
}

func TestNewWithClient_Success(t *testing.T) {
	hc := &http.Client{}
	src, err := NewWithClient(hc, WithURL("http://cfg.invalid"), WithHeader("k", "v"))
	if err != nil {
		t.Fatalf("NewWithClient() = %v", err)
	}
	if src.client != hc || src.opts.url != "http://cfg.invalid" || src.opts.headers["k"] != "v" {
		t.Errorf("src = %+v", src)
	}
}

func TestLoad_KeyOverrideAndBuildError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	defer srv.Close()

	src, err := New(WithURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	data, err := src.Load(context.Background(), srv.URL+"/override")
	if err != nil {
		t.Fatalf("Load(key) = %v", err)
	}
	if string(data) != "/override" {
		t.Errorf("Load(key) = %q，期望 key 覆盖默认 url", data)
	}

	if _, err := src.Load(context.Background(), "://bad"); err == nil ||
		!strings.Contains(err.Error(), "create request") {
		t.Errorf("Load(bad key) = %v，期望构造请求错误", err)
	}
}

func TestLoad_DoErrorRedactsUserinfo(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial boom")
	})}
	src, err := NewWithClient(hc, WithURL("http://user:s3cret@cfg.invalid/conf"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = src.Load(context.Background(), "")
	if err == nil {
		t.Fatal("请求失败时 Load 应当报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "REDACTED") {
		t.Errorf("错误未脱敏 userinfo: %v", err)
	}
	if strings.Contains(msg, "s3cret") {
		t.Errorf("错误泄漏了密码: %v", err)
	}
}

func TestLoad_StatusAndBodies(t *testing.T) {
	t.Run("304 返回 nil", func(t *testing.T) {
		hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return fakeResponse(http.StatusNotModified, strings.NewReader("")), nil
		})}
		src, _ := NewWithClient(hc, WithURL("http://cfg.invalid"))
		data, err := src.Load(context.Background(), "")
		if err != nil || data != nil {
			t.Fatalf("data=%v err=%v，期望 nil,nil", data, err)
		}
	})

	t.Run("读取 body 失败", func(t *testing.T) {
		boom := errors.New("read boom")
		hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			resp := fakeResponse(http.StatusOK, nil)
			resp.Body = failingBody{err: boom}
			return resp, nil
		})}
		src, _ := NewWithClient(hc, WithURL("http://cfg.invalid"))
		if _, err := src.Load(context.Background(), ""); !errors.Is(err, boom) {
			t.Fatalf("Load() = %v，期望读取错误", err)
		}
	})
}

func TestFetchWithETag_ErrorPaths(t *testing.T) {
	src, _ := NewWithClient(&http.Client{}, WithURL("http://cfg.invalid"))

	if _, _, err := src.fetchWithETag(context.Background(), "://bad", ""); err == nil {
		t.Error("非法 URL 应当构造请求失败")
	}

	boom := errors.New("read boom")
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		resp := fakeResponse(http.StatusInternalServerError, strings.NewReader(""))
		resp.Header.Set("ETag", `"e"`)
		return resp, nil
	})}
	src, _ = NewWithClient(hc, WithURL("http://cfg.invalid"))
	if _, etag, err := src.fetchWithETag(context.Background(), "http://cfg.invalid", ""); err == nil || etag != `"e"` {
		t.Errorf("etag=%q err=%v，期望非 2xx 报错且保留 ETag", etag, err)
	}

	hc = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		resp := fakeResponse(http.StatusOK, nil)
		resp.Body = failingBody{err: boom}
		return resp, nil
	})}
	src, _ = NewWithClient(hc, WithURL("http://cfg.invalid"))
	if _, _, err := src.fetchWithETag(context.Background(), "http://cfg.invalid", ""); !errors.Is(err, boom) {
		t.Errorf("fetchWithETag() = %v，期望读取错误", err)
	}
}

func TestRedactURL(t *testing.T) {
	if got := redactURL("://bad"); got != "://bad" {
		t.Errorf("非法 URL 应原样返回，得到 %q", got)
	}
	if got := redactURL("http://host/path"); got != "http://host/path" {
		t.Errorf("无 userinfo 应原样返回，得到 %q", got)
	}
	got := redactURL("http://user:pass@host/path")
	if got != "http://REDACTED@host/path" {
		t.Errorf("redactURL() = %q", got)
	}
}

func TestWatchValue_NoETagSameBodyNotRepushed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("same"))
	}))
	defer srv.Close()

	src, err := New(WithURL(srv.URL), WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, err := src.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if data := <-ch; string(data) != "same" {
		t.Fatalf("初始推送 = %q", data)
	}

	select {
	case data := <-ch:
		t.Fatalf("无 ETag 且内容未变时不应重复推送: %q", data)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWatchValue_NoETagBodyChangeRepushed(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		_, _ = fmt.Fprintf(w, "v%d", n)
	}))
	defer srv.Close()

	src, err := New(WithURL(srv.URL), WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, err := src.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if data := <-ch; string(data) != "v1" {
		t.Fatalf("初始推送 = %q", data)
	}
	select {
	case data := <-ch:
		if string(data) == "v1" {
			t.Errorf("内容变化后应推送新值，得到 %q", data)
		}
	case <-ctx.Done():
		t.Fatal("内容变化后没有推送")
	}
}

func TestWatchValue_CtxDoneWhilePollDispatch(t *testing.T) {
	var calls atomic.Int32
	secondFetch := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 2 {
			secondFetch <- struct{}{}
		}
		_, _ = fmt.Fprintf(w, "v%d", n)
	}))
	defer srv.Close()

	src, err := New(WithURL(srv.URL), WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := src.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	// 不消费初始值：缓冲已满，第二次轮询拿到新内容后会阻塞在派发 select 上。
	<-secondFetch
	time.Sleep(50 * time.Millisecond)
	cancel()

	var got []string
	for data := range ch {
		got = append(got, string(data))
	}
	if len(got) != 1 || got[0] != "v1" {
		t.Errorf("派发 = %v，期望仅初始值", got)
	}
}

// 结构性零覆盖（登记备查）：
//   http.go:149-152 —— 初始拉取的派发 select 里 ctx.Done 分支：此刻 out 缓冲必为空，
//   发送永远就绪；要让它被选中必须同时满足「ctx 已取消」与「请求无视 ctx 仍成功」，
//   只能靠 select 的随机选择命中，离线测试无法确定性复现。该分支是消费者已退出时的
//   快速返回护栏，保留。
