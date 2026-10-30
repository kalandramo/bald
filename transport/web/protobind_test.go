package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	_ "google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/transport/web"
)

// testRequestDescriptor 用描述符构造器造一个带各类字段的请求消息（不依赖
// protoc 产物）：验证绑定逻辑只需 protoreflect 视图。
func testRequestDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	fd := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("test/v1/req.proto"),
		Package:    proto.String("test.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/timestamp.proto"},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("Kind"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("KIND_UNSPECIFIED"), Number: proto.Int32(0)},
				{Name: proto.String("KIND_ADMIN"), Number: proto.Int32(1)},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Filter"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name: proto.String("keyword"), Number: proto.Int32(1),
						Type:  descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					},
				},
			},
			{
				Name: proto.String("Req"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name: proto.String("name"), Number: proto.Int32(1),
						Type:  descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					},
					{
						Name: proto.String("limit"), Number: proto.Int32(2),
						Type:  descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
						Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					},
					{
						Name: proto.String("ratio"), Number: proto.Int32(3),
						Type:  descriptorpb.FieldDescriptorProto_TYPE_DOUBLE.Enum(),
						Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					},
					{
						Name: proto.String("active"), Number: proto.Int32(4),
						Type:  descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(),
						Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					},
					{
						Name: proto.String("tags"), Number: proto.Int32(5),
						Type:  descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
					},
					{
						Name: proto.String("filter"), Number: proto.Int32(6),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						TypeName: proto.String(".test.v1.Filter"),
					},
					{
						Name: proto.String("kind"), Number: proto.Int32(7),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						TypeName: proto.String(".test.v1.Kind"),
					},
					{
						Name: proto.String("at"), Number: proto.Int32(8),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						TypeName: proto.String(".google.protobuf.Timestamp"),
					},
				},
			},
		},
	}
	file, err := protodesc.NewFile(fd, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("build test descriptor: %v", err)
	}
	return file.Messages().ByName("Req")
}

func newTestRequest(t *testing.T) proto.Message {
	t.Helper()
	return dynamicpb.NewMessage(testRequestDescriptor(t))
}

// newGinContext 构造一个带指定 method/url/body 的 gin 上下文。
func newGinContext(t *testing.T, method, target, body string, contentType string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c.Request = req
	return c, w
}

func TestBindProtoRequest_JSONBody(t *testing.T) {
	c, _ := newGinContext(t, http.MethodPost, "/v1/req", `{"name":"bob","limit":5,"kind":"KIND_ADMIN"}`, "application/json")
	msg := newTestRequest(t)
	if err := web.BindProtoRequest(c, msg, "*", nil); err != nil {
		t.Fatalf("BindProtoRequest: %v", err)
	}
	m := msg.ProtoReflect()
	if got := m.Get(m.Descriptor().Fields().ByName("name")).String(); got != "bob" {
		t.Fatalf("name = %q, want bob", got)
	}
	if got := m.Get(m.Descriptor().Fields().ByName("limit")).Int(); got != 5 {
		t.Fatalf("limit = %d, want 5", got)
	}
	if got := m.Get(m.Descriptor().Fields().ByName("kind")).Enum(); got != 1 {
		t.Fatalf("kind = %d, want 1 (KIND_ADMIN)", got)
	}
}

func TestBindProtoRequest_EmptyBodyAllowed(t *testing.T) {
	c, _ := newGinContext(t, http.MethodPost, "/v1/req", "", "application/json")
	msg := newTestRequest(t)
	if err := web.BindProtoRequest(c, msg, "*", nil); err != nil {
		t.Fatalf("empty body must not fail: %v", err)
	}
}

func TestBindProtoRequest_BodyFieldPath(t *testing.T) {
	c, _ := newGinContext(t, http.MethodPost, "/v1/req", `{"keyword":"kw"}`, "application/json")
	msg := newTestRequest(t)
	if err := web.BindProtoRequest(c, msg, "filter", nil); err != nil {
		t.Fatalf("BindProtoRequest: %v", err)
	}
	filter := msg.ProtoReflect().Get(msg.ProtoReflect().Descriptor().Fields().ByName("filter")).Message()
	if got := filter.Get(filter.Descriptor().Fields().ByName("keyword")).String(); got != "kw" {
		t.Fatalf("filter.keyword = %q, want kw", got)
	}
}

func TestBindProtoRequest_PathAndQuery(t *testing.T) {
	c, _ := newGinContext(t, http.MethodGet,
		"/v1/req/bob?limit=7&ratio=0.5&active=true&tags=a&tags=b&kind=KIND_ADMIN&filter.keyword=kw&at=2024-01-02T03:04:05Z&cachebust=1",
		"", "")
	msg := newTestRequest(t)
	if err := web.BindProtoRequest(c, msg, "", map[string]string{"name": "bob"}); err != nil {
		t.Fatalf("BindProtoRequest: %v", err)
	}
	m := msg.ProtoReflect()
	fields := m.Descriptor().Fields()
	if got := m.Get(fields.ByName("name")).String(); got != "bob" {
		t.Fatalf("name = %q, want bob", got)
	}
	if got := m.Get(fields.ByName("limit")).Int(); got != 7 {
		t.Fatalf("limit = %d, want 7", got)
	}
	if got := m.Get(fields.ByName("ratio")).Float(); got != 0.5 {
		t.Fatalf("ratio = %v, want 0.5", got)
	}
	if got := m.Get(fields.ByName("active")).Bool(); !got {
		t.Fatal("active = false, want true")
	}
	tags := m.Get(fields.ByName("tags")).List()
	if tags.Len() != 2 || tags.Get(0).String() != "a" || tags.Get(1).String() != "b" {
		t.Fatalf("tags = %v, want [a b]", tags)
	}
	if got := m.Get(fields.ByName("kind")).Enum(); got != 1 {
		t.Fatalf("kind = %d, want 1", got)
	}
	filter := m.Get(fields.ByName("filter")).Message()
	if got := filter.Get(filter.Descriptor().Fields().ByName("keyword")).String(); got != "kw" {
		t.Fatalf("filter.keyword = %q, want kw", got)
	}
	// Timestamp 走 proto JSON 字符串表示（与 grpc-gateway 一致的 WKT 形态）。
	at := m.Get(fields.ByName("at")).Message()
	if got := at.Get(at.Descriptor().Fields().ByName("seconds")).Int(); got == 0 {
		t.Fatalf("timestamp seconds = 0, want non-zero for 2024-01-02T03:04:05Z")
	}
}

func TestBindProtoRequest_QueryIgnoredWhenBodyCovered(t *testing.T) {
	c, _ := newGinContext(t, http.MethodPost, "/v1/req?name=query", `{"name":"body"}`, "application/json")
	msg := newTestRequest(t)
	if err := web.BindProtoRequest(c, msg, "*", nil); err != nil {
		t.Fatalf("BindProtoRequest: %v", err)
	}
	fd := msg.ProtoReflect().Descriptor().Fields().ByName("name")
	if got := msg.ProtoReflect().Get(fd).String(); got != "body" {
		t.Fatalf("name = %q, want body (body wins over query)", got)
	}
}

func TestBindProtoRequest_PathBeatsQuery(t *testing.T) {
	c, _ := newGinContext(t, http.MethodGet, "/v1/req/bob?name=query", "", "")
	msg := newTestRequest(t)
	if err := web.BindProtoRequest(c, msg, "", map[string]string{"name": "path"}); err != nil {
		t.Fatalf("BindProtoRequest: %v", err)
	}
	fd := msg.ProtoReflect().Descriptor().Fields().ByName("name")
	if got := msg.ProtoReflect().Get(fd).String(); got != "path" {
		t.Fatalf("name = %q, want path (path wins over query)", got)
	}
}

func TestBindProtoRequest_BadValueIsBadRequest(t *testing.T) {
	c, _ := newGinContext(t, http.MethodGet, "/v1/req?limit=abc", "", "")
	err := web.BindProtoRequest(c, newTestRequest(t), "", nil)
	if err == nil {
		t.Fatal("want error for limit=abc")
	}
	werr, ok := berrors.FromError(err)
	if !ok || werr.Code != berrors.CodeInvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

func TestBindProtoRequest_UnknownPathVarFailsLoud(t *testing.T) {
	c, _ := newGinContext(t, http.MethodGet, "/v1/req/bob", "", "")
	err := web.BindProtoRequest(c, newTestRequest(t), "", map[string]string{"nmae": "bob"})
	werr, ok := berrors.FromError(err)
	if !ok || !strings.Contains(werr.Message, "no such field") {
		t.Fatalf("err = %v, want unknown-field message (annotation/proto drift must not be silent)", err)
	}
}

func TestWriteProtoResponse(t *testing.T) {
	msg := newTestRequest(t)
	msg.ProtoReflect().Set(msg.ProtoReflect().Descriptor().Fields().ByName("name"), protoreflect.ValueOfString("bob"))

	c, w := newGinContext(t, http.MethodGet, "/v1/req", "", "")
	web.WriteProtoResponse(c, msg, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%q)", err, w.Body.String())
	}
	if body["name"] != "bob" {
		t.Fatalf("body = %v, want name=bob", body)
	}

	c2, w2 := newGinContext(t, http.MethodGet, "/v1/req", "", "")
	web.WriteProtoResponse(c2, nil, berrors.BadRequest("bad-name").WithMessage("bad name"))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w2.Code)
	}
}

func TestWriteProtoResponse_NilMessage(t *testing.T) {
	c, w := newGinContext(t, http.MethodGet, "/v1/req", "", "")
	web.WriteProtoResponse(c, (*wrapperspb.StringValue)(nil), nil)
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("status = %d body = %q, want empty 200", w.Code, w.Body.String())
	}
}
