package broker

import (
	"bytes"
	"encoding/gob"
	"errors"
	"reflect"

	"github.com/kalandramo/bald/encoding"
	_ "github.com/kalandramo/bald/encoding/json"
	_ "github.com/kalandramo/bald/encoding/proto"
)

// derefAnyPointer 当 outValue 是指向 any 的指针、且 any 中装着指针值时，
// 返回 any 中该指针，供反序列化保住类型；否则返回 nil。
func derefAnyPointer(outValue any) any {
	rv := reflect.ValueOf(outValue)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Interface || elem.IsNil() {
		return nil
	}
	inner := elem.Elem()
	if inner.Kind() == reflect.Pointer && !inner.IsNil() {
		return inner.Interface()
	}
	return nil
}

// Marshal encodes a message into bytes using the provided codec.
func Marshal(codec encoding.Codec, msg any) ([]byte, error) {
	if msg == nil {
		return nil, errors.New("message is nil")
	}

	if codec != nil {
		return codec.Marshal(msg)
	}

	switch t := msg.(type) {
	case []byte:
		return t, nil
	case string:
		return []byte(t), nil
	default:
		var buf bytes.Buffer
		enc := gob.NewEncoder(&buf)
		if err := enc.Encode(msg); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
}

// Unmarshal decodes bytes into a message using the provided codec.
//
// outValue 典型形态是 binder 场景下的 *any（any 中已装入具体类型指针）。
// 标准反序列化器对 *any 会用 map/slice 顶掉原类型，因此这里解引用一层，
// 把 any 中已有的指针交给 codec，保住 binder 的类型化语义。
func Unmarshal(codec encoding.Codec, inputData []byte, outValue any) error {
	if inputData == nil {
		return errors.New("inputData is nil")
	}
	if outValue == nil {
		return errors.New("outValue is nil; must be a pointer to the target value")
	}

	if ptr := derefAnyPointer(outValue); ptr != nil {
		outValue = ptr
	}

	if codec != nil {
		return codec.Unmarshal(inputData, outValue)
	}

	// No codec: support common target pointer types, otherwise use gob decode.
	switch v := outValue.(type) {
	case *[]byte:
		*v = make([]byte, len(inputData))
		copy(*v, inputData)
		return nil
	case *string:
		*v = string(inputData)
		return nil
	default:
		buf := bytes.NewBuffer(inputData)
		dec := gob.NewDecoder(buf)
		if err := dec.Decode(outValue); err != nil {
			return err
		}
		return nil
	}
}
