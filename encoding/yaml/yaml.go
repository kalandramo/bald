package yaml

import (
	"github.com/kalandramo/bald/encoding"
	"gopkg.in/yaml.v3"
)

// Name is the name registered for the YAML codec.
const Name = "yaml"

// New returns a fresh yaml codec for explicit registration
// via encoding.MustRegister.
func New() encoding.Codec { return codec{} }

// Compile-time guarantee that codec satisfies the encoding.Codec contract.
var _ encoding.Codec = codec{}

// codec implements encoding.Codec using gopkg.in/yaml.v3.
type codec struct{}

// Marshal encodes v into YAML bytes.
func (codec) Marshal(v any) ([]byte, error) {
	return yaml.Marshal(v)
}

// Unmarshal decodes YAML data into v.
func (codec) Unmarshal(data []byte, v any) error {
	return yaml.Unmarshal(data, v)
}

// Name returns the codec name.
func (codec) Name() string {
	return Name
}
