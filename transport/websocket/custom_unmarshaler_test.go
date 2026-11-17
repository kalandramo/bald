package websocket

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
)

// TestWithMessageUnmarshaler_ReachesInboundPath 复现自定义反序列化被静默绕过：
// WithMessageUnmarshaler 设置的 netPacketUnmarshaler 此前全仓零读取——入站
// defaultHandleSocketRawData 直呼 default 实现，用户的自定义包格式永远不生效
// （marshal 侧走字段、unmarshal 侧不走，两侧不对称）。修复后入站经字段转发，
// 自定义 unmarshaler 的产物必须原样到达 handler。
func TestWithMessageUnmarshaler_ReachesInboundPath(t *testing.T) {
	const customType = NetMessageType(42)

	srv := NewServer(":0", WithPayloadType(PayloadTypeBinary))
	srv.RegisterMessageHandler(customType, func(_ SessionID, p MessagePayload) error {
		got, ok := p.(string)
		if !ok || got != "via-custom-unmarshaler" {
			t.Fatalf("handler 收到 %v (%T)，want 自定义 unmarshaler 的产物", p, p)
		}
		return nil
	}, nil)

	srv.netPacketUnmarshaler = func(buf []byte) (*MessageHandlerData, MessagePayload, error) {
		// 自定义包格式：[4 字节 LE type]["via-custom-unmarshaler"]
		// 只识别自己的 magic type，其余交给默认实现。
		if len(buf) >= 4 && binary.LittleEndian.Uint32(buf[:4]) == uint32(customType) {
			h := srv.GetMessageHandler(customType)
			if h == nil {
				return nil, nil, errors.New("custom: no handler")
			}
			return h, string(buf[4:]), nil
		}
		return srv.defaultUnmarshalNetPacket(buf)
	}

	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint32(customType))
	buf.WriteString("via-custom-unmarshaler")

	if err := srv.defaultHandleSocketRawData("sess", buf.Bytes()); err != nil {
		t.Fatalf("入站经自定义 unmarshaler 不应报错: %v", err)
	}
}

// TestWithMessageUnmarshaler_FallsThroughToDefault 对照：未命中自定义格式时
// 回落默认实现仍工作（默认实现=同字段默认装配，行为不回归）。
func TestWithMessageUnmarshaler_FallsThroughToDefault(t *testing.T) {
	srv := NewServer(":0", WithPayloadType(PayloadTypeBinary))
	seen := false
	srv.RegisterMessageHandler(7, func(_ SessionID, p MessagePayload) error {
		seen = true
		return nil
	}, nil)

	srv.netPacketUnmarshaler = func(buf []byte) (*MessageHandlerData, MessagePayload, error) {
		if len(buf) >= 4 && binary.LittleEndian.Uint32(buf[:4]) == 9999 {
			return nil, nil, errors.New("custom: no handler")
		}
		return srv.defaultUnmarshalNetPacket(buf)
	}

	payload, _ := json.Marshal("hello")
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint32(7))
	buf.Write(payload)

	if err := srv.defaultHandleSocketRawData("sess", buf.Bytes()); err != nil {
		t.Fatalf("回落默认实现不应报错: %v", err)
	}
	if !seen {
		t.Fatal("默认实现的 handler 未被调用")
	}
}
