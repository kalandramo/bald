// Package toml provides a [encoding.Codec] implementation using
// github.com/BurntSushi/toml.
//
// Registration is explicit: encoding.MustRegister(toml.New()).
package toml

import (
	"bytes"

	"github.com/BurntSushi/toml"

	"github.com/kalandramo/bald/encoding"
)

// Name is the name registered for the TOML codec.
const Name = "toml"

// New returns a fresh toml codec for explicit registration
// via encoding.MustRegister.
func New() encoding.Codec { return codec{} }

// Compile-time guarantee that codec satisfies the encoding.Codec contract.
var _ encoding.Codec = codec{}

// codec implements encoding.Codec using BurntSushi/toml.
type codec struct{}

// Marshal encodes v into TOML bytes.
func (codec) Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Unmarshal decodes TOML data into v.
func (codec) Unmarshal(data []byte, v any) error {
	return toml.Unmarshal(data, v)
}

// Name returns the codec name.
func (codec) Name() string {
	return Name
}
