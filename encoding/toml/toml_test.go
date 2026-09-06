package toml

import (
	"testing"

	"github.com/kalandramo/bald/encoding"
)

type testStruct struct {
	Name  string `toml:"name"`
	Value int    `toml:"value"`
}

func TestCodec_Name(t *testing.T) {
	var c encoding.Codec = codec{}
	if c.Name() != "toml" {
		t.Errorf("Name() = %q, want %q", c.Name(), "toml")
	}
}

func TestCodec_RoundTrip(t *testing.T) {
	c := codec{}
	original := testStruct{Name: "hello", Value: 42}
	data, err := c.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded testStruct
	if err := c.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if decoded.Name != original.Name || decoded.Value != original.Value {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, original)
	}
}

func TestCodec_Registered(t *testing.T) {
	encoding.MustRegister(New()) // 显式注册（框架不用 init() 自注册）
	if c := encoding.GetCodec("toml"); c == nil {
		t.Fatal("GetCodec(\"toml\") returned nil after MustRegister")
	}
}
