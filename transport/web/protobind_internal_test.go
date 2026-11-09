// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// protobind_internal_test.go 用内部包（package web）直接驱动 proto 绑定的不可导出
// 助手，逐分支覆盖：scalarValue 对全部 18 种 Kind 的成功/失败取值，setProtoField
// 的 map/repeated-message/空值/标量/列表分支，setProtoPath 的 strict/unknown/
// 非消息/repeated 穿越与嵌套成功，coveredByBody/coveredByPathVar 的三段判定，
// wrapBodyField 的多段路径，以及 BindProtoRequest 的读体失败/坏 JSON、
// WriteProtoResponse 的 protojson 编码失败（NaN）。全部以描述符构造器造的动态消息
// 驱动，无需 protoc 产物或真实监听端口。

package web

import (
	"encoding/base64"
	"errors"
	"io"
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
)

type fieldType = descriptorpb.FieldDescriptorProto_Type

const (
	tString = descriptorpb.FieldDescriptorProto_TYPE_STRING
	tBytes  = descriptorpb.FieldDescriptorProto_TYPE_BYTES
	tBool   = descriptorpb.FieldDescriptorProto_TYPE_BOOL
	tInt32  = descriptorpb.FieldDescriptorProto_TYPE_INT32
	tSint32 = descriptorpb.FieldDescriptorProto_TYPE_SINT32
	tSFix32 = descriptorpb.FieldDescriptorProto_TYPE_SFIXED32
	tInt64  = descriptorpb.FieldDescriptorProto_TYPE_INT64
	tSint64 = descriptorpb.FieldDescriptorProto_TYPE_SINT64
	tSFix64 = descriptorpb.FieldDescriptorProto_TYPE_SFIXED64
	tUint32 = descriptorpb.FieldDescriptorProto_TYPE_UINT32
	tFix32  = descriptorpb.FieldDescriptorProto_TYPE_FIXED32
	tUint64 = descriptorpb.FieldDescriptorProto_TYPE_UINT64
	tFix64  = descriptorpb.FieldDescriptorProto_TYPE_FIXED64
	tFloat  = descriptorpb.FieldDescriptorProto_TYPE_FLOAT
	tDouble = descriptorpb.FieldDescriptorProto_TYPE_DOUBLE
	tEnum   = descriptorpb.FieldDescriptorProto_TYPE_ENUM
	tMsg    = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
)

func optField(name string, num int32, typ fieldType) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:   proto.String(name),
		Number: proto.Int32(num),
		Type:   typ.Enum(),
		Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
	}
}

func msgField(name string, num int32, typeName string) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		Number:   proto.Int32(num),
		Type:     tMsg.Enum(),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		TypeName: proto.String(typeName),
	}
}

func repField(name string, num int32, typ fieldType, typeName string) *descriptorpb.FieldDescriptorProto {
	f := optField(name, num, typ)
	f.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	if typeName != "" {
		f.TypeName = proto.String(typeName)
	}
	return f
}

func mapField(name string, num int32, entryName string) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		Number:   proto.Int32(num),
		Type:     tMsg.Enum(),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
		TypeName: proto.String(entryName),
	}
}

// mapEntryMessage 造 proto map 的自动条目消息（options.map_entry=true）：key/value 两个字段。
func mapEntryMessage(name string) *descriptorpb.DescriptorProto {
	return &descriptorpb.DescriptorProto{
		Name: proto.String(name),
		Field: []*descriptorpb.FieldDescriptorProto{
			optField("key", 1, tString),
			optField("value", 2, tString),
		},
		Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
	}
}

// allKindsRequest 造一个覆盖全部 18 种 Kind + repeated(标量/消息) + map 的 Req 动态消息，
// 返回 protoreflect.Message；调用方用 fields.ByName 取字段描述符。
func allKindsRequest(t *testing.T) protoreflect.Message {
	t.Helper()
	fd := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("internal_allkinds.proto"),
		Package:    proto.String("internal.allkinds"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/timestamp.proto"},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("Status"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("STATUS_ZERO"), Number: proto.Int32(0)},
				{Name: proto.String("STATUS_ONE"), Number: proto.Int32(1)},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name:  proto.String("Filter"),
				Field: []*descriptorpb.FieldDescriptorProto{optField("keyword", 1, tString)},
			},
			{
				Name: proto.String("Req"),
				NestedType: []*descriptorpb.DescriptorProto{
					mapEntryMessage("AttrsEntry"),
				},
				Field: []*descriptorpb.FieldDescriptorProto{
					optField("s", 1, tString),
					optField("b", 2, tBytes),
					optField("bl", 3, tBool),
					optField("i32", 4, tInt32),
					optField("si32", 5, tSint32),
					optField("sf32", 6, tSFix32),
					optField("i64", 7, tInt64),
					optField("si64", 8, tSint64),
					optField("sf64", 9, tSFix64),
					optField("u32", 10, tUint32),
					optField("f32", 11, tFix32),
					optField("u64", 12, tUint64),
					optField("f64", 13, tFix64),
					optField("fl", 14, tFloat),
					optField("do", 15, tDouble),
					msgField("e", 16, ".internal.allkinds.Status"), // 占位（下面覆盖为 enum）
					msgField("msg", 17, ".internal.allkinds.Filter"),
					msgField("ts", 18, ".google.protobuf.Timestamp"),
					repField("rs", 19, tString, ""),
					repField("ri", 20, tInt32, ""),
					repField("rm", 21, tMsg, ".internal.allkinds.Filter"),
					mapField("attrs", 22, ".internal.allkinds.Req.AttrsEntry"),
				},
			},
		},
	}
	// e 应为 ENUM 类型：msgField 造的是 MESSAGE，这里显式改成 enum。
	eField := fd.MessageType[1].Field[15]
	eField.Type = tEnum.Enum()
	file, err := protodesc.NewFile(fd, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("build all-kinds descriptor: %v", err)
	}
	return dynamicpb.NewMessage(file.Messages().ByName("Req"))
}

func fieldOf(t *testing.T, m protoreflect.Message, name string) protoreflect.FieldDescriptor {
	t.Helper()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		t.Fatalf("no field %q", name)
	}
	return fd
}

// TestScalarValueAllKinds 逐个 Kind 覆盖成功取值；每个 Kind 的失败取值覆盖错误返回。
func TestScalarValueAllKinds(t *testing.T) {
	m := allKindsRequest(t)

	// 成功分支。
	okCases := []struct {
		field, value string
	}{
		{"s", "hello"},
		{"b", base64.StdEncoding.EncodeToString([]byte("hi"))},
		{"bl", "true"},
		{"i32", "-5"},
		{"si32", "5"},
		{"sf32", "5"},
		{"i64", "-9000000000"},
		{"si64", "9000000000"},
		{"sf64", "9000000000"},
		{"u32", "7"},
		{"f32", "7"},
		{"u64", "7"},
		{"f64", "7"},
		{"fl", "1.5"},
		{"do", "2.5"},
		{"e", "STATUS_ONE"},            // 枚举名命中
		{"e", "3"},                     // 枚举按数字解析
		{"ts", "2024-01-02T03:04:05Z"}, // 消息（WKT 字符串形态）
	}
	for _, tc := range okCases {
		v, err := scalarValue(m, fieldOf(t, m, tc.field), tc.value)
		if err != nil {
			t.Errorf("scalarValue(%s=%q): %v", tc.field, tc.value, err)
			continue
		}
		if !v.IsValid() {
			t.Errorf("scalarValue(%s=%q) invalid value", tc.field, tc.value)
		}
	}

	// 失败分支（每种 Kind 的非法取值）。
	badCases := []struct{ field, value string }{
		{"b", "!!!not base64!!!"},
		{"bl", "notabool"},
		{"i32", "abc"},
		{"i64", "abc"},
		{"u32", "-1"},
		{"u64", "abc"},
		{"fl", "abc"},
		{"do", "abc"},
		{"e", "bogus"},            // 枚举：非法名且非数字
		{"ts", "not-a-timestamp"}, // 消息：非法 WKT 字符串
		{"msg", "plain-string"},   // 消息：字符串无法解成对象
	}
	for _, tc := range badCases {
		if _, err := scalarValue(m, fieldOf(t, m, tc.field), tc.value); err == nil {
			t.Errorf("scalarValue(%s=%q) want error", tc.field, tc.value)
		}
	}
}

// TestSetProtoFieldBranches 覆盖 map / repeated-message / 列表成功与失败 / 空值 / 标量成功与失败。
func TestSetProtoFieldBranches(t *testing.T) {
	m := allKindsRequest(t)

	// map 字段拒绝绑定。
	if err := setProtoField(m, fieldOf(t, m, "attrs"), []string{"x"}); err == nil {
		t.Error("map field must error")
	}
	// repeated message 字段拒绝绑定。
	if err := setProtoField(m, fieldOf(t, m, "rm"), []string{"x"}); err == nil {
		t.Error("repeated message field must error")
	}
	// 列表：成功追加 + 元素取值失败。
	if err := setProtoField(m, fieldOf(t, m, "rs"), []string{"a", "b"}); err != nil {
		t.Errorf("repeated string append: %v", err)
	}
	if err := setProtoField(m, fieldOf(t, m, "ri"), []string{"1", "bad"}); err == nil {
		t.Error("repeated int32 bad element must error")
	}
	// 空 values 直接返回 nil。
	if err := setProtoField(m, fieldOf(t, m, "s"), nil); err != nil {
		t.Errorf("empty values want nil: %v", err)
	}
	// 标量成功 + 失败。
	if err := setProtoField(m, fieldOf(t, m, "s"), []string{"v"}); err != nil {
		t.Errorf("scalar set: %v", err)
	}
	if err := setProtoField(m, fieldOf(t, m, "i32"), []string{"abc"}); err == nil {
		t.Error("scalar bad must error")
	}
}

// TestSetProtoPathBranches 覆盖 strict/unknown、非消息穿越、repeated 穿越、单段/嵌套成功。
func TestSetProtoPathBranches(t *testing.T) {
	m := allKindsRequest(t)

	// strict 未知字段报错。
	if err := setProtoPath(m, "nope", []string{"x"}, true); err == nil {
		t.Error("strict unknown field want error")
	}
	// 非 strict 未知字段返回 errUnknownField。
	if err := setProtoPath(m, "nope", []string{"x"}, false); !errors.Is(err, errUnknownField) {
		t.Errorf("non-strict unknown want errUnknownField, got %v", err)
	}
	// 穿越非消息字段。
	if err := setProtoPath(m, "s.x", []string{"x"}, true); err == nil {
		t.Error("traverse non-message want error")
	}
	// 穿越 repeated message 字段（Kind 是 message 但 IsList）。
	if err := setProtoPath(m, "rm.x", []string{"x"}, true); err == nil {
		t.Error("traverse repeated message want error")
	}
	// 穿越 map 字段（Kind 是 message 但 IsMap）。
	if err := setProtoPath(m, "attrs.x", []string{"x"}, true); err == nil {
		t.Error("traverse map want error")
	}
	// 穿越 repeated 字段。
	if err := setProtoPath(m, "rs.x", []string{"x"}, true); err == nil {
		t.Error("traverse repeated want error")
	}
	// 单段成功。
	if err := setProtoPath(m, "s", []string{"ok"}, false); err != nil {
		t.Errorf("single-seg path: %v", err)
	}
	// 嵌套消息穿越成功（JSON 名与 proto 名都接受）。
	if err := setProtoPath(m, "msg.keyword", []string{"kw"}, false); err != nil {
		t.Errorf("nested path: %v", err)
	}
	got := m.Get(fieldOf(t, m, "msg")).Message()
	if got.Get(got.Descriptor().Fields().ByName("keyword")).String() != "kw" {
		t.Error("nested path did not set msg.keyword")
	}
}

func TestCoveredByBodyBranches(t *testing.T) {
	cases := []struct {
		body, key string
		want      bool
	}{
		{"*", "anything", true},
		{"", "anything", false},
		{"filter", "filter", true},
		{"filter", "filter.keyword", true},
		{"filter", "other", false},
	}
	for _, tc := range cases {
		if got := coveredByBody(tc.body, tc.key); got != tc.want {
			t.Errorf("coveredByBody(%q,%q)=%v want %v", tc.body, tc.key, got, tc.want)
		}
	}
}

func TestCoveredByPathVarBranches(t *testing.T) {
	// 精确命中。
	if !coveredByPathVar(map[string]string{"name": "x"}, "name") {
		t.Error("exact key must be covered")
	}
	// key 是 varName 的子路径。
	if !coveredByPathVar(map[string]string{"filter": "x"}, "filter.keyword") {
		t.Error("child-of-var must be covered")
	}
	// varName 是 key 的子路径。
	if !coveredByPathVar(map[string]string{"user.name": "x"}, "user") {
		t.Error("parent-of-var must be covered")
	}
	// 无匹配。
	if coveredByPathVar(map[string]string{"other": "x"}, "name") {
		t.Error("unrelated key must not be covered")
	}
}

func TestWrapBodyFieldMultiSegment(t *testing.T) {
	got := string(wrapBodyField("a.b", []byte(`{"c":1}`)))
	want := `{"a":{"b":{"c":1}}}`
	if got != want {
		t.Errorf("wrapBodyField(a.b) = %s, want %s", got, want)
	}
}

// errReader 是读取即失败的 body，用于覆盖 BindProtoRequest 的 io.ReadAll 错误分支。
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func protoCtx(t *testing.T, method, target string, body io.Reader) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(method, target, body)
	return c
}

func TestBindProtoRequest_NilMessage(t *testing.T) {
	c := protoCtx(t, http.MethodGet, "/v1/req", nil)
	if err := BindProtoRequest(c, nil, "*", nil); err == nil {
		t.Fatal("nil request message want BadRequest")
	}
}

func TestBindProtoRequest_ReadBodyError(t *testing.T) {
	c := protoCtx(t, http.MethodPost, "/v1/req", nil)
	c.Request.Body = io.NopCloser(errReader{})
	if err := BindProtoRequest(c, allKindsRequest(t).Interface(), "*", nil); err == nil {
		t.Fatal("read body error want BadRequest")
	}
}

func TestBindProtoRequest_BadJSONBody(t *testing.T) {
	c := protoCtx(t, http.MethodPost, "/v1/req", strings.NewReader(`{"s":`))
	if err := BindProtoRequest(c, allKindsRequest(t).Interface(), "*", nil); err == nil {
		t.Fatal("bad JSON body want BadRequest")
	}
}

func TestBindProtoRequest_QueryBindError(t *testing.T) {
	// query 已知字段但值非法 → 透传到 setProtoField 报错 → BadRequest。
	c := protoCtx(t, http.MethodGet, "/v1/req?i32=abc", nil)
	if err := BindProtoRequest(c, allKindsRequest(t).Interface(), "", nil); err == nil {
		t.Fatal("bad query value want BadRequest")
	}
}

func TestWriteProtoResponse_MarshalError(t *testing.T) {
	m := allKindsRequest(t)
	// 字符串字段含非法 UTF-8 是 protojson.Marshal 会真实报错的输入 → 命中 encode response 分支。
	m.Set(fieldOf(t, m, "s"), protoreflect.ValueOfString("bad\xff\xfe"))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/req", nil)
	WriteProtoResponse(c, m.Interface(), nil)
	if w.Code == http.StatusOK {
		t.Fatalf("invalid-UTF-8 response should not marshal to 200 (body=%q)", w.Body.String())
	}
}
