package broker

import (
	"errors"
	"reflect"

	"github.com/kalandramo/bald/encoding"
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

// errNoCodec 在既无 codec、消息体又不是可直通的 []byte/string 时返回。
//
// 刻意 fail-fast 而非静默兜底：encoding 注册表是显式注册的（见
// [encoding.MustRegister]），未注册时 GetCodec 返回 nil。旧实现此时静默走
// gob 编码，于是「配了 JSON 心智、实际发 gob 字节」的错配会一路潜行到
// 跨语言消费端才炸。与 transport/tcp、transport/sse、transport/websocket、
// transport/asynq 的同类错误保持一致。
var errNoCodec = errors.New("codec is nil (nothing registered); register one via encoding.MustRegister, e.g. encoding.MustRegister(json.New())")

// Marshal encodes a message into bytes using the provided codec.
//
// When codec is nil, []byte and string pass through unchanged (透传场景) and
// any other type fails fast with a registration hint — see [errNoCodec].
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
		return nil, errNoCodec
	}
}

// Unmarshal decodes bytes into a message using the provided codec.
//
// outValue 典型形态是 binder 场景下的 *any（any 中已装入具体类型指针）。
// 标准反序列化器对 *any 会用 map/slice 顶掉原类型，因此这里解引用一层，
// 把 any 中已有的指针交给 codec，保住 binder 的类型化语义。
//
// When codec is nil, *[]byte and *string are supported (透传场景); any other
// target fails fast with a registration hint — see [errNoCodec].
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

	// No codec: only the passthrough target types are supported.
	switch v := outValue.(type) {
	case *[]byte:
		*v = make([]byte, len(inputData))
		copy(*v, inputData)
		return nil
	case *string:
		*v = string(inputData)
		return nil
	default:
		return errNoCodec
	}
}
