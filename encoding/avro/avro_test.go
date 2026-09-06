package avro

import (
	"testing"

	"github.com/kalandramo/bald/encoding"
)

const userSchema = `{"type":"record","name":"user","fields":[{"name":"name","type":"string"},{"name":"age","type":"int"}]}`

func TestCodec_Name(t *testing.T) {
	c, err := NewCodec(userSchema)
	if err != nil {
		t.Fatalf("NewCodec() error = %v", err)
	}
	var cc encoding.Codec = c
	if cc.Name() != "avro" {
		t.Errorf("Name() = %q, want %q", cc.Name(), "avro")
	}
}

func TestCodec_RoundTrip(t *testing.T) {
	c, err := NewCodec(userSchema)
	if err != nil {
		t.Fatalf("NewCodec() error = %v", err)
	}
	original := map[string]any{"name": "Alice", "age": 30}

	data, err := c.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := c.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded["name"] != "Alice" {
		t.Errorf("round-trip mismatch: got %+v", decoded)
	}
}

func TestNewCodec_InvalidSchema(t *testing.T) {
	if _, err := NewCodec("not-a-json"); err == nil {
		t.Error("NewCodec(invalid schema) should return error")
	}
}
