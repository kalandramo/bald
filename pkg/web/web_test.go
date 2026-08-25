package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kalandramo/bald/pkg/errors"
)

// 示例业务 handler：演示 ApplyTo 挂载路由。
type userHandler struct{}

func (userHandler) Name() string { return "user" }

func (userHandler) ApplyTo(r *Router, mws ...Middleware) error {
	v1 := r.Group("/v1/users", mws...)
	v1.HandleFunc(http.MethodGet, "/{id}", func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		HandleQueryRequest(w, req, func(_ context.Context, req *struct {
			ID string `json:"id"`
		}) (any, error) {
			return map[string]string{"id": id}, nil
		})
	})
	return nil
}

func TestRouterGroupAndMiddleware(t *testing.T) {
	router := NewRouter(Recovery())
	_ = userHandler{}.ApplyTo(router)

	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/users/42")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHandleJSONRequestBindsAndErrors(t *testing.T) {
	type reqBody struct {
		Name string `json:"name"`
	}
	type respBody struct {
		Greet string `json:"greet"`
	}

	h := func(_ context.Context, r *reqBody) (respBody, error) {
		if r.Name == "" {
			return respBody{}, errors.BadRequest("EMPTY_NAME").WithMessage("name 不能为空")
		}
		return respBody{Greet: "hi " + r.Name}, nil
	}

	mux := NewRouter()
	mux.HandleFunc(http.MethodPost, "/greet", func(w http.ResponseWriter, req *http.Request) {
		HandleJSONRequest(w, req, h)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 成功路径。
	body, _ := json.Marshal(reqBody{Name: "kong"})
	resp, _ := http.Post(srv.URL+"/greet", "application/json", strings.NewReader(string(body)))
	if resp.StatusCode != 200 {
		t.Fatalf("success: expected 200, got %d", resp.StatusCode)
	}

	// 业务错误：应返回 400 + 结构化 ErrorResponse。
	resp2, _ := http.Post(srv.URL+"/greet", "application/json", strings.NewReader(`{"name":""}`))
	if resp2.StatusCode != 400 {
		t.Fatalf("error: expected 400, got %d", resp2.StatusCode)
	}
	var er ErrorResponse
	_ = json.NewDecoder(resp2.Body).Decode(&er)
	if er.Reason != "EMPTY_NAME" || er.Message != "name 不能为空" {
		t.Fatalf("error response wrong: %+v", er)
	}
}

// TestHandleJSONRequestRejectsMissingContentType 回归：curl -d 默认不设
// Content-Type（实为 application/x-www-form-urlencoded），但 body 是裸 JSON。
// 依"非法请求即拒绝"原则，JSON 绑定器应显式返回 400，而非静默跳过把锅甩给业务校验。
func TestHandleJSONRequestRejectsMissingContentType(t *testing.T) {
	type reqBody struct {
		Name string `json:"name"`
	}
	h := func(_ context.Context, r *reqBody) (map[string]string, error) {
		return map[string]string{"name": r.Name}, nil
	}
	mux := NewRouter()
	mux.HandleFunc(http.MethodPost, "/greet", func(w http.ResponseWriter, req *http.Request) {
		HandleJSONRequest(w, req, h)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 不传 Content-Type，模拟 curl -d '{"name":"bald"}'（默认 form-urlencoded）。
	resp, err := http.Post(srv.URL+"/greet", "", strings.NewReader(`{"name":"bald"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 (illegal request), got %d", resp.StatusCode)
	}
	// 必须返回结构化绑定错误，明确指出 Content-Type 问题。
	var got ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Reason != "BIND_ERROR" {
		t.Fatalf("expected reason BIND_ERROR, got %q", got.Reason)
	}
}

// TestHandleJSONRequestBindsWithJSONContentType 正面用例：显式带
// Content-Type: application/json 时正常绑定。
func TestHandleJSONRequestBindsWithJSONContentType(t *testing.T) {
	type reqBody struct {
		Name string `json:"name"`
	}
	h := func(_ context.Context, r *reqBody) (map[string]string, error) {
		return map[string]string{"name": r.Name}, nil
	}
	mux := NewRouter()
	mux.HandleFunc(http.MethodPost, "/greet", func(w http.ResponseWriter, req *http.Request) {
		HandleJSONRequest(w, req, h)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/greet", "application/json", strings.NewReader(`{"name":"bald"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["name"] != "bald" {
		t.Fatalf("expected name=bald, got %q", got["name"])
	}
}

func TestMiddlewareOrder(t *testing.T) {
	var order []string
	mw := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	router := NewRouter(mw("root"))
	g := router.Group("/x", mw("group"))
	g.HandleFunc(http.MethodGet, "/y", func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
		w.WriteHeader(200)
	}, mw("route"))

	srv := httptest.NewServer(router)
	defer srv.Close()
	_, _ = http.Get(srv.URL + "/x/y")

	want := []string{"root", "group", "route", "handler"}
	if len(order) != len(want) {
		t.Fatalf("expected %v, got %v", want, order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order[%d] = %s, want %s", i, order[i], want[i])
		}
	}
}
