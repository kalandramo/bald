// Package avro provides a [encoding.Codec] implementation using Apache Avro
// via github.com/linkedin/goavro/v2.
//
// Avro is a schema-based serialization format widely used in big-data
// pipelines (Kafka, Hadoop, Druid). Unlike schemaless formats (JSON, CBOR),
// Avro requires an Avro schema (JSON-encoded) to encode and decode data.
//
// Create a codec by providing the schema at construction time:
//
//	schema := `{"type":"record","name":"User","fields":[{"name":"name","type":"string"}]}`
//	codec, err := avro.NewCodec(schema)
//	if err != nil { ... }
//
//	data, err := codec.Marshal(map[string]any{"name": "Alice"})
//
// The default codec returned by New() carries no schema: it exists so the
// format can be registered under the name "avro". It falls back to the Avro
// `"null"` schema internally, so it can only encode/decode Avro null values —
// it is NOT a general-purpose primitive codec. For any real data (including
// plain strings and ints), always use [NewCodec] with an explicit schema.
package avro

import (
	"fmt"

	"github.com/linkedin/goavro/v2"

	"github.com/kalandramo/bald/encoding"
)

// Name is the name registered for the Avro codec.
const Name = "avro"

// New returns a fresh avro codec for explicit registration
// via encoding.MustRegister.
func New() encoding.Codec { return codec{avroCodec: nil} }

// Compile-time guarantee that codec satisfies the encoding.Codec contract.
var _ encoding.Codec = codec{}

// codec implements encoding.Codec using linkedin/goavro/v2.
type codec struct {
	avroCodec *goavro.Codec
}

// NewCodec creates an Avro codec from the given Avro schema (JSON-encoded).
// This is the recommended way to use Avro for complex record types.
func NewCodec(schema string) (encoding.Codec, error) {
	c, err := goavro.NewCodec(schema)
	if err != nil {
		return nil, fmt.Errorf("avro: invalid schema: %w", err)
	}
	return codec{avroCodec: c}, nil
}

// Marshal encodes v into Avro binary bytes.
// v should be a native Go type (map[string]any, or a struct via goavro's
// field-name mapping) compatible with the codec's schema.
func (c codec) Marshal(v any) ([]byte, error) {
	ac := c.avroCodec
	if ac == nil {
		// Fallback for the registered default codec — use null schema.
		var err error
		ac, err = goavro.NewCodec(`"null"`)
		if err != nil {
			return nil, err
		}
	}
	bb, err := ac.BinaryFromNative(nil, v)
	if err != nil {
		return nil, fmt.Errorf("avro: marshal error: %w", err)
	}
	return bb, nil
}

// Unmarshal decodes Avro binary data into v.
// The decoded result is returned as a native Go type (map[string]any).
// v must be a pointer to a map[string]any or *any.
func (c codec) Unmarshal(data []byte, v any) error {
	ac := c.avroCodec
	if ac == nil {
		var err error
		ac, err = goavro.NewCodec(`"null"`)
		if err != nil {
			return err
		}
	}
	native, _, err := ac.NativeFromBinary(data)
	if err != nil {
		return fmt.Errorf("avro: unmarshal error: %w", err)
	}

	// Assign the decoded native value to the target pointer.
	switch target := v.(type) {
	case *any:
		*target = native
	case *map[string]any:
		if m, ok := native.(map[string]any); ok {
			*target = m
		} else {
			return fmt.Errorf("avro: decoded value is not a map[string]any, got %T", native)
		}
	default:
		return fmt.Errorf("avro: unsupported unmarshal target %T (use *any or *map[string]any)", v)
	}
	return nil
}

// Name returns the codec name.
func (codec) Name() string {
	return Name
}
