package websocket

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

// benchFrame 构造 [4 字节 LE type][json payload] 二进制包（与 binaryFrame 同形，
// 接受 *testing.B）。
func benchFrame(b *testing.B, typ NetMessageType, payload any) []byte {
	b.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		b.Fatalf("marshal payload: %v", err)
	}
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint32(typ))
	buf.Write(data)
	return buf.Bytes()
}

// BenchmarkHandleSocketRawDataBinary 度量每消息处理路径：此前一次入站消息要
// 做 2 次整包 unmarshal（defaultUnmarshalNetPacket + extractMessageType）和
// 2 次 handler 查找——第二次只为了重新推导第一次已经拿到手的类型。
// 口径说明：nil binder 的 raw payload 路径（无 codec）；带 Creator+codec 的
// 消息另有反序列化成本，本数字不代表其全量每消息开销。
func BenchmarkHandleSocketRawDataBinary(b *testing.B) {
	srv := NewServer(":0", WithPayloadType(PayloadTypeBinary))
	srv.RegisterMessageHandler(11, func(_ SessionID, _ MessagePayload) error { return nil }, nil)

	buf := benchFrame(b, 11, map[string]string{
		"text":  strings.Repeat("x", 256),
		"index": "0",
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := srv.defaultHandleSocketRawData("bench", buf); err != nil {
			b.Fatal(err)
		}
	}
}
