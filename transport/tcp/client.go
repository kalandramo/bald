package tcp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/kalandramo/bald/encoding"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/metrics"

	"go.opentelemetry.io/otel/trace"
)

type ClientMessageHandler func(NetMessagePayload) error

type ClientRawMessageHandler func([]byte) error

type ClientHandlerData struct {
	Handler ClientMessageHandler
	Creator Creator
}
type ClientMessageHandlerMap map[NetMessageType]ClientHandlerData

type Client struct {
	// connMu 保护 conn：run goroutine 与 Connect/Disconnect/SendRawData 跨 goroutine 访问。
	connMu sync.Mutex
	conn   net.Conn

	url      string
	endpoint *url.URL

	codec             encoding.Codec
	messageHandlers   ClientMessageHandlerMap
	rawMessageHandler ClientRawMessageHandler

	tracer trace.Tracer
	m      metrics.Metrics

	timeout time.Duration

	// byteOrder 是本实例报文字段（uint32 类型头）的字节序；nil 沿用进程级默认。
	// 经 WithClientByteOrder 配置，须与对端 server 一致。
	byteOrder binary.ByteOrder
}

func NewClient(opts ...ClientOption) *Client {
	cli := &Client{
		url:             "",
		timeout:         1 * time.Second,
		codec:           encoding.GetCodec("json"),
		messageHandlers: make(ClientMessageHandlerMap),
	}

	cli.init(opts...)

	return cli
}

func (c *Client) init(opts ...ClientOption) {
	for _, o := range opts {
		o(c)
	}

	addr := c.url

	c.endpoint, _ = url.Parse(addr)
}

func (c *Client) Connect() error {
	if c.endpoint == nil {
		return errors.New("endpoint is nil")
	}

	log.Info(context.Background(), "tcp connecting", "endpoint", c.endpoint.String())

	conn, err := net.Dial("tcp", c.endpoint.String())
	if err != nil {
		log.Error(context.Background(), "tcp connect to server failed", "error", err)
		return err
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	go c.run(conn)

	return nil
}

// release 在 conn 仍是当前连接时清空字段并返回 true；谁持有谁关闭，避免重复 Close。
func (c *Client) release(conn net.Conn) bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn != conn {
		return false
	}
	c.conn = nil
	return true
}

func (c *Client) Disconnect() {
	c.connMu.Lock()
	conn := c.conn
	c.conn = nil
	c.connMu.Unlock()

	if conn == nil {
		return
	}
	if err := conn.Close(); err != nil {
		log.Error(context.Background(), "tcp disconnect failed", "error", err)
	}
}

func (c *Client) RegisterMessageHandler(messageType NetMessageType, handler ClientMessageHandler, binder Creator) {
	if _, ok := c.messageHandlers[messageType]; ok {
		return
	}

	c.messageHandlers[messageType] = ClientHandlerData{handler, binder}
}

func RegisterClientMessageHandler[T any](cli *Client, messageType NetMessageType, handler func(*T) error) {
	cli.RegisterMessageHandler(messageType,
		func(payload NetMessagePayload) error {
			switch t := payload.(type) {
			case *T:
				return handler(t)
			default:
				log.Error(context.Background(), "tcp invalid payload struct type", "type", fmt.Sprintf("%T", t))
				return errors.New("invalid payload struct type")
			}
		},
		func() any {
			var t T
			return &t
		},
	)
}

func (c *Client) DeregisterMessageHandler(messageType NetMessageType) {
	delete(c.messageHandlers, messageType)
}

func (c *Client) SendRawData(message []byte) error {
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()

	if conn == nil {
		return errors.New("client is not connected")
	}

	startTime := time.Now()
	labels := map[string]string{
		"rpc_system": "tcp",
	}

	if c.m != nil {
		c.m.Counter(context.Background(), "tcp_client_messages_sent_total", 1, labels)
	}

	if _, err := conn.Write(message); err != nil {
		if c.m != nil {
			c.m.Counter(context.Background(), "tcp_client_messages_errors_total", 1, map[string]string{
				"rpc_system": "tcp",
				"error":      "true",
			})
		}
		return err
	}

	if c.m != nil {
		c.m.Histogram(context.Background(), "tcp_client_message_send_duration_seconds", time.Since(startTime).Seconds(), labels)
	}

	return nil
}

func (c *Client) SendMessage(messageType int, message any) error {
	var msg NetPacket
	msg.Type = NetMessageType(messageType)
	msg.Order = c.byteOrder
	var err error
	if c.codec == nil {
		return errors.New("codec is nil (nothing registered); register one via encoding.MustRegister, e.g. encoding.MustRegister(json.New())")
	}
	msg.Payload, err = c.codec.Marshal(message)
	if err != nil {
		return err
	}

	var buff []byte
	if buff, err = msg.Marshal(); err != nil {
		return err
	}

	return c.SendRawData(buff)
}

func (c *Client) run(conn net.Conn) {
	defer func() {
		if c.release(conn) {
			if err := conn.Close(); err != nil {
				log.Error(context.Background(), "tcp disconnect failed", "error", err)
			}
		}
	}()

	buf := make([]byte, 102400)

	for {
		readLen, err := conn.Read(buf)
		if err != nil {
			log.Error(context.Background(), "tcp read message failed", "error", err)
			return
		}

		if c.rawMessageHandler != nil {
			if err := c.rawMessageHandler(buf[:readLen]); err != nil {
				log.Error(context.Background(), "tcp raw data handler exception", "error", err)
				continue
			}
			continue
		}

		if err = c.messageHandler(buf[:readLen]); err != nil {
			log.Error(context.Background(), "tcp process message failed", "error", err)
		}

		if c.m != nil {
			c.m.Counter(context.Background(), "tcp_client_messages_received_total", 1, map[string]string{
				"rpc_system": "tcp",
			})
		}
	}
}

func (c *Client) messageHandler(buf []byte) error {
	var msg NetPacket
	msg.Order = c.byteOrder
	if err := msg.Unmarshal(buf); err != nil {
		log.Error(context.Background(), "tcp decode message exception", "error", err)
		return err
	}

	handlerData, ok := c.messageHandlers[msg.Type]
	if !ok {
		log.Warn(context.Background(), "tcp message type not found", "type", msg.Type)
		return errors.New("message handler not found")
	}

	var payload NetMessagePayload

	if handlerData.Creator != nil {
		payload = handlerData.Creator()

		if c.codec == nil {
			return errors.New("codec is nil (nothing registered); register one via encoding.MustRegister, e.g. encoding.MustRegister(json.New())")
		}
		if err := c.codec.Unmarshal(msg.Payload, payload); err != nil {
			log.Error(context.Background(), "tcp unmarshal message exception", "error", err)
			return err
		}
	} else {
		payload = msg.Payload
	}

	if err := handlerData.Handler(payload); err != nil {
		log.Error(context.Background(), "tcp message handler exception", "error", err)
		return err
	}

	return nil
}
