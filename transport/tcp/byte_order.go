package tcp

import "encoding/binary"

// defaultByteOrder 是未显式指定字节序时的进程级默认值（历史行为：小端）。
//
// 它只作为「没有按实例指定」时的回退：Server/Client 与 NetPacket 都有按实例的
// 字节序入口（WithByteOrder / WithClientByteOrder / NetPacket.Order），同进程
// 多台实例可以各用各的。
var defaultByteOrder binary.ByteOrder = binary.LittleEndian

// WithLittleEndian 设置**进程级**默认字节序为小端。
//
// Deprecated: 它改的是包级全局，同进程内所有未显式指定字节序的实例都会跟着变。
// 按实例配置请用 WithByteOrder（服务端）/ WithClientByteOrder（客户端），或
// 契约字段 server.tcp.byte_order。
func WithLittleEndian() {
	defaultByteOrder = binary.LittleEndian
}

// WithBigEndian 设置**进程级**默认字节序为大端。
//
// Deprecated: 同 WithLittleEndian——全局写入、不分实例。按实例配置请用
// WithByteOrder / WithClientByteOrder。
func WithBigEndian() {
	defaultByteOrder = binary.BigEndian
}

// WithByteOrder 设置本 server 实例的报文字段字节序（nil = 沿用进程级默认）。
//
// 只影响类型头（uint32）的编解码；payload 的序列化由 codec 决定，与本选项无关。
func WithByteOrder(order binary.ByteOrder) Option {
	return func(s *Server) {
		if order != nil {
			s.byteOrder = order
		}
	}
}

// WithClientByteOrder 设置本 client 实例的报文字段字节序（nil = 沿用进程级默认）。
//
// 两端必须一致：server 用大端时 client 也要配大端，否则类型头会被读成另一个数。
func WithClientByteOrder(order binary.ByteOrder) ClientOption {
	return func(c *Client) {
		if order != nil {
			c.byteOrder = order
		}
	}
}
