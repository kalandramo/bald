package tcp

import (
	"context"
	"errors"
	"log"
	"net"
	"net/url"
	"time"

	"github.com/kalandramo/bald/encoding"
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
	conn net.Conn

	url      string
	endpoint *url.URL

	codec             encoding.Codec
	messageHandlers   ClientMessageHandlerMap
	rawMessageHandler ClientRawMessageHandler

	tracer trace.Tracer
	m      metrics.Metrics

	timeout time.Duration
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

	log.Printf("[tcp] connecting to %s", c.endpoint.String())

	conn, err := net.Dial("tcp", c.endpoint.String())
	if err != nil {
		log.Printf("[tcp] cant connect to server: %s", err)
		return err
	}

	c.conn = conn

	go c.run()

	return nil
}

func (c *Client) Disconnect() {
	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			log.Printf("[tcp] disconnect error: %s", err)
		}
		c.conn = nil
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
				log.Printf("[tcp] invalid payload struct type: %T", t)
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
	if c.conn == nil {
		return errors.New("client is not connected")
	}

	startTime := time.Now()
	labels := map[string]string{
		"rpc_system": "tcp",
	}

	if c.m != nil {
		c.m.Counter(context.Background(), "tcp_client_messages_sent_total", 1, labels)
	}

	if _, err := c.conn.Write(message); err != nil {
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

func (c *Client) run() {
	defer c.Disconnect()

	buf := make([]byte, 102400)

	for {
		readLen, err := c.conn.Read(buf)
		if err != nil {
			log.Printf("[tcp] read message error: %v", err)
			return
		}

		if c.rawMessageHandler != nil {
			if err := c.rawMessageHandler(buf[:readLen]); err != nil {
				log.Printf("[tcp] raw data handler exception: %s", err)
				continue
			}
			continue
		}

		if err = c.messageHandler(buf[:readLen]); err != nil {
			log.Printf("[tcp] process message error: %v", err)
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
	if err := msg.Unmarshal(buf); err != nil {
		log.Printf("[tcp] decode message exception: %s", err)
		return err
	}

	handlerData, ok := c.messageHandlers[msg.Type]
	if !ok {
		log.Printf("[tcp] message type not found: %d", msg.Type)
		return errors.New("message handler not found")
	}

	var payload NetMessagePayload

	if handlerData.Creator != nil {
		payload = handlerData.Creator()

		if c.codec == nil {
			return errors.New("codec is nil (nothing registered); register one via encoding.MustRegister, e.g. encoding.MustRegister(json.New())")
		}
		if err := c.codec.Unmarshal(msg.Payload, payload); err != nil {
			log.Printf("[tcp] unmarshal message exception: %s", err)
			return err
		}
	} else {
		payload = msg.Payload
	}

	if err := handlerData.Handler(payload); err != nil {
		log.Printf("[tcp] message handler exception: %s", err)
		return err
	}

	return nil
}
