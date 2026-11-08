// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package middleware

import (
	"context"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

// TestTransporterRoundTrip 钉住契约面：构造→读回→ctx 注入取出。
func TestTransporterRoundTrip(t *testing.T) {
	tr := NewTransporter(KindHTTP, "GET /v1/users/:userID", "127.0.0.1:8080",
		NewHTTPHeader(http.Header{"X-Req": {"v1"}}),
		NewHTTPHeader(http.Header{}),
		WithHTTPRequest("GET", "/v1/users/:userID", "/v1/users/usr-123", "HTTP/1.1"),
	)
	defer ReleaseTransporter(tr)

	if tr.Operation() != "GET /v1/users/:userID" {
		t.Errorf("Operation = %q", tr.Operation())
	}
	// Operation 是路由模板不是 path——基数纪律（低基数进指标，raw path 只进 span）。
	if tr.Path() != "/v1/users/usr-123" {
		t.Errorf("Path = %q", tr.Path())
	}
	if tr.Protocol() != "HTTP/1.1" {
		// WithHTTPRequest 原样承载协议串；剥前缀是 GinHandler 桥（protocolVersion）的职责。
		t.Errorf("Protocol = %q", tr.Protocol())
	}
	if protocolVersion("HTTP/1.1") != "1.1" || protocolVersion("h2") != "h2" {
		t.Error("protocolVersion broken")
	}
	if tr.GRPCCode() != codes.OK {
		t.Errorf("GRPCCode zero value = %v, want OK", tr.GRPCCode())
	}

	ctx := NewServerContext(context.Background(), tr)
	got, ok := FromServerContext(ctx)
	if !ok || got != tr {
		t.Fatal("FromServerContext did not return the injected transporter")
	}
	if _, ok := FromServerContext(context.Background()); ok {
		t.Error("empty context should not carry a transporter")
	}
}

// TestHeaderAdapters 钉住 Header 的双协议适配：Get 缺失返回 "" 不 panic
// （metadata.MD.Get 的坑），Set/Values/Keys 走通。
func TestHeaderAdapters(t *testing.T) {
	mh := NewMetadataHeader(metadata.MD{"x-a": {"1", "2"}})
	if mh.Get("x-a") != "1" || mh.Get("x-missing") != "" {
		t.Errorf("metadata Get = %q / %q", mh.Get("x-a"), mh.Get("x-missing"))
	}
	mh.Set("x-a", "9")
	if got := mh.Values("x-a"); len(got) != 1 || got[0] != "9" {
		t.Errorf("after Set: %v", got)
	}
	mh.Add("x-a", "3")
	if len(mh.Values("x-a")) != 2 {
		t.Errorf("after Add: %v", mh.Values("x-a"))
	}

	hh := NewHTTPHeader(http.Header{})
	hh.Add("X-B", "v")
	if hh.Get("x-b") != "v" || len(hh.Values("x-b")) != 1 || len(hh.Keys()) != 1 {
		t.Errorf("httpHeader adapter broken: get=%q values=%v keys=%v", hh.Get("x-b"), hh.Values("x-b"), hh.Keys())
	}
}

// TestTransporterPoolReset 经 Release 后复用的实例必须零值起步（池化不泄漏
// 上一请求的状态）。
func TestTransporterPoolReset(t *testing.T) {
	tr := NewTransporter(KindHTTP, "GET /", "", NewHTTPHeader(http.Header{}), NewHTTPHeader(http.Header{}),
		WithHTTPRequest("GET", "/", "/x", "HTTP/1.1"))
	tr.SetStatusCode(500)
	tr.SetGRPCCode(codes.Internal)
	ReleaseTransporter(tr)

	tr2 := NewTransporter(KindGRPC, "/pkg.Svc/M", "", NewMetadataHeader(metadata.MD{}), NewMetadataHeader(metadata.MD{}))
	defer ReleaseTransporter(tr2)
	if tr2.StatusCode() != 0 || tr2.GRPCCode() != codes.OK || tr2.Route() != "" {
		t.Errorf("pooled instance not reset: status=%d grpc=%v route=%q", tr2.StatusCode(), tr2.GRPCCode(), tr2.Route())
	}
}

// TestChainCompositionOrder 钉住组合序：Chain(m1, m2, m3)(h) == m1(m2(m3(h)))
// ——最外层最先执行。
func TestChainCompositionOrder(t *testing.T) {
	var order []string
	mk := func(name string) Middleware {
		return func(next Handler) Handler {
			return func(ctx context.Context, _ interface{}) (interface{}, error) {
				order = append(order, name+"→")
				resp, err := next(ctx, nil)
				order = append(order, "←"+name)
				return resp, err
			}
		}
	}
	h := Chain(mk("m1"), mk("m2"), mk("m3"))(func(_ context.Context, _ interface{}) (interface{}, error) {
		order = append(order, "h")
		return nil, nil
	})
	if _, err := h(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"m1→", "m2→", "m3→", "h", "←m3", "←m2", "←m1"}
	for i, s := range want {
		if order[i] != s {
			t.Fatalf("composition order broken at %d: got %v", i, order)
		}
	}
}
