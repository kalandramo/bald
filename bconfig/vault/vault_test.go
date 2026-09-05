package vault

import (
	"context"
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
)

// newVaultStub 启动 httptest 服务器模拟 Vault KV v2 read API。
// content 须传指针：atomic.Pointer 按值拷贝会让闭包看到旧的内部指针。
func newVaultStub(t *testing.T, content *atomic.Pointer[[]byte]) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/secret/data/app", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"data":{"content":` + jsonString(*content.Load()) + `},"metadata":{"version":1}}}`))
	})
	return httptest.NewServer(mux)
}

// jsonString 把内容编码为合法 JSON 字符串字面量（含换行等控制字符转义）。
func jsonString(s []byte) string {
	if s == nil {
		return `""`
	}
	b, _ := stdjson.Marshal(string(s))
	return string(b)
}

// TestNew_ParameterValidation path 必填。
func TestNew_ParameterValidation(t *testing.T) {
	if _, err := New(); err == nil {
		t.Error("New() without path should fail")
	}
	if _, err := NewWithClient(nil, WithPath("secret/data/app")); err == nil {
		t.Error("NewWithClient(nil) should fail")
	}
	c, _ := vaultapi.NewClient(vaultapi.DefaultConfig())
	if _, err := NewWithClient(c); err == nil {
		t.Error("NewWithClient without path should fail")
	}
}

// TestLoad_KVv2 httptest 端到端：KV v2 剥壳 + content 提取。
func TestLoad_KVv2(t *testing.T) {
	var content atomic.Pointer[[]byte]
	v := []byte("log:\n  level: debug")
	content.Store(&v)

	srv := newVaultStub(t, &content)
	defer srv.Close()

	src, err := New(WithPath("secret/data/app"), WithAddress(srv.URL), WithToken("root"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	data, err := src.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "log:\n  level: debug" {
		t.Errorf("Load() = %q", data)
	}
}

// TestLoad_KVv1 KV v1 扁平 Data 直接提取。
func TestLoad_KVv1(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"content":"flat: v1"}}`))
	}))
	defer srv.Close()

	src, err := New(WithPath("secret/app"), WithAddress(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	data, err := src.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "flat: v1" {
		t.Errorf("Load() = %q", data)
	}
}

// TestWatchValue_Polling 轮询：初始值推送 + 内容变化推送新值。
func TestWatchValue_Polling(t *testing.T) {
	var content atomic.Pointer[[]byte]
	v1 := []byte("v: 1")
	content.Store(&v1)

	srv := newVaultStub(t, &content)
	defer srv.Close()

	src, err := New(WithPath("secret/data/app"), WithAddress(srv.URL), WithPollInterval(30*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := src.WatchValue(ctx, "")
	if err != nil {
		t.Fatalf("WatchValue: %v", err)
	}

	select {
	case data := <-ch:
		if string(data) != "v: 1" {
			t.Errorf("initial push = %q", data)
		}
	case <-ctx.Done():
		t.Fatal("no initial push")
	}

	v2 := []byte("v: 2")
	content.Store(&v2)
	select {
	case data := <-ch:
		if string(data) != "v: 2" {
			t.Errorf("change push = %q", data)
		}
	case <-ctx.Done():
		t.Fatal("no change push")
	}
}

// TestExtractValue 纯函数：v2 剥壳/dataKey 缺失兜底/复杂值 JSON。
func TestExtractValue(t *testing.T) {
	// KV v2 剥壳。
	v2 := map[string]any{"data": map[string]any{"content": "x"}, "metadata": map[string]any{}}
	if got := extractValue(v2, "content"); string(got) != "x" {
		t.Errorf("v2 = %q", got)
	}
	// KV v1 扁平。
	v1 := map[string]any{"content": "y"}
	if got := extractValue(v1, "content"); string(got) != "y" {
		t.Errorf("v1 = %q", got)
	}
	// dataKey 缺失 → 整体 JSON 兜底。
	miss := map[string]any{"other": "z"}
	if got := extractValue(miss, "content"); len(got) == 0 {
		t.Error("missing dataKey should marshal whole data")
	}
}
