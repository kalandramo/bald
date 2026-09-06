package websocket

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
)

// NetMessageType 标识 WebSocket 消息的业务类型。
type NetMessageType uint32

// MessagePayload 表示消息载荷的任意类型。
type MessagePayload any

// BinaryNetPacket 是二进制模式下的消息包格式。
// 布局：[4 字节 Little-Endian uint32 类型][剩余字节为 payload]
type BinaryNetPacket struct {
	Type    NetMessageType
	Payload []byte
}

// Marshal 将 BinaryNetPacket 序列化为二进制格式。
func (m *BinaryNetPacket) Marshal() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, uint32(m.Type)); err != nil {
		return nil, err
	}
	buf.Write(m.Payload)
	return buf.Bytes(), nil
}

// Unmarshal 从二进制数据反序列化 BinaryNetPacket。
func (m *BinaryNetPacket) Unmarshal(buf []byte) error {
	network := new(bytes.Buffer)
	network.Write(buf)

	if err := binary.Read(network, binary.LittleEndian, &m.Type); err != nil {
		return err
	}

	m.Payload = network.Bytes()

	return nil
}

// TextNetPacket 是文本模式下的消息包格式（JSON）。
type TextNetPacket struct {
	Type    NetMessageType `json:"type" xml:"type"`
	Payload string         `json:"payload" xml:"payload"`
}

// Marshal 将 TextNetPacket 序列化为 JSON。
func (m *TextNetPacket) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

// Unmarshal 从 JSON 数据反序列化 TextNetPacket。
func (m *TextNetPacket) Unmarshal(buf []byte) error {
	return json.Unmarshal(buf, m)
}
