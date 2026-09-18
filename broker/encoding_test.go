package broker

import (
	"errors"
	"strings"
	"testing"

	"github.com/kalandramo/bald/encoding"
)

// 无 codec 且消息体非 []byte/string 时必须 fail-fast，不得静默走 gob。
// 回归自「DefaultCodec 恒为 nil → 配了 JSON 心智实际发 gob」的静默错配。
func TestMarshal_NoCodecNonBytesFailsFast(t *testing.T) {
	type payload struct{ A int }
	_, err := Marshal(nil, payload{A: 1})
	if err == nil {
		t.Fatal("expected error when codec is nil and body is not []byte/string, got nil")
	}
	if !errors.Is(err, errNoCodec) && !strings.Contains(err.Error(), "encoding.MustRegister") {
		t.Fatalf("error should carry a registration hint, got: %v", err)
	}
}

func TestUnmarshal_NoCodecNonBytesFailsFast(t *testing.T) {
	type payload struct{ A int }
	var out payload
	err := Unmarshal(nil, []byte("whatever"), &out)
	if err == nil {
		t.Fatal("expected error when codec is nil and target is not *[]byte/*string, got nil")
	}
	if !strings.Contains(err.Error(), "encoding.MustRegister") {
		t.Fatalf("error should carry a registration hint, got: %v", err)
	}
}

// 透传路径必须保留：[]byte / string 无 codec 直通。
func TestMarshal_NoCodecPassthrough(t *testing.T) {
	b, err := Marshal(nil, []byte("raw"))
	if err != nil || string(b) != "raw" {
		t.Fatalf("bytes passthrough broken: b=%q err=%v", b, err)
	}
	s, err := Marshal(nil, "str")
	if err != nil || string(s) != "str" {
		t.Fatalf("string passthrough broken: s=%q err=%v", s, err)
	}
}

func TestUnmarshal_NoCodecPassthrough(t *testing.T) {
	var bs []byte
	if err := Unmarshal(nil, []byte("raw"), &bs); err != nil || string(bs) != "raw" {
		t.Fatalf("bytes passthrough broken: bs=%q err=%v", bs, err)
	}
	var s string
	if err := Unmarshal(nil, []byte("raw"), &s); err != nil || s != "raw" {
		t.Fatalf("string passthrough broken: s=%q err=%v", s, err)
	}
}

// NewOptions 惰性查表：注册后能拿到真实 codec（修复前包级 var 恒 nil）。
func TestNewOptions_LazyCodecLookup(t *testing.T) {
	if encoding.GetCodec("json") == nil {
		encoding.MustRegister(jsonProbeCodec{})
	}
	opt := NewOptions()
	if opt.Codec == nil {
		t.Fatal("NewOptions().Codec should resolve a registered codec, got nil")
	}
	if opt.Codec.Name() != "json" {
		t.Fatalf("Codec.Name() = %q, want json", opt.Codec.Name())
	}
}

type jsonProbeCodec struct{}

func (jsonProbeCodec) Name() string                       { return "json" }
func (jsonProbeCodec) Marshal(v any) ([]byte, error)      { return nil, nil }
func (jsonProbeCodec) Unmarshal(data []byte, v any) error { return nil }
