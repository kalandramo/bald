package json

import (
	"encoding/json"

	"github.com/kalandramo/bald/encoding"
)

// Name is the name registered for the JSON codec.
const Name = "json"

// New returns a fresh json codec for explicit registration
// via encoding.MustRegister.
func New() encoding.Codec { return codec{} }

// Compile-time guarantee that codec satisfies the encoding.Codec contract.
var _ encoding.Codec = codec{}

// codec implements encoding.Codec using encoding/json.
type codec struct{}

// Marshal encodes v into JSON bytes.
func (codec) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Unmarshal decodes JSON data into v.
func (codec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// Name returns the codec name.
func (codec) Name() string {
	return Name
}
