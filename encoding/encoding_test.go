package encoding

import (
	"testing"
)

// mockCodec is a minimal Codec implementation for testing.
type mockCodec struct {
	name string
}

func (m mockCodec) Marshal(v any) ([]byte, error)      { return []byte("mock:" + m.name), nil }
func (m mockCodec) Unmarshal(data []byte, v any) error { return nil }
func (m mockCodec) Name() string                       { return m.name }

// ---------------------------------------------------------------------------
// MustRegister / GetCodec
// ---------------------------------------------------------------------------

func TestMustRegisterAndGetCodec(t *testing.T) {
	c := mockCodec{name: "testformat"}
	MustRegister(c)

	got := GetCodec("testformat")
	if got == nil {
		t.Fatal("GetCodec returned nil after MustRegister")
	}
	if got.Name() != "testformat" {
		t.Errorf("GetCodec().Name() = %q, want %q", got.Name(), "testformat")
	}
}

func TestGetCodec_CaseInsensitive(t *testing.T) {
	MustRegister(mockCodec{name: "CamelCase"})

	// Lookup should be case-insensitive
	if GetCodec("camelcase") == nil {
		t.Error(`GetCodec("camelcase") should find registered "CamelCase"`)
	}
	if GetCodec("CAMELCASE") == nil {
		t.Error(`GetCodec("CAMELCASE") should find registered "CamelCase"`)
	}
	if GetCodec("CamelCase") == nil {
		t.Error(`GetCodec("CamelCase") should find registered "CamelCase"`)
	}
}

func TestGetCodec_NotFound(t *testing.T) {
	if got := GetCodec("nonexistent"); got != nil {
		t.Errorf("GetCodec(%q) = %v, want nil", "nonexistent", got)
	}
}

func TestGetCodec_EmptyString(t *testing.T) {
	if got := GetCodec(""); got != nil {
		t.Errorf(`GetCodec("") = %v, want nil`, got)
	}
}

// ---------------------------------------------------------------------------
// Panic cases（注册期编程错误 fail-fast）
// ---------------------------------------------------------------------------

func TestMustRegister_NilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustRegister(nil) should panic")
		}
	}()
	MustRegister(nil)
}

func TestMustRegister_EmptyNamePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustRegister with empty name should panic")
		}
	}()
	MustRegister(mockCodec{name: ""})
}

func TestMustRegister_DuplicatePanics(t *testing.T) {
	MustRegister(mockCodec{name: "dup-test"})
	defer func() {
		if r := recover(); r == nil {
			t.Error("duplicate MustRegister should panic")
		}
	}()
	MustRegister(mockCodec{name: "dup-test"})
}

// ---------------------------------------------------------------------------
// Names
// ---------------------------------------------------------------------------

func TestNames(t *testing.T) {
	MustRegister(mockCodec{name: "zeta"})
	MustRegister(mockCodec{name: "Alpha"})

	names := Names()
	found := 0
	for _, n := range names {
		if n == "zeta" || n == "alpha" {
			found++
		}
	}
	if found != 2 {
		t.Errorf("Names() should contain alpha/zeta, got %v", names)
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("Names() not sorted: %v", names)
			break
		}
	}
}

// ---------------------------------------------------------------------------
// Codec interface compliance
// ---------------------------------------------------------------------------

var _ Codec = mockCodec{}

func TestCodecInterface(t *testing.T) {
	c := mockCodec{name: "interface-test"}

	data, err := c.Marshal("anything")
	if err != nil {
		t.Errorf("Marshal() error = %v", err)
	}
	if string(data) != "mock:interface-test" {
		t.Errorf("Marshal() = %q, want %q", data, "mock:interface-test")
	}

	if err := c.Unmarshal(nil, nil); err != nil {
		t.Errorf("Unmarshal() error = %v", err)
	}

	if c.Name() != "interface-test" {
		t.Errorf("Name() = %q, want %q", c.Name(), "interface-test")
	}
}
