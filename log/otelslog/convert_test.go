package otelslog

import (
	"fmt"
	"math"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

func TestConvertValuePrimitives(t *testing.T) {
	cases := []struct {
		in   any
		want attribute.Type
		check func(attribute.Value) bool
	}{
		{true, attribute.BOOL, func(v attribute.Value) bool { return v.AsBool() }},
		{"s", attribute.STRING, func(v attribute.Value) bool { return v.AsString() == "s" }},
		{int(3), attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == 3 }},
		{int8(3), attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == 3 }},
		{int16(3), attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == 3 }},
		{int32(3), attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == 3 }},
		{int64(3), attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == 3 }},
		{uint(3), attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == 3 }},
		{uint8(3), attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == 3 }},
		{uint64(3), attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == 3 }},
		{uint64(math.MaxInt64), attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == math.MaxInt64 }},
		{uint64(math.MaxInt64) + 1, attribute.STRING, func(v attribute.Value) bool { return v.AsString() == "9223372036854775808" }},
		{float32(1.5), attribute.FLOAT64, func(v attribute.Value) bool { return v.AsFloat64() == 1.5 }},
		{float64(1.5), attribute.FLOAT64, func(v attribute.Value) bool { return v.AsFloat64() == 1.5 }},
		{time.Second, attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == int64(time.Second) }},
		{time.Unix(1700000000, 0), attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == time.Unix(1700000000, 0).UnixNano() }},
		{[]byte("bytes"), attribute.BYTESLICE, func(v attribute.Value) bool { return string(v.AsByteSlice()) == "bytes" }},
		{fmt.Errorf("e"), attribute.STRING, func(v attribute.Value) bool { return v.AsString() == "e" }},
		{attribute.BoolValue(true), attribute.BOOL, func(v attribute.Value) bool { return v.AsBool() }},
		{complex(1, 2), attribute.MAP, func(v attribute.Value) bool { return len(v.AsMap()) == 2 }},
		{[]string{"a", "b"}, attribute.SLICE, func(v attribute.Value) bool { return len(v.AsSlice()) == 2 }},
		{map[string]int{"k": 1}, attribute.MAP, func(v attribute.Value) bool { return len(v.AsMap()) == 1 }},
		{struct{ A int }{1}, attribute.STRING, func(v attribute.Value) bool { return v.AsString() == "{A:1}" }},
		{nil, attribute.EMPTY, func(v attribute.Value) bool { return v.Type() == attribute.EMPTY }},
		{make(chan int), attribute.STRING, func(v attribute.Value) bool { return v.Type() == attribute.STRING }},
	}
	for _, tc := range cases {
		got := convertValue(tc.in)
		if got.Type() != tc.want {
			t.Errorf("convertValue(%#v): want type %v, got %v (%v)", tc.in, tc.want, got.Type(), got)
			continue
		}
		if !tc.check(got) {
			t.Errorf("convertValue(%#v): check failed, got %v", tc.in, got)
		}
	}
}

func TestConvertValuePointerAndInterface(t *testing.T) {
	// 指针解引用到底层值；nil 指针/nil interface 归 INVALID。
	p := 7
	if v := convertValue(&p); v.Type() != attribute.INT64 || v.AsInt64() != 7 {
		t.Errorf("pointer: got %v", v)
	}
	var np *int
	if v := convertValue(np); v.Type() != attribute.EMPTY {
		t.Errorf("nil pointer: got %v", v)
	}
	var ni any
	if v := convertValue(ni); v.Type() != attribute.EMPTY {
		t.Errorf("nil interface: got %v", v)
	}
}

func TestConvertValueDepthBound(t *testing.T) {
	// 循环引用的 map/slice 不再爆栈：超过 maxConvertDepth 输出哨兵字符串。
	m := map[string]any{}
	m["self"] = m
	if v := convertValue(m); v.Type() != attribute.STRING || v.AsString() != maxDepthExceeded {
		t.Errorf("cyclic map: want %q, got %v", maxDepthExceeded, v)
	}

	s := make([]any, 1)
	s[0] = s
	if v := convertValue(s); v.Type() != attribute.STRING || v.AsString() != maxDepthExceeded {
		t.Errorf("cyclic slice: want %q, got %v", maxDepthExceeded, v)
	}

	// 浅层嵌套不受影响（< maxConvertDepth 深度）。
	deep := any("leaf")
	for i := 0; i < 50; i++ {
		deep = []any{deep}
	}
	if v := convertValue(deep); v.Type() != attribute.SLICE {
		t.Errorf("shallow-deep slice: want SLICE, got %v", v.Type())
	}

	// 恰好在边界内的 map 嵌套：99 层（convertValue 入口吃掉第 100 层余量）。
	nested := map[string]any{"leaf": true}
	for i := 0; i < maxConvertDepth-2; i++ {
		nested = map[string]any{"n": nested}
	}
	if v := convertValue(nested); v.Type() != attribute.MAP {
		t.Errorf("boundary nested map: want MAP, got %v", v.Type())
	}
}
