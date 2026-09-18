// Package xml provides a [encoding.Codec] implementation using Go's
// standard encoding/xml package.
//
// Registration is explicit: encoding.MustRegister(xml.New()).
package xml

import (
	"encoding/xml"

	"github.com/kalandramo/bald/encoding"
)

// Name is the name registered for the XML codec.
const Name = "xml"

// New returns a fresh xml codec for explicit registration
// via encoding.MustRegister.
func New() encoding.Codec { return codec{} }

// Compile-time guarantee that codec satisfies the encoding.Codec contract.
var _ encoding.Codec = codec{}

// codec implements encoding.Codec using encoding/xml.
type codec struct{}

// Marshal encodes v into XML bytes.
func (codec) Marshal(v any) ([]byte, error) {
	return xml.Marshal(v)
}

// Unmarshal decodes XML data into v.
func (codec) Unmarshal(data []byte, v any) error {
	return xml.Unmarshal(data, v)
}

// Name returns the codec name.
func (codec) Name() string {
	return Name
}
