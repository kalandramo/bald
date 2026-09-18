// Package bson provides a [encoding.Codec] implementation using MongoDB BSON
// via go.mongodb.org/mongo-driver/bson.
//
// BSON (Binary JSON) is a binary representation of JSON-like documents,
// extended with additional types such as Date, Binary, ObjectId, Decimal128.
// It is the native storage format for MongoDB.
//
// Registration is explicit: encoding.MustRegister(bson.New()).
package bson

import (
	"go.mongodb.org/mongo-driver/bson"

	"github.com/kalandramo/bald/encoding"
)

// Name is the name registered for the BSON codec.
const Name = "bson"

// New returns a fresh bson codec for explicit registration
// via encoding.MustRegister.
func New() encoding.Codec { return codec{} }

// Compile-time guarantee that codec satisfies the encoding.Codec contract.
var _ encoding.Codec = codec{}

// codec implements encoding.Codec using mongo-driver/bson.
type codec struct{}

// Marshal encodes v into BSON binary bytes.
func (codec) Marshal(v any) ([]byte, error) {
	return bson.Marshal(v)
}

// Unmarshal decodes BSON binary data into v.
func (codec) Unmarshal(data []byte, v any) error {
	return bson.Unmarshal(data, v)
}

// Name returns the codec name.
func (codec) Name() string {
	return Name
}
