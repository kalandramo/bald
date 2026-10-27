package schema

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// newCache creates a new schema cache for the given type.
func newCache[T any]() *cache {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("Failed to generate schema: %v", err))
	}

	data, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("Failed to marshal schema: %v", err))
	}

	return &cache{
		data: data,
	}
}

// cache stores a cached JSON schema.
type cache struct {
	data []byte
}

// Copy returns a copy of the cached schema.
func (s *cache) Copy() *jsonschema.Schema {
	schema := &jsonschema.Schema{}
	err := json.Unmarshal(s.data, schema)
	if err != nil {
		panic(fmt.Sprintf("Failed to unmarshal schema: %v", err))
	}

	return schema
}
