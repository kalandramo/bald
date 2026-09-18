// Package msgpack provides a [encoding.Codec] implementation using
// github.com/vmihailenco/msgpack/v5.
//
// MessagePack is a compact binary serialization format that is
// cross-language and more efficient than JSON for structured data.
//
// Registration is explicit: encoding.MustRegister(msgpack.New()).
package msgpack

import (
	"github.com/kalandramo/bald/encoding"
	"github.com/vmihailenco/msgpack/v5"
)

// Name is the name registered for the MessagePack codec.
const Name = "msgpack"

// New returns a fresh msgpack codec for explicit registration
// via encoding.MustRegister.
func New() encoding.Codec { return codec{} }

// Compile-time guarantee that codec satisfies the encoding.Codec contract.
var _ encoding.Codec = codec{}

// codec implements encoding.Codec using vmihailenco/msgpack/v5.
type codec struct{}

// Marshal encodes v into MessagePack bytes.
func (codec) Marshal(v any) ([]byte, error) {
	return msgpack.Marshal(v)
}

// Unmarshal decodes MessagePack data into v.
func (codec) Unmarshal(data []byte, v any) error {
	return msgpack.Unmarshal(data, v)
}

// Name returns the codec name.
func (codec) Name() string {
	return Name
}
