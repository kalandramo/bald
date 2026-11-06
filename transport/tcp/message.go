package tcp

import (
	"bytes"
	"encoding/binary"
)

type NetMessageType uint32
type NetMessagePayload any

type NetPacket struct {
	Type    NetMessageType
	Payload []byte
	// Order 是类型头的字节序；nil 沿用进程级默认（小端，可经 WithLittleEndian /
	// WithBigEndian 改）。Server/Client 按实例配置后会写在这里，直接构造
	// NetPacket 的调用方也可自行指定。
	Order binary.ByteOrder
}

// order 返回本包实际使用的字节序：显式值优先，否则进程级默认。
func (m *NetPacket) order() binary.ByteOrder {
	if m.Order != nil {
		return m.Order
	}
	return defaultByteOrder
}

func (m *NetPacket) Marshal() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, m.order(), uint32(m.Type)); err != nil {
		return nil, err
	}
	buf.Write(m.Payload)
	return buf.Bytes(), nil
}

func (m *NetPacket) Unmarshal(buf []byte) error {
	network := new(bytes.Buffer)
	network.Write(buf)

	if err := binary.Read(network, m.order(), &m.Type); err != nil {
		return err
	}

	m.Payload = network.Bytes()

	return nil
}
