package http3

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// serve 直接在进程内走 Server.ServeHTTP：覆盖 mux 路由、全局过滤链、
// handler 中间件与 context wrapper 的真实装配，不需要起 QUIC 监听。
func serve(srv *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestServerOptions_AllApplied(t *testing.T) {
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
	dec := func(*http.Request, any) error { return nil }
	enc := func(http.ResponseWriter, *http.Request, any) error { return nil }
	ene := func(http.ResponseWriter, *http.Request, error) error { return nil }

	srv := NewServer(
		WithTLSConfig(tlsConf),
		WithAddress("127.0.0.1:9999"),
		WithTimeout(3*time.Second),
		WithMiddleware(func(next Handler) Handler { return next }),
		WithFilter(func(h http.Handler) http.Handler { return h }),
		WithRequestDecoder(dec),
		WithResponseEncoder(enc),
		WithErrorEncoder(ene),
		WithStrictSlash(false),
	)

	if srv.Server.TLSConfig != tlsConf {
		t.Error("WithTLSConfig 未生效")
	}
	if srv.Addr != "127.0.0.1:9999" {
		t.Errorf("WithAddress = %q", srv.Addr)
	}
	if srv.timeout != 3*time.Second {
		t.Errorf("WithTimeout = %v", srv.timeout)
	}
	if len(srv.hms) != 1 || len(srv.filters) != 1 {
		t.Errorf("中间件/过滤器数量 = %d/%d", len(srv.hms), len(srv.filters))
	}
	if srv.strictSlash {
		t.Error("WithStrictSlash(false) 未生效")
	}
	// 用函数身份判断，避免比较函数值
	if srv.dec == nil || srv.enc == nil || srv.ene == nil {
		t.Error("编解码器未设置")
	}
}

func TestServerIdentity(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:9443"))
	if srv.Name() != KindHTTP3 {
		t.Errorf("Name() = %q", srv.Name())
	}
	if srv.Endpoint() != "127.0.0.1:9443" {
		t.Errorf("Endpoint() = %q", srv.Endpoint())
	}
}

// Route/GET + Context 方法在真实请求上的完整路径。
func TestServerRoute_Dispatch(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"))
	r := srv.Route("/api")
	r.GET("/users/{id}", func(c Context) error {
		var in struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		if err := c.BindVars(&in); err != nil {
			return err
		}
		if err := c.BindQuery(&in); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]any{"id": in.ID, "name": in.Name, "q": c.Query().Get("q")})
	})

	rec := serve(srv, httptest.NewRequest(http.MethodGet, "/api/users/3?name=ann&q=x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["id"] != float64(3) || got["name"] != "ann" || got["q"] != "x" {
		t.Errorf("body = %v", got)
	}
}

// 全局过滤器（WithFilter）+ 路由过滤器 + handler 中间件三层都必须执行。
func TestServerMiddlewareLayers(t *testing.T) {
	var (
		filterHits atomic.Int32
		routeHits  atomic.Int32
		mwHits     atomic.Int32
		httpMW     atomic.Int32
	)
	handlerMW := HandlerMiddleware(func(next Handler) Handler {
		return func(ctx context.Context, req any) (any, error) {
			mwHits.Add(1)
			return next(ctx, req)
		}
	})
	srv := NewServer(
		WithAddress("127.0.0.1:0"),
		WithMiddleware(handlerMW),
		WithFilter(func(h http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				filterHits.Add(1)
				h.ServeHTTP(w, r)
			})
		}),
	)
	// Use 在 NewServer 之后注册也必须生效（见 server.go Use 的约定）。
	srv.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpMW.Add(1)
			next.ServeHTTP(w, r)
		})
	})

	srv.Route("/").GET("/mw", func(c Context) error {
		h := c.Middleware(func(context.Context, any) (any, error) { return "ok", nil })
		if _, err := h(context.Background(), nil); err != nil {
			return err
		}
		return c.String(http.StatusOK, "ok")
	}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			routeHits.Add(1)
			h.ServeHTTP(w, r)
		})
	})

	rec := serve(srv, httptest.NewRequest(http.MethodGet, "/mw", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if filterHits.Load() != 1 || routeHits.Load() != 1 || mwHits.Load() != 1 || httpMW.Load() != 1 {
		t.Errorf("命中次数 filter=%d route=%d handlerMW=%d httpMW=%d，均应为 1",
			filterHits.Load(), routeHits.Load(), mwHits.Load(), httpMW.Load())
	}
}

// handler 返回错误时统一走 ErrorEncoder。
func TestServerHandlerError(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"))
	srv.Route("/").GET("/boom", func(c Context) error {
		return &statusError{code: http.StatusTeapot, msg: "teapot"}
	})

	rec := serve(srv, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("code = %d, want 418", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "teapot") {
		t.Errorf("body = %q", body)
	}
}

type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string   { return e.msg }
func (e *statusError) HTTPStatus() int { return e.code }

// filter 超时：>0 用 WithTimeout，<=0 用纯 Cancel（Deadline 不可用）。
func TestServerFilterTimeout(t *testing.T) {
	cases := []struct {
		name   string
		opts   []ServerOption
		wantDL bool
	}{
		{"默认 1s", nil, true},
		{"显式 0 关闭超时", []ServerOption{WithTimeout(0)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := NewServer(append(c.opts, WithAddress("127.0.0.1:0"))...)
			var gotDL bool
			srv.Route("/").GET("/dl", func(ctx Context) error {
				_, gotDL = ctx.Deadline()
				return ctx.String(http.StatusOK, "ok")
			})
			if rec := serve(srv, httptest.NewRequest(http.MethodGet, "/dl", nil)); rec.Code != http.StatusOK {
				t.Fatalf("code = %d", rec.Code)
			}
			if gotDL != c.wantDL {
				t.Errorf("Deadline ok = %v, want %v", gotDL, c.wantDL)
			}
		})
	}
}

// Server 级 Handle / HandlePrefix / HandleFunc / HandleHeader 四种挂载方式。
func TestServerHandlers(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"), WithStrictSlash(false))
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	srv.Handle("/exact", http.HandlerFunc(ok))
	srv.HandlePrefix("/static", http.HandlerFunc(ok))
	srv.HandleFunc("/func", ok)
	srv.HandleHeader("X-Api-Version", "v1", ok)

	cases := []struct {
		name   string
		method string
		target string
		header map[string]string
		want   int
	}{
		{"Handle 精确匹配", http.MethodGet, "/exact", nil, http.StatusNoContent},
		{"HandlePrefix 前缀匹配", http.MethodGet, "/static/a/b", nil, http.StatusNoContent},
		{"HandleFunc", http.MethodGet, "/func", nil, http.StatusNoContent},
		{"HandleHeader 命中", http.MethodGet, "/", map[string]string{"X-Api-Version": "v1"}, http.StatusNoContent},
		{"HandleHeader 未命中", http.MethodGet, "/", nil, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.target, nil)
			for k, v := range c.header {
				req.Header.Set(k, v)
			}
			if rec := serve(srv, req); rec.Code != c.want {
				t.Errorf("code = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

// 组路由前缀拼接 + Group 过滤器继承。
func TestRouterGroup(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"))
	root := srv.Route("/v1", func(h http.Handler) http.Handler { return h })
	grp := root.Group("users", func(h http.Handler) http.Handler { return h })
	grp.GET("list", func(c Context) error { return c.String(http.StatusOK, "list") })

	if rec := serve(srv, httptest.NewRequest(http.MethodGet, "/v1/users/list", nil)); rec.Code != http.StatusOK {
		t.Errorf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(grp.filters) != 2 {
		t.Errorf("Group 应继承父过滤器: %d", len(grp.filters))
	}
}

// 其余 HTTP 方法 helper 走同一 Handle 通道。
func TestRouterMethodHelpers(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"), WithStrictSlash(false))
	r := srv.Route("/")
	h := func(c Context) error { return c.String(http.StatusOK, c.Request().Method) }

	r.HEAD("/m", h)
	r.POST("/m", h)
	r.PUT("/m", h)
	r.PATCH("/m", h)
	r.DELETE("/m", h)
	r.CONNECT("/m", h)
	r.OPTIONS("/m", h)
	r.TRACE("/m", h)
	r.GET("/m", h)

	for _, m := range []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect,
		http.MethodOptions, http.MethodTrace,
	} {
		if rec := serve(srv, httptest.NewRequest(m, "/m", nil)); rec.Code != http.StatusOK {
			t.Errorf("%s /m = %d, want 200", m, rec.Code)
		}
	}
}

// StrictSlash(true) 时 "/x/" 对 "/x" 发 301；默认值即 true。
// 注意 Router.Handle 会 path.Join 去掉尾斜杠，这里用 Server.Handle 保留。
func TestServerStrictSlash(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"))
	srv.Handle("/x/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := serve(srv, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Errorf("code = %d, want 301", rec.Code)
	}
}

// 监听失败（非法地址）→ Start 返回错误并记录日志。
func TestServerStart_BindError(t *testing.T) {
	srv := NewServer(WithAddress("bad::addr"))
	if err := srv.Start(context.Background()); err == nil {
		t.Fatal("非法地址应返回错误")
	}
}

// Close 后再 Start：ListenAndServe 返回 http.ErrServerClosed，Start 吞掉并返回 nil。
func TestServerStart_ClosedServer(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"))
	if err := srv.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Errorf("Start on closed server = %v, want nil", err)
	}
}

// 未启动即 Stop 也应安全返回。
func TestServerStop_NotStarted(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"))
	if err := srv.Stop(context.Background()); err != nil {
		t.Errorf("Stop = %v, want nil", err)
	}
}

// 默认构造函数必须生成可用的自签 TLS 上下文。
func TestServerDefaultTLSConfig(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"))
	if srv.Server.TLSConfig == nil || len(srv.Server.TLSConfig.Certificates) != 1 {
		t.Fatal("默认 TLS 配置缺少证书")
	}
	if got := srv.Server.TLSConfig.NextProtos; len(got) != 1 || got[0] != "h3" {
		t.Errorf("NextProtos = %v, want [h3]", got)
	}
}

func TestDefaultCodecs(t *testing.T) {
	t.Run("RequestDecoder 空体", func(t *testing.T) {
		if err := DefaultRequestDecoder(httptest.NewRequest(http.MethodGet, "/", nil), &struct{}{}); err != nil {
			t.Errorf("空体应返回 nil: %v", err)
		}
	})
	t.Run("RequestDecoder JSON", func(t *testing.T) {
		var dst struct {
			N int `json:"n"`
		}
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"n":5}`))
		if err := DefaultRequestDecoder(req, &dst); err != nil || dst.N != 5 {
			t.Errorf("dec: err=%v n=%d", err, dst.N)
		}
	})
	t.Run("RequestDecoder 非法 JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{`))
		if err := DefaultRequestDecoder(req, &struct{}{}); err == nil {
			t.Error("非法 JSON 应报错")
		}
	})
	t.Run("ResponseEncoder", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := DefaultResponseEncoder(rec, nil, map[string]int{"n": 1}); err != nil {
			t.Fatalf("enc: %v", err)
		}
		if rec.Header().Get("Content-Type") != "application/json" || !strings.Contains(rec.Body.String(), `"n":1`) {
			t.Errorf("resp = %q %q", rec.Header().Get("Content-Type"), rec.Body.String())
		}
	})
	t.Run("ErrorEncoder 默认 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := DefaultErrorEncoder(rec, nil, errors.New("plain")); err != nil {
			t.Fatalf("ene: %v", err)
		}
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "plain") {
			t.Errorf("code = %d body = %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("ErrorEncoder 自定义状态", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := DefaultErrorEncoder(rec, nil, &statusError{code: http.StatusUnauthorized, msg: "no"}); err != nil {
			t.Fatalf("ene: %v", err)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("code = %d, want 401", rec.Code)
		}
	})
}

// ChainHandler / FilterChain 顺序：先注册的在外层。
func TestChains_Order(t *testing.T) {
	var order []string
	mw := func(name string) HandlerMiddleware {
		return func(next Handler) Handler {
			return func(ctx context.Context, req any) (any, error) {
				order = append(order, name)
				return next(ctx, req)
			}
		}
	}
	h := ChainHandler(mw("a"), mw("b"))(func(context.Context, any) (any, error) {
		order = append(order, "h")
		return nil, nil
	})
	if _, err := h(context.Background(), nil); err != nil {
		t.Fatalf("chain: %v", err)
	}
	if strings.Join(order, ",") != "a,b,h" {
		t.Errorf("order = %v", order)
	}

	order = nil
	f := func(name string) FilterFunc {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	FilterChain(f("a"), f("b"))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		order = append(order, "h")
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Join(order, ",") != "a,b,h" {
		t.Errorf("filter order = %v", order)
	}
}

// 结构性零覆盖（登记备查）：
//   - server.go generateTLSConfig 的三处 panic：rsa.GenerateKey /
//     x509.CreateCertificate / tls.X509KeyPair 的入参全部由本函数内部即时生成，
//     只有在 crypto/rand 熵源损坏时才会失败；无法在不注入故障的前提下触达。
//     成功路径由 TestServerDefaultTLSConfig 真实覆盖。
