// Package gob provides a [encoding.Codec] implementation using Go's
// standard encoding/gob package.
//
// Gob is a streaming binary format designed for efficient exchange of
// Go data structures. It is well-suited for Go-to-Go communication
// (e.g. internal RPC) but not interoperable with other languages.
//
// Registration is explicit: encoding.MustRegister(gob.New()).
package gob

import (
	"bytes"
	"encoding/gob"

	"github.com/kalandramo/bald/encoding"
)

// Name is the name registered for the gob codec.
const Name = "gob"

// New returns a fresh gob codec for explicit registration
// via encoding.MustRegister.
func New() encoding.Codec { return codec{} }

// codec implements encoding.Codec using encoding/gob.
type codec struct{}

// Marshal encodes v into gob bytes.
func (codec) Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Unmarshal decodes gob data into v.
func (codec) Unmarshal(data []byte, v any) error {
	dec := gob.NewDecoder(bytes.NewReader(data))
	return dec.Decode(v)
}

// Name returns the codec name.
func (codec) Name() string {
	return Name
}
