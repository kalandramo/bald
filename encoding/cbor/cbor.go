// Package cbor provides a [encoding.Codec] implementation using
// CBOR (Concise Binary Object Representation, RFC 8949) via
// github.com/fxamacker/cbor/v2.
//
// CBOR is a binary data format that is semantically equivalent to JSON but
// significantly smaller and faster. It is used in COSE, WebAuthn, and other
// IETF standards.
//
// Registration is explicit: encoding.MustRegister(cbor.New()).
package cbor

import (
	"github.com/fxamacker/cbor/v2"

	"github.com/kalandramo/bald/encoding"
)

// Name is the name registered for the CBOR codec.
const Name = "cbor"

// New returns a fresh cbor codec for explicit registration
// via encoding.MustRegister.
func New() encoding.Codec { return codec{} }

// codec implements encoding.Codec using fxamacker/cbor/v2.
type codec struct{}

// Marshal encodes v into CBOR binary bytes.
func (codec) Marshal(v any) ([]byte, error) {
	return cbor.Marshal(v)
}

// Unmarshal decodes CBOR binary data into v.
func (codec) Unmarshal(data []byte, v any) error {
	return cbor.Unmarshal(data, v)
}

// Name returns the codec name.
func (codec) Name() string {
	return Name
}
