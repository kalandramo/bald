// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/berrors"
)

func newTestEngine(route string, m Middleware, terminal gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET(route, GinHandler(m), terminal)
	return r
}

func doRequest(r *gin.Engine, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	return w
}

// TestGinHandlerInjectsTransporter 桥向 ctx 注入 Transporter，且统一链里读到
// 的 Operation 是路由模板、终值状态由终端位置回写。
func TestGinHandlerInjectsTransporter(t *testing.T) {
	var seenOp string
	var seenStatus int
	m := func(next Handler) Handler {
		return func(ctx context.Context, _ interface{}) (interface{}, error) {
			tr, ok := FromServerContext(ctx)
			if !ok {
				t.Fatal("transporter not injected")
			}
			seenOp = tr.Operation()
			seenStatus = tr.StatusCode()
			_, err := next(ctx, nil)
			seenStatus = tr.StatusCode() // 终端写出后的终值
			return nil, err
		}
	}
	terminal := func(c *gin.Context) { c.Status(http.StatusOK) }

	r := newTestEngine("/v1/users/:userID", m, terminal)
	w := doRequest(r, "/v1/users/usr-123")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if seenOp != "GET /v1/users/:userID" {
		t.Errorf("Operation = %q（必须取路由模板，raw path 只进 span）", seenOp)
	}
	if seenStatus != http.StatusOK {
		t.Errorf("terminal status observed = %d", seenStatus)
	}
}

// TestGinHandlerUnmatchedOperation 无路由命中时 operation 归入有界的
// "METHOD unmatched"——扫描器试探的 URL 不生成新指标序列。
func TestGinHandlerUnmatchedOperation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var seenOp string
	// gin 的 no-route 链默认 404；挂全局 GinHandler 桥观察 operation。
	r.NoRoute(GinHandler(func(next Handler) Handler {
		return func(ctx context.Context, _ interface{}) (interface{}, error) {
			tr, _ := FromServerContext(ctx)
			seenOp = tr.Operation()
			_, err := next(ctx, nil)
			return nil, err
		}
	}), func(c *gin.Context) { c.Status(http.StatusNotFound) })

	w := doRequest(r, "/nothing/matches/here")
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d", w.Code)
	}
	if seenOp != "GET unmatched" {
		t.Errorf("Operation = %q, want %q", seenOp, "GET unmatched")
	}
}

// TestGinHandlerShortCircuitRendersStatus 中间件短路（未走 next）时按错误的
// 传输类别渲染状态码与 google.rpc.Status JSON 同形包络（errors-r08 线型）。
func TestGinHandlerShortCircuitRendersStatus(t *testing.T) {
	shortCircuit := func(next Handler) Handler {
		return func(ctx context.Context, _ interface{}) (interface{}, error) {
			return nil, berrors.ResourceExhausted("ResourceExhausted.TooManyRequests")
		}
	}
	r := newTestEngine("/v1/thing", shortCircuit, func(c *gin.Context) { c.Status(http.StatusOK) })

	w := doRequest(r, "/v1/thing")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", w.Code)
	}
	for _, key := range []string{"code", "message", "details"} {
		if !containsKey(w.Body.String(), key) {
			t.Errorf("body missing %q: %s", key, w.Body.String())
		}
	}
}

// TestGinHandlerNoDoubleWrite next 已写出响应后，状态派生错误不得触发第二次
// 写（HTTP 只允许一个状态码——桥的 Writer.Written 防线）。
func TestGinHandlerNoDoubleWrite(t *testing.T) {
	terminal := func(c *gin.Context) { c.Status(http.StatusOK) }
	m := func(next Handler) Handler {
		return func(ctx context.Context, _ interface{}) (interface{}, error) {
			_, err := next(ctx, nil) // 200 已写出，状态派生错误非 nil
			return nil, err          // 桥不得再渲染
		}
	}
	r := newTestEngine("/v1/thing", m, terminal)

	w := doRequest(r, "/v1/thing")
	if w.Code != http.StatusOK {
		t.Errorf("double write changed code: %d", w.Code)
	}
}

// TestHTTPStatusErrorMapping 钉住状态→Reason 映射与低状态返回 nil。
func TestHTTPStatusErrorMapping(t *testing.T) {
	if httpStatusError(http.StatusOK) != nil || httpStatusError(http.StatusMovedPermanently) != nil {
		t.Error("non-4xx/5xx statuses must map to nil")
	}
	for status, reason := range map[int]string{
		http.StatusBadRequest:      "InvalidArgument",
		http.StatusUnauthorized:    "Unauthenticated",
		http.StatusTooManyRequests: "ResourceExhausted.TooManyRequests",
		http.StatusBadGateway:      "InternalError",
	} {
		e := httpStatusError(status)
		if e == nil {
			t.Fatalf("status %d: nil", status)
		}
		werr, _ := berrors.FromError(e)
		if werr.Reason != reason {
			t.Errorf("status %d reason = %q, want %q", status, werr.Reason, reason)
		}
	}
}

// TestErrorIsFlyweight 相同状态重复观测返回同一实例（享元：热路径不分配）。
func TestErrorIsFlyweight(t *testing.T) {
	a, b := httpStatusError(http.StatusNotFound), httpStatusError(http.StatusNotFound)
	if a != b {
		t.Error("404 sentinel should be a shared flyweight")
	}
}

// containsKey 是测试专用的 JSON 键检查（保持断言轻，不引解码器）。
func containsKey(body, key string) bool {
	return len(body) > 0 && indexOf(body, `"`+key+`"`) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
