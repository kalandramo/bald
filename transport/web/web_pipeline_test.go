// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// web_pipeline_test.go 覆盖 handler.go / binder.go / response.go 的纯流水线分支：
// 四个 Handle*Request 入口、ShouldBindAll 多源合并 + hasBody + Defaulter、
// runValidators、WriteResponse/isEmpty/StatusOf。全部用 gin.CreateTestContext +
// httptest 请求驱动，无需真实监听端口。

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/berrors"
)

type mergeReq struct {
	Name string `json:"name" uri:"name" form:"name"`
	N    int    `json:"n" uri:"n" form:"n"`
}

type respT struct {
	Msg string `json:"msg"`
}

// ctxWith 构造带 method/target/body/uri-params 的 gin 上下文。
func ctxWith(t *testing.T, method, target, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	c.Request = r
	c.Params = params
	return c, w
}

func okResp(req *mergeReq) func(context.Context, *mergeReq) (respT, error) {
	return func(_ context.Context, _ *mergeReq) (respT, error) { return respT{Msg: "ok"}, nil }
}

func TestHandleJSONRequestBranches(t *testing.T) {
	// 成功：body 绑定 + 空校验器列表 + handler 返回数据。
	c, w := ctxWith(t, http.MethodPost, "/x", `{"name":"bob","n":2}`, nil)
	var got mergeReq
	HandleJSONRequest(c, func(_ context.Context, r *mergeReq) (respT, error) {
		got = *r
		return respT{Msg: "hi"}, nil
	})
	if w.Code != http.StatusOK || got.Name != "bob" || got.N != 2 {
		t.Errorf("JSON 成功: code=%d req=%+v", w.Code, got)
	}

	// 坏 body → ShouldBindJSON 失败 → 400。
	c, w = ctxWith(t, http.MethodPost, "/x", `{`, nil)
	HandleJSONRequest(c, okResp(nil))
	if code, _ := decodeError(t, w); code != http.StatusBadRequest {
		t.Errorf("坏 JSON 应 400, got %d", code)
	}

	// 校验器：nil 被跳过 + 通过者放行。
	c, w = ctxWith(t, http.MethodPost, "/x", `{"name":"bob"}`, nil)
	pass := func(context.Context, *mergeReq) error { return nil }
	HandleJSONRequest(c, okResp(nil), nil, pass)
	if w.Code != http.StatusOK {
		t.Errorf("nil+通过校验器应 200, got %d", w.Code)
	}

	// 校验器报错 → runValidators 透传 → 400。
	c, w = ctxWith(t, http.MethodPost, "/x", `{"name":"bob"}`, nil)
	reject := func(context.Context, *mergeReq) error { return berrors.BadRequest("nope") }
	HandleJSONRequest(c, okResp(nil), reject)
	if code, _ := decodeError(t, w); code != http.StatusBadRequest {
		t.Errorf("校验器报错应 400, got %d", code)
	}

	// handler 返回 error → WriteResponse 错误分支。
	c, w = ctxWith(t, http.MethodPost, "/x", `{"name":"bob"}`, nil)
	HandleJSONRequest(c, func(context.Context, *mergeReq) (respT, error) {
		return respT{}, berrors.Internal("boom")
	})
	if w.Code == http.StatusOK {
		t.Errorf("handler 报错不应 200")
	}
}

func TestHandleQueryRequestBranches(t *testing.T) {
	c, w := ctxWith(t, http.MethodGet, "/x?name=bob&n=3", "", nil)
	var got mergeReq
	HandleQueryRequest(c, func(_ context.Context, r *mergeReq) (respT, error) {
		got = *r
		return respT{Msg: "q"}, nil
	})
	if w.Code != http.StatusOK || got.Name != "bob" || got.N != 3 {
		t.Errorf("Query 成功: code=%d req=%+v", w.Code, got)
	}

	// n 非数字 → ShouldBindQuery 失败 → 400。
	c, w = ctxWith(t, http.MethodGet, "/x?n=abc", "", nil)
	HandleQueryRequest(c, okResp(nil))
	if code, _ := decodeError(t, w); code != http.StatusBadRequest {
		t.Errorf("坏 Query 应 400, got %d", code)
	}

	// 校验器报错 → runValidators 透传 → 400。
	c, w = ctxWith(t, http.MethodGet, "/x?name=bob", "", nil)
	reject := func(context.Context, *mergeReq) error { return berrors.BadRequest("nope") }
	HandleQueryRequest(c, okResp(nil), reject)
	if code, _ := decodeError(t, w); code != http.StatusBadRequest {
		t.Errorf("Query 校验器报错应 400, got %d", code)
	}
}

func TestHandleUriRequestBranches(t *testing.T) {
	c, w := ctxWith(t, http.MethodGet, "/x/bob", "", gin.Params{{Key: "name", Value: "bob"}})
	var got mergeReq
	HandleUriRequest(c, func(_ context.Context, r *mergeReq) (respT, error) {
		got = *r
		return respT{Msg: "u"}, nil
	})
	if w.Code != http.StatusOK || got.Name != "bob" {
		t.Errorf("URI 成功: code=%d req=%+v", w.Code, got)
	}

	// n 非数字 → ShouldBindUri 失败 → 400。
	c, w = ctxWith(t, http.MethodGet, "/x", "", gin.Params{{Key: "n", Value: "xyz"}})
	HandleUriRequest(c, okResp(nil))
	if code, _ := decodeError(t, w); code != http.StatusBadRequest {
		t.Errorf("坏 URI 应 400, got %d", code)
	}

	// 校验器报错 → runValidators 透传 → 400。
	c, w = ctxWith(t, http.MethodGet, "/x/bob", "", gin.Params{{Key: "name", Value: "bob"}})
	reject := func(context.Context, *mergeReq) error { return berrors.BadRequest("nope") }
	HandleUriRequest(c, okResp(nil), reject)
	if code, _ := decodeError(t, w); code != http.StatusBadRequest {
		t.Errorf("URI 校验器报错应 400, got %d", code)
	}
}

func TestHandleAllRequestAndBinder(t *testing.T) {
	// 多源合并：uri 最低、query、json 最高（json 覆盖 name）。
	c, w := ctxWith(t, http.MethodPost, "/x?n=3", `{"name":"fromJSON"}`, gin.Params{{Key: "name", Value: "fromURI"}})
	var got mergeReq
	HandleAllRequest(c, func(_ context.Context, r *mergeReq) (respT, error) {
		got = *r
		return respT{Msg: "all"}, nil
	})
	if w.Code != http.StatusOK || got.Name != "fromJSON" || got.N != 3 {
		t.Errorf("多源合并应 JSON 覆盖 uri: code=%d req=%+v", w.Code, got)
	}

	// 无 body：hasBody=false 跳过 JSON，仅 uri+query。
	c, w = ctxWith(t, http.MethodGet, "/x?n=7", "", gin.Params{{Key: "name", Value: "bob"}})
	got = mergeReq{}
	HandleAllRequest(c, func(_ context.Context, r *mergeReq) (respT, error) {
		got = *r
		return respT{Msg: "all"}, nil
	})
	if got.Name != "bob" || got.N != 7 {
		t.Errorf("无 body 应绑 uri+query: req=%+v", got)
	}

	// uri 绑定失败 → ShouldBindAll 透传 → HandleAllRequest 报 400。
	c, w = ctxWith(t, http.MethodPost, "/x", `{"n":1}`, gin.Params{{Key: "n", Value: "xyz"}})
	HandleAllRequest(c, okResp(nil))
	if code, _ := decodeError(t, w); code != http.StatusBadRequest {
		t.Errorf("ShouldBindAll uri 错应 400, got %d", code)
	}

	// 绑定全通过、校验器报错 → runValidators 透传 → 400。
	c, w = ctxWith(t, http.MethodPost, "/x?n=3", `{"name":"bob"}`, nil)
	reject := func(context.Context, *mergeReq) error { return berrors.BadRequest("nope") }
	HandleAllRequest(c, okResp(nil), reject)
	if code, _ := decodeError(t, w); code != http.StatusBadRequest {
		t.Errorf("All 校验器报错应 400, got %d", code)
	}
}

// TestShouldBindAllErrorPaths 直接覆盖 ShouldBindAll 的 query 与 json 失败返回。
func TestShouldBindAllErrorPaths(t *testing.T) {
	// uri 通过、query 里 n 非数字 → ShouldBindQuery 失败。
	c, _ := ctxWith(t, http.MethodGet, "/x?n=abc", "", nil)
	var q mergeReq
	if err := ShouldBindAll(c, &q); err == nil {
		t.Error("query 非法应报错")
	}

	// uri/query 通过、body 坏 JSON → ShouldBindJSON 失败。
	c, _ = ctxWith(t, http.MethodPost, "/x", `{bad`, nil)
	var j mergeReq
	if err := ShouldBindAll(c, &j); err == nil {
		t.Error("坏 JSON body 应报错")
	}
}

type defReq struct {
	Name string `json:"name" form:"name"`
	Fill string `json:"-" form:"-"`
}

func (d *defReq) Default() {
	if d.Fill == "" {
		d.Fill = "defaulted"
	}
}

func TestShouldBindAllDefaulter(t *testing.T) {
	c, _ := ctxWith(t, http.MethodGet, "/x?name=zoe", "", nil)
	var obj defReq
	if err := ShouldBindAll(c, &obj); err != nil {
		t.Fatalf("ShouldBindAll 失败: %v", err)
	}
	if obj.Name != "zoe" || obj.Fill != "defaulted" {
		t.Errorf("Defaulter 未生效: %+v", obj)
	}
}

func TestHasBody(t *testing.T) {
	c, _ := ctxWith(t, http.MethodPost, "/x", `{"a":1}`, nil)
	if !hasBody(c) {
		t.Error("非空 body 应 hasBody=true")
	}
	c, _ = ctxWith(t, http.MethodGet, "/x", "", nil)
	if hasBody(c) {
		t.Error("无 body 应 hasBody=false")
	}
}

func TestWriteResponseEmptyAndData(t *testing.T) {
	// 空数据（nil 指针）→ 仅置 200，无 body。
	c, w := ctxWith(t, http.MethodGet, "/x", "", nil)
	WriteResponse(c, (*respT)(nil), nil)
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("空响应应 200 无 body: code=%d len=%d", w.Code, w.Body.Len())
	}

	// 非空数据 → c.JSON。
	c, w = ctxWith(t, http.MethodGet, "/x", "", nil)
	WriteResponse(c, respT{Msg: "y"}, nil)
	var out respT
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Msg != "y" {
		t.Errorf("非空响应 JSON 解码失败: body=%q err=%v", w.Body.String(), err)
	}
}

func TestIsEmptyAndStatusOf(t *testing.T) {
	if !isEmpty((*respT)(nil)) {
		t.Error("nil 指针应 isEmpty")
	}
	if isEmpty(respT{Msg: "x"}) {
		t.Error("结构体值不应 isEmpty")
	}
	if !isEmpty[map[string]int](nil) {
		t.Error("nil map 应 isEmpty")
	}
	body := StatusOf(berrors.NotFound("gone"))
	if len(body.Details) == 0 || body.Details[0].Reason != "gone" {
		t.Errorf("StatusOf 应产出 details[0].reason=gone: %+v", body)
	}
}
