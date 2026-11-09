package avro

import (
	"testing"

	"github.com/kalandramo/bald/encoding"
)

// TestDefaultCodecNullSchema 覆盖 New() 注册用的无 schema 默认 codec：
// 回退 "null" schema，只能编解码 Avro null，非 null 输入报错。
func TestDefaultCodecNullSchema(t *testing.T) {
	var c encoding.Codec = New()
	if c.Name() != Name {
		t.Fatalf("Name() = %q, want %q", c.Name(), Name)
	}

	data, err := c.Marshal(nil)
	if err != nil {
		t.Fatalf("Marshal(nil): %v", err)
	}

	var got any
	if err := c.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != nil {
		t.Fatalf("decoded = %#v, want nil", got)
	}

	if _, err := c.Marshal("not-null"); err == nil {
		t.Fatal("default codec must reject non-null values")
	}
}

// TestUnmarshalTargets 覆盖 *any / *map[string]any 成功与类型不符/不支持目标的错误。
func TestUnmarshalTargets(t *testing.T) {
	c, err := NewCodec(userSchema)
	if err != nil {
		t.Fatalf("NewCodec: %v", err)
	}
	data, err := c.Marshal(map[string]any{"name": "Bob", "age": 7})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var anyTarget any
	if err := c.Unmarshal(data, &anyTarget); err != nil {
		t.Fatalf("Unmarshal(*any): %v", err)
	}
	if m, ok := anyTarget.(map[string]any); !ok || m["name"] != "Bob" {
		t.Fatalf("*any target = %#v", anyTarget)
	}

	var mapTarget map[string]any
	if err := c.Unmarshal(data, &mapTarget); err != nil {
		t.Fatalf("Unmarshal(*map): %v", err)
	}
	if mapTarget["age"] != int32(7) {
		t.Fatalf("*map target = %#v", mapTarget)
	}

	// null schema 解码出的 nil 不是 map → 类型不符错误。
	if err := New().Unmarshal(nil, &mapTarget); err == nil {
		t.Fatal("expected type-mismatch error for *map target with null value")
	}

	// 不支持的目标类型。
	var s string
	if err := c.Unmarshal(data, &s); err == nil {
		t.Fatal("expected unsupported-target error")
	}
}

// 结构性零覆盖（登记备查）：
//   avro.go:65-67 / 84-86 —— 回退 schema 是字面量 `"null"`，goavro 编译不可能失败，
//   两处错误分支为防御性代码，无法从测试注入。
