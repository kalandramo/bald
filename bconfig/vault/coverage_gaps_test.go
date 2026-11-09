package vault

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
)

func TestNew_ClientError(t *testing.T) {
	if _, err := New(WithPath("secret/data/app"), WithAddress("://bad")); err == nil {
		t.Fatal("非法 address 时 New 应当返回建连错误")
	}
}

func TestNewWithClient_SuccessAndOptions(t *testing.T) {
	hc, err := vaultapi.NewClient(vaultapi.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	src, err := NewWithClient(hc, WithPath("secret/data/app"), WithDataKey("cfg"), WithContext(ctx))
	if err != nil {
		t.Fatalf("NewWithClient() = %v", err)
	}
	if src.client != hc || src.opts.path != "secret/data/app" || src.opts.dataKey != "cfg" {
		t.Errorf("src = %+v", src)
	}
	if got := src.resolveKey("override"); got != "override" {
		t.Errorf("resolveKey(override) = %q", got)
	}
}

func TestLoad_KeyOverrideAndErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/secret/data/app", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"data":{"content":"ok"},"metadata":{}}}`))
	})
	mux.HandleFunc("/v1/secret/data/bad", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/v1/secret/data/empty", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	src, err := New(WithPath("secret/data/app"), WithAddress(srv.URL), WithToken("root"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("key 覆盖默认 path", func(t *testing.T) {
		data, err := src.Load(context.Background(), "secret/data/app")
		if err != nil || string(data) != "ok" {
			t.Fatalf("data=%q err=%v", data, err)
		}
	})

	t.Run("服务端错误", func(t *testing.T) {
		if _, err := src.Load(context.Background(), "secret/data/bad"); err == nil {
			t.Fatal("500 应当返回读取错误")
		}
	})

	t.Run("secret 不存在返回 nil", func(t *testing.T) {
		data, err := src.Load(context.Background(), "secret/data/empty")
		if err != nil || data != nil {
			t.Fatalf("data=%v err=%v，期望 nil,nil", data, err)
		}
	})
}

// vaultContentStub 按请求次数返回脚本化内容；空内容视为非 JSON（读取报错），
// 不走 5xx —— vault 客户端对 5xx 会内置重试，拖慢轮询节奏。
type vaultContentStub struct {
	calls   atomic.Int32
	bodies  func(n int32) string
	onFetch func(n int32)
}

func (s *vaultContentStub) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	n := s.calls.Add(1)
	if s.onFetch != nil {
		s.onFetch(n)
	}
	if s.bodies == nil {
		return
	}
	_, _ = w.Write([]byte(s.bodies(n)))
}

func contentBody(v string) string {
	return `{"data":{"data":{"content":"` + v + `"},"metadata":{}}}`
}

func TestWatchValue_SameContentNotRepushed(t *testing.T) {
	srv := httptest.NewServer(&vaultContentStub{
		bodies: func(int32) string { return contentBody("same") },
	})
	defer srv.Close()

	src, err := New(WithPath("secret/data/app"), WithAddress(srv.URL), WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

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
		t.Fatalf("内容未变时不应重复推送: %q", data)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWatchValue_PollErrorContinues(t *testing.T) {
	stub := &vaultContentStub{bodies: func(n int32) string {
		switch {
		case n == 1:
			return contentBody("v1")
		case n < 4:
			return "not-json" // 解析失败 → 读取报错 → 继续轮询
		default:
			return contentBody("v2")
		}
	}}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	src, err := New(WithPath("secret/data/app"), WithAddress(srv.URL), WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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
		if string(data) != "v2" {
			t.Errorf("错误恢复后推送 = %q", data)
		}
	case <-ctx.Done():
		t.Fatal("错误恢复后没有推送新值")
	}
}

func TestWatchValue_CtxDoneWhilePollDispatch(t *testing.T) {
	stub := &vaultContentStub{bodies: func(n int32) string {
		if n == 1 {
			return contentBody("v1")
		}
		return contentBody(fmt.Sprintf("v%d", n))
	}}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	src, err := New(WithPath("secret/data/app"), WithAddress(srv.URL), WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := src.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	// 不消费初始值：缓冲已满，第二次轮询拿到新内容后会阻塞在派发 select 上。
	deadline := time.Now().Add(2 * time.Second)
	for stub.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
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

func TestExtractValue_EdgeCases(t *testing.T) {
	t.Run("dataKey 值为 []byte", func(t *testing.T) {
		if got := extractValue(map[string]any{"content": []byte("raw")}, "content"); string(got) != "raw" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("dataKey 值为复杂类型走 JSON", func(t *testing.T) {
		got := extractValue(map[string]any{"content": map[string]any{"a": 1}}, "content")
		if string(got) != `{"a":1}` {
			t.Errorf("got %q", got)
		}
	})

	t.Run("不可序列化时返回 nil", func(t *testing.T) {
		bad := map[string]any{"content": func() {}}
		if got := extractValue(bad, "content"); got != nil {
			t.Errorf("got %q，期望 nil", got)
		}
	})

	t.Run("data 层不是 map 时按扁平处理", func(t *testing.T) {
		got := extractValue(map[string]any{"data": "scalar"}, "content")
		if len(got) == 0 {
			t.Error("期望整体 JSON 兜底")
		}
	})
}

func TestBytesEqual(t *testing.T) {
	if !bytesEqual([]byte("ab"), []byte("ab")) {
		t.Error("相同内容应判等")
	}
	if bytesEqual([]byte("ab"), []byte("abc")) {
		t.Error("长度不同应判不等")
	}
	if bytesEqual([]byte("ab"), []byte("ac")) {
		t.Error("内容不同应判不等")
	}
}

// 结构性零覆盖（登记备查）：
//   config.go:122-127 —— 初始拉取的派发 select 里 ctx.Done 分支：此刻 out 缓冲必为空，
//   发送永远就绪；要让它被选中必须同时满足「ctx 已取消」与「请求无视 ctx 仍成功」，
//   只能靠 select 的随机选择命中，离线测试无法确定性复现。该分支是消费者已退出时的
//   快速返回护栏，保留。
