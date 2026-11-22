package web_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/transport/web"
)

// newSnakeFieldMessage 造一个含下划线字段名（proto 名 type_name、JSON 名
// typeName）的消息，用来观察 UseProtoNames / camelCase 的输出差异。
func newSnakeFieldMessage(t *testing.T) *descriptorpb.FieldDescriptorProto {
	t.Helper()
	msg := &descriptorpb.FieldDescriptorProto{}
	fd := msg.ProtoReflect().Descriptor().Fields().ByName("type_name")
	msg.ProtoReflect().Set(fd, protoreflect.ValueOfString(".test.v1.Foo"))
	return msg
}

// 零 Option 必须与改动前完全一致：未知字段仍报错，响应仍是默认状态码、
// 默认 Content-Type 与 camelCase 字段名。
func TestProtoJSONOption_DefaultsMatchLegacy(t *testing.T) {
	c, _ := newGinContext(t, http.MethodPost, "/v1/req", `{"name":"bob","unknown_field":1}`, "application/json")
	err := web.BindProtoRequest(c, newTestRequest(t), "*", nil)
	if err == nil {
		t.Fatal("want error for unknown field when DiscardUnknown is off by default")
	}
	werr, ok := berrors.FromError(err)
	if !ok || werr.Code != berrors.CodeInvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}

	c2, w2 := newGinContext(t, http.MethodGet, "/v1/req", "", "")
	web.WriteProtoResponse(c2, newSnakeFieldMessage(t), nil)
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w2.Code)
	}
	if ct := w2.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want default", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%q)", err, w2.Body.String())
	}
	if got, ok := body["typeName"]; !ok || got != ".test.v1.Foo" {
		t.Fatalf("body = %v, want camelCase typeName", body)
	}
	if _, ok := body["type_name"]; ok {
		t.Fatalf("body = %v, should not contain snake_case type_name", body)
	}
}

// WithDiscardUnknown(true) 放行未知字段。
func TestProtoJSONOption_WithDiscardUnknown(t *testing.T) {
	c, _ := newGinContext(t, http.MethodPost, "/v1/req", `{"name":"bob","unknown_field":1}`, "application/json")
	if err := web.BindProtoRequest(c, newTestRequest(t), "*", nil, web.WithDiscardUnknown(true)); err != nil {
		t.Fatalf("unknown field should be discarded: %v", err)
	}
}

// WithUseProtoNames(true) 输出 proto 下划线字段名。
func TestProtoJSONOption_WithUseProtoNames(t *testing.T) {
	c, w := newGinContext(t, http.MethodGet, "/v1/req", "", "")
	web.WriteProtoResponse(c, newSnakeFieldMessage(t), nil, web.WithUseProtoNames(true))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%q)", err, w.Body.String())
	}
	if got, ok := body["type_name"]; !ok || got != ".test.v1.Foo" {
		t.Fatalf("body = %v, want snake_case type_name", body)
	}
	if _, ok := body["typeName"]; ok {
		t.Fatalf("body = %v, should not contain camelCase typeName", body)
	}
}

// WithStatusCode / WithContentType 覆盖成功响应的状态码与 Content-Type。
func TestProtoJSONOption_WithStatusCodeAndContentType(t *testing.T) {
	cases := []struct {
		name     string
		opts     []web.ProtoJSONOption
		wantCode int
		wantCT   string
	}{
		{
			name:     "override",
			opts:     []web.ProtoJSONOption{web.WithStatusCode(http.StatusCreated), web.WithContentType("application/json")},
			wantCode: http.StatusCreated,
			wantCT:   "application/json",
		},
		{
			name:     "invalid values ignored",
			opts:     []web.ProtoJSONOption{web.WithStatusCode(0), web.WithContentType("")},
			wantCode: http.StatusOK,
			wantCT:   "application/json; charset=utf-8",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := newGinContext(t, http.MethodGet, "/v1/req", "", "")
			web.WriteProtoResponse(c, newTestRequest(t), nil, tc.opts...)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantCode)
			}
			if ct := w.Header().Get("Content-Type"); ct != tc.wantCT {
				t.Fatalf("Content-Type = %q, want %q", ct, tc.wantCT)
			}
		})
	}
}

// WithProtoJSONOptions 直接替换两套 protojson 选项同样生效。
func TestProtoJSONOption_WithProtoJSONOptions(t *testing.T) {
	c, _ := newGinContext(t, http.MethodPost, "/v1/req", `{"unknown_field":1}`, "application/json")
	if err := web.BindProtoRequest(c, newTestRequest(t), "*", nil,
		web.WithProtoJSONOptions(protojson.UnmarshalOptions{DiscardUnknown: true}, protojson.MarshalOptions{})); err != nil {
		t.Fatalf("unknown field should be discarded: %v", err)
	}
}
