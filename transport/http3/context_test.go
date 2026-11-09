package http3

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// failingWriter 让底层 Write 失败，覆盖编码返回错误的路径。
type failingWriter struct {
	header http.Header
	code   int
}

func (w *failingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *failingWriter) WriteHeader(code int) { w.code = code }

func (w *failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write boom")
}

func newWrapperForTest() *wrapper {
	srv := NewServer(WithAddress("127.0.0.1:0"))
	r := srv.Route("/")
	req := httptest.NewRequest(http.MethodPost, "/p?q=1", strings.NewReader("f=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = mux.SetURLVars(req, map[string]string{"id": "7"})
	rec := httptest.NewRecorder()
	w := &wrapper{router: r}
	w.Reset(rec, req)
	return w
}

func TestWrapper_Accessors(t *testing.T) {
	w := newWrapperForTest()

	if got := w.Header().Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Errorf("Header() = %q", got)
	}
	if got := w.Vars().Get("id"); got != "7" {
		t.Errorf("Vars() = %q", got)
	}
	if got := w.Query().Get("q"); got != "1" {
		t.Errorf("Query() = %q", got)
	}
	if got := w.Form().Get("f"); got != "2" {
		t.Errorf("Form() = %q", got)
	}
	if w.Request() != w.req {
		t.Error("Request() 未返回原始请求")
	}
	if w.Response() != w.res {
		t.Error("Response() 未返回原始 writer")
	}

	called := false
	h := w.Middleware(func(context.Context, any) (any, error) {
		called = true
		return nil, nil
	})
	if _, err := h(context.Background(), nil); err != nil || !called {
		t.Errorf("Middleware 包装后调用异常: err=%v called=%v", err, called)
	}
}

// Form() 在 ParseForm 失败时返回空集合而不是 panic。
func TestWrapper_FormParseError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("a=%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := &wrapper{router: NewServer(WithAddress("127.0.0.1:0")).Route("/"), req: req}

	if got := w.Form(); len(got) != 0 {
		t.Errorf("Form() = %v, want empty", got)
	}
}

func TestWrapper_BindFamily(t *testing.T) {
	t.Run("BindVars", func(t *testing.T) {
		w := newWrapperForTest()
		var dst struct {
			ID int `json:"id"`
		}
		if err := w.BindVars(&dst); err != nil {
			t.Fatalf("BindVars: %v", err)
		}
		if dst.ID != 7 {
			t.Errorf("ID = %d, want 7", dst.ID)
		}
	})

	t.Run("BindQuery", func(t *testing.T) {
		w := newWrapperForTest()
		var dst struct {
			Q string `json:"q"`
		}
		if err := w.BindQuery(&dst); err != nil {
			t.Fatalf("BindQuery: %v", err)
		}
		if dst.Q != "1" {
			t.Errorf("Q = %q, want 1", dst.Q)
		}
	})

	t.Run("BindForm", func(t *testing.T) {
		w := newWrapperForTest()
		var dst struct {
			F string `json:"f"`
		}
		if err := w.BindForm(&dst); err != nil {
			t.Fatalf("BindForm: %v", err)
		}
		if dst.F != "2" {
			t.Errorf("F = %q, want 2", dst.F)
		}
	})

	t.Run("Bind", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"v":"x"}`))
		rec := httptest.NewRecorder()
		w := &wrapper{router: NewServer(WithAddress("127.0.0.1:0")).Route("/")}
		w.Reset(rec, req)
		var dst struct {
			V string `json:"v"`
		}
		if err := w.Bind(&dst); err != nil {
			t.Fatalf("Bind: %v", err)
		}
		if dst.V != "x" {
			t.Errorf("V = %q, want x", dst.V)
		}
	})
}

func TestWrapper_Returns(t *testing.T) {
	w := newWrapperForTest()
	boom := errors.New("boom")
	if err := w.Returns(nil, boom); !errors.Is(err, boom) {
		t.Errorf("Returns 应原样返回错误, got %v", err)
	}
	if err := w.Returns(map[string]string{"ok": "1"}, nil); err != nil {
		t.Fatalf("Returns: %v", err)
	}

	rec := w.res.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"ok":"1"`) {
		t.Errorf("body = %q", body)
	}
}

func TestWrapper_Result(t *testing.T) {
	w := newWrapperForTest()
	if err := w.Result(http.StatusBadRequest, map[string]string{"e": "bad"}); err != nil {
		t.Fatalf("Result: %v", err)
	}
	rec := w.res.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"e":"bad"`) {
		t.Errorf("body = %q", body)
	}
}

type xmlPayload struct {
	XMLName xml.Name `xml:"payload"`
	A       string   `xml:"a"`
}

func TestWrapper_Writers(t *testing.T) {
	ctxCases := []struct {
		name     string
		write    func(Context) error
		wantCode int
		wantType string
		wantBody string
	}{
		{"JSON", func(c Context) error { return c.JSON(201, map[string]int{"n": 1}) }, 201, "application/json", `{"n":1}`},
		{"XML", func(c Context) error { return c.XML(202, xmlPayload{A: "v"}) }, 202, "application/xml", "<a>v</a>"},
		{"String", func(c Context) error { return c.String(203, "plain") }, 203, "text/plain", "plain"},
		{"Blob", func(c Context) error { return c.Blob(207, "application/octet-stream", []byte("bin")) }, 207, "application/octet-stream", "bin"},
		{"Stream", func(c Context) error { return c.Stream(206, "text/plain", strings.NewReader("stream")) }, 206, "text/plain", "stream"},
	}
	for _, c := range ctxCases {
		t.Run(c.name, func(t *testing.T) {
			w := newWrapperForTest()
			if err := c.write(w); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			rec := w.res.(*httptest.ResponseRecorder)
			if rec.Code != c.wantCode {
				t.Errorf("code = %d, want %d", rec.Code, c.wantCode)
			}
			if got := rec.Header().Get("Content-Type"); got != c.wantType {
				t.Errorf("Content-Type = %q, want %q", got, c.wantType)
			}
			if !strings.Contains(rec.Body.String(), c.wantBody) {
				t.Errorf("body = %q, want contains %q", rec.Body.String(), c.wantBody)
			}
		})
	}
}

// 底层 writer 失败时 String/Blob/Stream/Result 必须把错误传回调用方。
func TestWrapper_WriterErrors(t *testing.T) {
	fw := &failingWriter{}
	w := &wrapper{router: NewServer(WithAddress("127.0.0.1:0")).Route("/"), res: fw}
	w.Reset(fw, httptest.NewRequest(http.MethodGet, "/", nil))

	if err := w.String(200, "x"); err == nil {
		t.Error("String 应返回写错误")
	}
	if err := w.Blob(200, "x/y", []byte("x")); err == nil {
		t.Error("Blob 应返回写错误")
	}
	if err := w.Stream(200, "x/y", strings.NewReader("x")); err == nil {
		t.Error("Stream 应返回写错误")
	}
	if err := w.Result(200, map[string]string{"a": "b"}); err == nil {
		t.Error("Result 经编码器应返回写错误")
	}
	if err := w.Returns(map[string]string{"a": "b"}, nil); err == nil {
		t.Error("Returns 经编码器应返回写错误")
	}
}

func TestWrapper_Reset(t *testing.T) {
	w := newWrapperForTest()
	req := httptest.NewRequest(http.MethodGet, "/next", nil)
	rec := httptest.NewRecorder()
	w.Reset(rec, req)

	if w.Request() != req || w.Response() != rec {
		t.Error("Reset 未替换请求/响应")
	}
	if w.w.code != http.StatusOK || w.w.w != rec {
		t.Errorf("内部 responseWriter 未复位: code=%d", w.w.code)
	}
	// Reset(nil, nil) 用于归还对象池前的清理
	w.Reset(nil, nil)
	if err := w.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("nil 请求时 Err() = %v, want context.Canceled", err)
	}
}

func TestWrapper_ContextInterface(t *testing.T) {
	t.Run("real request", func(t *testing.T) {
		type key struct{}
		ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), key{}, "v"), time.Minute)
		defer cancel()
		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
		w := &wrapper{req: req}

		if _, ok := w.Deadline(); !ok {
			t.Error("Deadline 应报告存在")
		}
		if w.Done() == nil {
			t.Error("Done 应为非 nil channel")
		}
		if err := w.Err(); err != nil {
			t.Errorf("Err() = %v, want nil", err)
		}
		if got := w.Value(key{}); got != "v" {
			t.Errorf("Value() = %v, want v", got)
		}
	})

	t.Run("nil request", func(t *testing.T) {
		w := &wrapper{}
		if _, ok := w.Deadline(); ok {
			t.Error("nil 请求 Deadline 应为零值 false")
		}
		if w.Done() != nil {
			t.Error("nil 请求 Done 应为 nil")
		}
		if err := w.Err(); !errors.Is(err, context.Canceled) {
			t.Errorf("Err() = %v, want context.Canceled", err)
		}
		if got := w.Value("k"); got != nil {
			t.Errorf("Value() = %v, want nil", got)
		}
	})
}
