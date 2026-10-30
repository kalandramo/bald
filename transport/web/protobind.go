package web

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/kalandramo/bald/berrors"
)

// BindProtoRequest 按 google.api.http 语义把一次 HTTP 请求装入 proto 请求消息，
// 供生成物（bald gen domain 的注解派生绑定）的 gin 路由直接复用：
//
//   - body：注解的 body（"*" 整个请求体；"" 无 body；其他为请求字段路径）；
//   - pathVars：路径变量（key 为注解里的变量名，可含 "." 表示嵌套字段）；
//   - query：body 未覆盖的字段从 URL 查询参数绑定（GET/DELETE 的常见形态）。
//
// body 走 protojson——与 grpc-gateway 转码同一 JSON 语义（字段名、枚举、WKT、
// 64 位整数都按 proto JSON 规则）；path/query 的字符串按字段声明类型转换。
// 未知查询参数忽略（与 grpc-gateway 一致，客户端常带缓存穿透参数）；已知
// 字段转换失败返回 berrors.BadRequest。
func BindProtoRequest(c *gin.Context, msg proto.Message, body string, pathVars map[string]string) error {
	if msg == nil {
		return berrors.BadRequest("").WithMessage("nil request message")
	}
	m := msg.ProtoReflect()
	if body != "" && c.Request.Body != nil {
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return berrors.BadRequest("").WithMessage("read request body: %s", err)
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			payload := raw
			if body != "*" {
				payload = wrapBodyField(body, raw)
			}
			if err := protojson.Unmarshal(payload, msg); err != nil {
				return berrors.BadRequest("").WithMessage("decode request body: %s", err)
			}
		}
	}
	for _, key := range sortedKeys(pathVars) {
		if err := setProtoPath(m, key, []string{pathVars[key]}, true); err != nil {
			return berrors.BadRequest("").WithMessage("bind path parameter %q: %s", key, err)
		}
	}
	query := c.Request.URL.Query()
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if coveredByBody(body, key) || coveredByPathVar(pathVars, key) {
			continue
		}
		values := query[key]
		err := setProtoPath(m, key, values, false)
		if errors.Is(err, errUnknownField) {
			continue
		}
		if err != nil {
			return berrors.BadRequest("").WithMessage("bind query parameter %q: %s", key, err)
		}
	}
	return nil
}

// WriteProtoResponse 把 proto 响应写成 REST JSON：err 非 nil 时走统一错误体，
// 否则用 protojson 编码（与 grpc-gateway 输出同形）。空响应只置 200。
func WriteProtoResponse(c *gin.Context, resp proto.Message, err error) {
	if err != nil {
		ErrorResponse(c, err)
		return
	}
	if resp == nil || !resp.ProtoReflect().IsValid() {
		c.Status(http.StatusOK)
		return
	}
	data, mErr := protojson.Marshal(resp)
	if mErr != nil {
		ErrorResponse(c, berrors.Internal("").WithMessage("encode response: %s", mErr))
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", data)
}

// errUnknownField 表示路径/查询参数没有对应的请求字段（查询侧忽略）。
var errUnknownField = errors.New("unknown request field")

// wrapBodyField 把 body 的原始 JSON 挂到注解指定的字段路径下。google.api.http
// 允许 body 是嵌套字段路径（如 "user.name"）。
func wrapBodyField(fieldPath string, raw []byte) []byte {
	segs := strings.Split(fieldPath, ".")
	var b bytes.Buffer
	for _, seg := range segs {
		b.WriteString(`{"`)
		b.WriteString(seg)
		b.WriteString(`":`)
	}
	b.Write(raw)
	b.WriteString(strings.Repeat("}", len(segs)))
	return b.Bytes()
}

// coveredByBody 报告查询参数是否已被 body 覆盖（body "*" 覆盖全部）。
func coveredByBody(body, key string) bool {
	switch {
	case body == "*":
		return true
	case body == "":
		return false
	default:
		return key == body || strings.HasPrefix(key, body+".")
	}
}

// coveredByPathVar 报告查询参数是否与路径变量指向同一字段（路径优先）。
func coveredByPathVar(pathVars map[string]string, key string) bool {
	if _, ok := pathVars[key]; ok {
		return true
	}
	for varName := range pathVars {
		if key == varName || strings.HasPrefix(key, varName+".") ||
			strings.HasPrefix(varName, key+".") {
			return true
		}
	}
	return false
}

// setProtoPath 按 "." 分隔的字段路径写入取值；strict 为 true 时未知字段报错
// （路径变量是注解与 proto 的契约，失配必须响亮），false 时返回 errUnknownField。
func setProtoPath(m protoreflect.Message, path string, values []string, strict bool) error {
	segs := strings.Split(path, ".")
	for i, seg := range segs {
		fd := fieldByName(m.Descriptor(), seg)
		if fd == nil {
			if strict {
				return fmt.Errorf("no such field %q in %s", seg, m.Descriptor().FullName())
			}
			return errUnknownField
		}
		if i == len(segs)-1 {
			return setProtoField(m, fd, values)
		}
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			return fmt.Errorf("field %q is not a message", seg)
		}
		if fd.IsList() || fd.IsMap() {
			return fmt.Errorf("field %q is repeated and cannot be traversed", seg)
		}
		m = m.Mutable(fd).Message()
	}
	return nil
}

// fieldByName 同时接受 proto 字段名与 JSON 名（与 protojson 的宽松口径一致）。
func fieldByName(md protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	fields := md.Fields()
	if fd := fields.ByName(protoreflect.Name(name)); fd != nil {
		return fd
	}
	return fields.ByJSONName(name)
}

// setProtoField 把字符串取值写入单个字段（重复字段按出现序追加）。
func setProtoField(m protoreflect.Message, fd protoreflect.FieldDescriptor, values []string) error {
	if fd.IsMap() {
		return fmt.Errorf("map field %q cannot be bound from a path or query parameter", fd.Name())
	}
	if fd.IsList() {
		if k := fd.Kind(); k == protoreflect.MessageKind || k == protoreflect.GroupKind {
			return fmt.Errorf("repeated message field %q cannot be bound from a path or query parameter", fd.Name())
		}
		list := m.Mutable(fd).List()
		for _, s := range values {
			v, err := scalarValue(m, fd, s)
			if err != nil {
				return err
			}
			list.Append(v)
		}
		return nil
	}
	if len(values) == 0 {
		return nil
	}
	v, err := scalarValue(m, fd, values[0])
	if err != nil {
		return err
	}
	m.Set(fd, v)
	return nil
}

// scalarValue 按字段声明类型把 URL 字符串转换为 protoreflect 值；消息字段
// （Timestamp/Duration/FieldMask 等）按 proto JSON 的字符串表示解码。
func scalarValue(m protoreflect.Message, fd protoreflect.FieldDescriptor, s string) (protoreflect.Value, error) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(s), nil
	case protoreflect.BytesKind:
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid base64 value %q", s)
		}
		return protoreflect.ValueOfBytes(b), nil
	case protoreflect.BoolKind:
		v, err := strconv.ParseBool(s)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid bool value %q", s)
		}
		return protoreflect.ValueOfBool(v), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		v, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid int32 value %q", s)
		}
		return protoreflect.ValueOfInt32(int32(v)), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid int64 value %q", s)
		}
		return protoreflect.ValueOfInt64(v), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid uint32 value %q", s)
		}
		return protoreflect.ValueOfUint32(uint32(v)), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid uint64 value %q", s)
		}
		return protoreflect.ValueOfUint64(v), nil
	case protoreflect.FloatKind:
		v, err := strconv.ParseFloat(s, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid float value %q", s)
		}
		return protoreflect.ValueOfFloat32(float32(v)), nil
	case protoreflect.DoubleKind:
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid double value %q", s)
		}
		return protoreflect.ValueOfFloat64(v), nil
	case protoreflect.EnumKind:
		if ev := fd.Enum().Values().ByName(protoreflect.Name(s)); ev != nil {
			return protoreflect.ValueOfEnum(ev.Number()), nil
		}
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("unknown %s value %q", fd.Enum().FullName(), s)
		}
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(n)), nil
	case protoreflect.MessageKind, protoreflect.GroupKind:
		sub := m.NewField(fd).Message()
		if err := protojson.Unmarshal([]byte(strconv.Quote(s)), sub.Interface()); err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid %s value %q", fd.Message().FullName(), s)
		}
		return protoreflect.ValueOfMessage(sub), nil
	default:
		return protoreflect.Value{}, fmt.Errorf("unsupported field kind %s", fd.Kind())
	}
}

// sortedKeys 让路径变量的绑定顺序确定（错误信息与副作用都可复现）。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
