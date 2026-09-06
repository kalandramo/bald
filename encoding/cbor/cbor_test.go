package cbor

import (
	"testing"

	"github.com/kalandramo/bald/encoding"
)

type user struct {
	Name string `cbor:"name"`
	Age  int    `cbor:"age"`
}

func TestCodec_Name(t *testing.T) {
	var c encoding.Codec = New()
	if c.Name() != "cbor" {
		t.Errorf("Name() = %q, want %q", c.Name(), "cbor")
	}
}

func TestCodec_RoundTrip(t *testing.T) {
	c := New()
	original := user{Name: "Alice", Age: 30}

	data, err := c.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded user
	if err := c.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded != original {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, original)
	}
}
