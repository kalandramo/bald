package otelslog

// coverage_gaps_test.go —— 77.4% 缺口补齐：嵌套值转换的类型矩阵、深度边界、
// Option 组装与 attrs/group 的错误晋升分支。上游语义不变，只补测试。

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

// TestConvertValueTopLevelTypes 顶层类型矩阵中已覆盖之外的标量。
func TestConvertValueTopLevelTypes(t *testing.T) {
	cases := []struct {
		in   any
		want attribute.Type
	}{
		{uint16(3), attribute.INT64},
		{uint32(3), attribute.INT64},
		{uintptr(3), attribute.INT64},
		{complex64(1 + 2i), attribute.MAP},
		{map[int]string{7: "v"}, attribute.MAP},
	}
	for _, tc := range cases {
		if got := convertValue(tc.in); got.Type() != tc.want {
			t.Errorf("convertValue(%#v): want %v, got %v", tc.in, tc.want, got.Type())
		}
	}
	// 非字符串 map 键按 %+v 文本落键。
	v := convertValue(map[int]string{7: "v"})
	if kv := v.AsMap(); len(kv) != 1 || string(kv[0].Key) != "7" {
		t.Errorf("non-string map key = %v, want key \"7\"", kv)
	}
}

// TestConvertValueDepthTypeMatrix 嵌套路径的类型全集（reflect 分支逐项）。
func TestConvertValueDepthTypeMatrix(t *testing.T) {
	ptr := 7
	var nilPtr *int
	nested := []any{
		int8(1), int16(1), int32(1), int64(1),
		uint(1), uint8(1), uint16(1), uint32(1), uint64(1), uintptr(1),
		float32(1.5), float64(1.5), complex64(1 + 2i), complex128(1 + 2i),
		time.Duration(5), time.Unix(1700000000, 0),
		[]byte("b"), errors.New("e"), attribute.BoolValue(true),
		struct{ A int }{1},
		&ptr,
		nilPtr,
		[]string{},             // 空切片
		map[int]int{},          // 空 map
		map[int]string{1: "a"}, // 非字符串键
		make(chan int),         // 未支持 Kind → unhandled
	}
	got := convertValue(nested)
	if got.Type() != attribute.SLICE {
		t.Fatalf("nested slice type = %v, want SLICE", got.Type())
	}
	items := got.AsSlice()
	if len(items) != len(nested) {
		t.Fatalf("nested items = %d, want %d", len(items), len(nested))
	}
	if items[0].Type() != attribute.INT64 || items[0].AsInt64() != 1 {
		t.Errorf("int8 item = %v", items[0])
	}
	if items[3].Type() != attribute.INT64 || items[3].AsInt64() != 1 {
		t.Errorf("int64 item = %v", items[3])
	}
	if items[12].Type() != attribute.MAP {
		t.Errorf("complex64 item = %v, want MAP", items[12].Type())
	}
	if items[15].Type() != attribute.INT64 {
		t.Errorf("time item = %v, want INT64", items[15].Type())
	}
	if items[19].Type() != attribute.STRING || items[19].AsString() != "{A:1}" {
		t.Errorf("struct item = %v", items[19])
	}
	if items[20].Type() != attribute.INT64 || items[20].AsInt64() != 7 {
		t.Errorf("pointer item = %v", items[20])
	}
	if items[21].Type() != attribute.EMPTY {
		t.Errorf("nil pointer item = %v, want EMPTY", items[21])
	}
	if items[22].Type() != attribute.SLICE || len(items[22].AsSlice()) != 0 {
		t.Errorf("empty slice item = %v", items[22])
	}
	if items[23].Type() != attribute.MAP || len(items[23].AsMap()) != 0 {
		t.Errorf("empty map item = %v", items[23])
	}
	if items[24].Type() != attribute.MAP || string(items[24].AsMap()[0].Key) != "1" {
		t.Errorf("non-string key item = %v", items[24])
	}
	if items[25].Type() != attribute.STRING {
		t.Errorf("unhandled kind item = %v, want STRING", items[25].Type())
	}
}

// TestConvertValueDepthInterfaceNil 嵌套 nil interface 归 EMPTY。
func TestConvertValueDepthInterfaceNil(t *testing.T) {
	got := convertValue([]any{nil})
	if got.AsSlice()[0].Type() != attribute.EMPTY {
		t.Fatalf("nested nil = %v, want EMPTY", got.AsSlice()[0])
	}
}

// TestConvertValuePointerDepthBound 指针自引用链超深度 → 哨兵。
func TestConvertValuePointerDepthBound(t *testing.T) {
	var v any = int8(1)
	for i := 0; i < maxConvertDepth; i++ {
		v = &v
	}
	got := convertValue(v)
	if got.Type() != attribute.STRING || got.AsString() != maxDepthExceeded {
		t.Fatalf("pointer depth = %v, want %q", got, maxDepthExceeded)
	}
}

// TestNewLoggerOptions 组装路径：version/schemaURL/attributes/level/source 全透传。
func TestNewLoggerOptions(t *testing.T) {
	logger := &fakeLogger{}
	sl := NewLogger("bald/gaps",
		WithLoggerProvider(&fakeProvider{logger: logger}),
		WithVersion("v1.2.3"),
		WithSchemaURL("https://opentelemetry.io/schemas/1.43.0"),
		WithAttributes(attribute.String("scope", "test")),
		WithLevelString("WARN"),
		WithSource(true),
	)
	sl.Debug("gated")
	if len(logger.emitted) != 0 {
		t.Fatalf("Debug should be gated by WARN, emitted=%d", len(logger.emitted))
	}
	sl.Warn("pass", "k", "v")
	if len(logger.emitted) != 1 {
		t.Fatalf("Warn emitted = %d, want 1", len(logger.emitted))
	}
	if _, ok := findAttr(&logger.emitted[0].rec, string(semconvCodeFilePath)); !ok {
		t.Error("source attr missing")
	}
}

// TestNewHandlerDefaultProvider 零 Option：回落全局 LoggerProvider（不 panic）。
func TestNewHandlerDefaultProvider(t *testing.T) {
	h := NewHandler("bald/gaps")
	_ = h.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "x", 0))
}

// TestHandlerAttrsErrorPromotion WithAttrs 携带 error：晋升为记录错误，
// 其余属性仍保留。
func TestHandlerAttrsErrorPromotion(t *testing.T) {
	h, logger := newTestHandler(t)
	sentinel := errors.New("attr boom")
	sl := slog.New(h.WithAttrs([]slog.Attr{
		slog.Any("err", sentinel),
		slog.String("keep", "yes"),
	}))
	sl.Info("msg")

	rec := logger.emitted[0].rec
	if rec.Err() == nil || rec.Err().Error() != "attr boom" {
		t.Fatalf("record err = %v, want attr boom", rec.Err())
	}
	if v, ok := findAttr(&rec, "keep"); !ok || v.AsString() != "yes" {
		t.Fatalf("keep attr = %v (found=%v)", v, ok)
	}
}

// TestGroupAttrsErrorPromotion WithGroup 链上的 error：同样晋升记录错误。
func TestGroupAttrsErrorPromotion(t *testing.T) {
	h, logger := newTestHandler(t)
	sl := slog.New(h.WithGroup("g").WithAttrs([]slog.Attr{slog.Any("err", errors.New("group boom"))}))
	sl.Info("msg")

	rec := logger.emitted[0].rec
	if rec.Err() == nil || rec.Err().Error() != "group boom" {
		t.Fatalf("record err = %v, want group boom", rec.Err())
	}
	if n := rec.AttributesLen(); n != 0 {
		t.Fatalf("attrs = %d, want 0 (error promoted, empty group elided)", n)
	}
}

// TestGroupEmptyPayloadFallsToGroupAttrs 记录侧属性全被晋升（buf 为空）+
// 组自身有属性：走 NextNonEmpty 兜底输出组。
func TestGroupEmptyPayloadFallsToGroupAttrs(t *testing.T) {
	h, logger := newTestHandler(t)
	sl := slog.New(h.WithGroup("g").WithAttrs([]slog.Attr{slog.String("a", "1")}))
	sl.Info("msg", slog.Any("err", errors.New("promoted")))

	rec := logger.emitted[0].rec
	if rec.Err() == nil || rec.Err().Error() != "promoted" {
		t.Fatalf("record err = %v, want promoted", rec.Err())
	}
	g, ok := findAttr(&rec, "g")
	if !ok || g.Type() != attribute.MAP {
		t.Fatalf("group g = %v (found=%v)", g.Type(), ok)
	}
	var a string
	for _, kv := range g.AsMap() {
		if string(kv.Key) == "a" {
			a = kv.Value.AsString()
		}
	}
	if a != "1" {
		t.Fatalf("g.a = %q, want 1", a)
	}
}

// TestWithAttrsCloneAndNils WithAttrs 二次调用走 Clone；nil 接收者 Clone 返回 nil。
func TestWithAttrsCloneAndNils(t *testing.T) {
	h, logger := newTestHandler(t)
	h1 := h.WithAttrs([]slog.Attr{slog.String("a", "1")})
	h2 := h1.WithAttrs([]slog.Attr{slog.String("b", "2")})
	slog.New(h2).Info("msg")

	rec := logger.emitted[0].rec
	for _, key := range []string{"a", "b"} {
		if _, ok := findAttr(&rec, key); !ok {
			t.Errorf("attr %s missing after chained WithAttrs", key)
		}
	}

	var g *group
	if g.Clone() != nil {
		t.Error("nil group Clone must return nil")
	}
	var b *kvBuffer
	if b.Clone() != nil {
		t.Error("nil kvBuffer Clone must return nil")
	}
}

// TestConvertBoolKind 记录属性走 convert 的 KindBool 分支。
func TestConvertBoolKind(t *testing.T) {
	h, logger := newTestHandler(t)
	slog.New(h).Info("b", slog.Bool("ok", true))
	rec := logger.emitted[0].rec
	if v, ok := findAttr(&rec, "ok"); !ok || !v.AsBool() {
		t.Fatalf("ok attr = %v (found=%v)", v, ok)
	}
}

// TestConvertUint64Fit uint64 在 int64 范围内走 Int64Value（KindUint64 另一分支）。
func TestConvertUint64Fit(t *testing.T) {
	v := convert(slog.Uint64Value(uint64(math.MaxInt64)))
	if v.Type() != attribute.INT64 || v.AsInt64() != math.MaxInt64 {
		t.Fatalf("fit uint64 = %v, want INT64 MaxInt64", v)
	}
}

// 结构性零覆盖（登记备查）：
//   - handler.go:594-602 —— convert 的 default 分支：slog.Kind 是封闭枚举，
//     公开 API 构造不出枚举外的 Value（零值即 KindAny），保留为上游防御代码。
