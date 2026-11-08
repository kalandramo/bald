// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package semconv

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/codes"

	"github.com/kalandramo/bald/berrors"
)

// find 在属性集里取键值，找不到 ok=false。
func find(attrs []attribute.KeyValue, key attribute.Key) (attribute.Value, bool) {
	for _, kv := range attrs {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// wantAttr 断言属性集含 key 且值匹配（字符串化比较，兼容 int/string 属性）。
func wantAttr(t *testing.T, attrs []attribute.KeyValue, key attribute.Key, want string) {
	t.Helper()
	v, ok := find(attrs, key)
	if !ok || v.String() != want {
		t.Errorf("attr %s: want %q, got %q (found=%v)", key, want, v.String(), ok)
	}
}

// wantNoAttr 断言属性集不含 key。
func wantNoAttr(t *testing.T, attrs []attribute.KeyValue, key attribute.Key) {
	t.Helper()
	if _, ok := find(attrs, key); ok {
		t.Errorf("attr %s: must be absent", key)
	}
}

// TestHTTPSpanAttrs 钉住 span 全集：method 有界、route/path/protocol 缺省省略、
// 失败才挂 error.type、平台 reason 紧邻。
func TestHTTPSpanAttrs(t *testing.T) {
	attrs := HTTPSpanAttrs("GET", "/v1/users/:userID", "/v1/users/usr-123", "1.1", 200, nil)
	wantAttr(t, attrs, "http.request.method", "GET")
	wantAttr(t, attrs, "http.route", "/v1/users/:userID")
	wantAttr(t, attrs, "url.path", "/v1/users/usr-123")
	wantAttr(t, attrs, "network.protocol.version", "1.1")
	wantAttr(t, attrs, "http.response.status_code", "200")
	wantNoAttr(t, attrs, "error.type")
	wantNoAttr(t, attrs, ErrorReasonKey)

	// 失败：error.type = 状态码字符串；berrors 错误带出平台 reason。
	attrs = HTTPSpanAttrs("POST", "/v1/orders", "/v1/orders", "2.0", 404,
		berrors.NotFound("ORDER_NOT_FOUND"))
	wantAttr(t, attrs, "error.type", "404")
	wantAttr(t, attrs, ErrorReasonKey, "ORDER_NOT_FOUND")

	// 非 berrors 错误：无 reason 属性（不能编造）。
	attrs = HTTPSpanAttrs("POST", "/v1/orders", "/v1/orders", "2.0", 500, errPlain)
	wantAttr(t, attrs, "error.type", "500")
	wantNoAttr(t, attrs, ErrorReasonKey)

	// 空量省略；未知动词有界化；status<=0（未写出）省略状态。
	attrs = HTTPSpanAttrs("GARBAGE", "", "", "", 0, nil)
	wantAttr(t, attrs, "http.request.method", "_OTHER")
	wantNoAttr(t, attrs, "http.route")
	wantNoAttr(t, attrs, "url.path")
	wantNoAttr(t, attrs, "network.protocol.version")
	wantNoAttr(t, attrs, "http.response.status_code")
}

// TestHTTPMetricAttrs 钉住指标面：无 url.path（基数纪律）、method/scheme 手动
// 携带（裸 instrument 无自动注入）。
func TestHTTPMetricAttrs(t *testing.T) {
	attrs := HTTPMetricAttrs("GET", "http", "/v1/secret/:id", "", 200, nil)
	wantAttr(t, attrs, "http.request.method", "GET")
	wantAttr(t, attrs, "url.scheme", "http")
	wantAttr(t, attrs, "http.route", "/v1/secret/:id")
	wantAttr(t, attrs, "http.response.status_code", "200")
	wantNoAttr(t, attrs, "url.path")
	wantNoAttr(t, attrs, "error.type")

	attrs = HTTPMetricAttrs("GET", "http", "", "", 429, nil)
	wantAttr(t, attrs, "error.type", "429")
	wantNoAttr(t, attrs, "http.route")
}

// TestActiveRequestAttrs 钉住在途数属性：scheme 恒带，addr/port 缺省省略。
func TestActiveRequestAttrs(t *testing.T) {
	attrs := ActiveRequestAttrs("http", "api.example.com", 8080)
	wantAttr(t, attrs, "url.scheme", "http")
	wantAttr(t, attrs, "server.address", "api.example.com")
	wantAttr(t, attrs, "server.port", "8080")

	attrs = ActiveRequestAttrs("https", "", 0)
	wantAttr(t, attrs, "url.scheme", "https")
	wantNoAttr(t, attrs, "server.address")
	wantNoAttr(t, attrs, "server.port")
}

// TestRPCAttrs 钉住 gRPC 两面一份：system.name 恒带、method 空省略、状态 wire
// 形态、失败才挂 error.type、reason 经 berrors 带出。
func TestRPCAttrs(t *testing.T) {
	attrs := RPCAttrs("/pkg.v1.SecretService/GetSecret", codes.OK, nil)
	wantAttr(t, attrs, "rpc.system.name", "grpc")
	wantAttr(t, attrs, "rpc.method", "/pkg.v1.SecretService/GetSecret")
	wantAttr(t, attrs, "rpc.response.status_code", "OK")
	wantNoAttr(t, attrs, "error.type")

	attrs = RPCAttrs("/pkg.v1.SecretService/GetSecret", codes.PermissionDenied,
		berrors.PermissionDenied("TENANT_SUSPENDED"))
	wantAttr(t, attrs, "rpc.response.status_code", "PERMISSION_DENIED")
	wantAttr(t, attrs, "error.type", "PERMISSION_DENIED")
	wantAttr(t, attrs, ErrorReasonKey, "TENANT_SUSPENDED")

	// 旁路链路短路（handler 未跑）时 err 兜底供码：transport 零值 OK、err 非 nil。
	attrs = RPCAttrs("", codes.Unavailable, nil)
	wantNoAttr(t, attrs, "rpc.method")
	wantAttr(t, attrs, "rpc.response.status_code", "UNAVAILABLE")
}

// TestGRPCStatusCodeName 钉住 wire 拼写与词边界规则。
func TestGRPCStatusCodeName(t *testing.T) {
	for code, want := range map[codes.Code]string{
		codes.OK:                "OK",
		codes.NotFound:          "NOT_FOUND",
		codes.PermissionDenied:  "PERMISSION_DENIED",
		codes.ResourceExhausted: "RESOURCE_EXHAUSTED",
		codes.OutOfRange:        "OUT_OF_RANGE",
		codes.DataLoss:          "DATA_LOSS",
		codes.Unauthenticated:   "UNAUTHENTICATED",
		codes.Code(99):          "CODE(99)",
	} {
		if got := GRPCStatusCodeName(code); got != want {
			t.Errorf("code %d: want %q, got %q", code, want, got)
		}
	}
}

type plainErr struct{}

func (plainErr) Error() string { return "boom" }

var errPlain error = plainErr{}
