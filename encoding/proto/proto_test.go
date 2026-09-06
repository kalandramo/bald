package proto

import (
	"testing"
	"time"

	"github.com/kalandramo/bald/encoding"

	"google.golang.org/protobuf/types/known/durationpb"
)

func TestCodec_Name(t *testing.T) {
	var c encoding.Codec = New()
	if c.Name() != "proto" {
		t.Errorf("Name() = %q, want %q", c.Name(), "proto")
	}
}

func TestCodec_RoundTrip(t *testing.T) {
	c := New()
	original := durationpb.New(3 * time.Second)
	data, err := c.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded durationpb.Duration
	if err := c.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded.AsDuration() != 3*time.Second {
		t.Errorf("round-trip mismatch: got %v, want %v", decoded.AsDuration(), 3*time.Second)
	}
}

func TestCodec_NotProtoMessage(t *testing.T) {
	c := New()
	if _, err := c.Marshal(struct{}{}); err == nil {
		t.Error("Marshal(non-proto.Message) should return error")
	}
	if err := c.Unmarshal(nil, &struct{}{}); err == nil {
		t.Error("Unmarshal into non-proto.Message should return error")
	}
}
