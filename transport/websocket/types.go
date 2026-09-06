package websocket

import "net/url"

// SocketConnectHandler 是连接建立/断开时的回调。
// connect 为 true 表示连接建立，false 表示断开。
type SocketConnectHandler func(sessionId SessionID, queries url.Values, connect bool)

// SocketRawDataHandler 处理从 WebSocket 接收到的原始数据。
type SocketRawDataHandler func(sessionId SessionID, buf []byte) error

// NetPacketMarshaler 将消息类型和载荷序列化为字节流。
type NetPacketMarshaler func(messageType NetMessageType, message MessagePayload) ([]byte, error)

// NetPacketUnmarshaler 将字节流反序列化为处理器和载荷。
type NetPacketUnmarshaler func(buf []byte) (*MessageHandlerData, MessagePayload, error)

// NetMessageHandler 处理特定类型的 WebSocket 消息。
type NetMessageHandler func(SessionID, MessagePayload) error

// Creator 创建消息载荷的空实例，用于反序列化。
type Creator func() any

// MessageHandlerData 包装了一个消息处理器及其载荷创建器。
type MessageHandlerData struct {
	Handler NetMessageHandler
	Creator Creator
}

// Create 调用 Creator 创建载荷实例。如果 Creator 为 nil 则返回 nil。
func (h *MessageHandlerData) Create() any {
	if h.Creator != nil {
		return h.Creator()
	}
	return nil
}

// NetMessageHandlerMap 将消息类型映射到其处理器数据。
type NetMessageHandlerMap map[NetMessageType]*MessageHandlerData
