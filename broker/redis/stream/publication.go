package stream

import (
	"github.com/gomodule/redigo/redis"
	"github.com/kalandramo/bald/broker"
)

type publication struct {
	topic   string
	group   string
	msgID   string
	message *broker.Message
	err     error

	pool  *redis.Pool
	acked bool
}

func (p *publication) Topic() string {
	return p.topic
}

func (p *publication) Message() *broker.Message {
	return p.message
}

func (p *publication) RawMessage() any {
	return p.message
}

// Ack 使用 XACK 确认消息已处理
func (p *publication) Ack() error {
	if p.acked {
		return nil
	}

	if p.pool == nil {
		return nil
	}

	conn := p.pool.Get()
	defer conn.Close()

	_, err := conn.Do("XACK", p.topic, p.group, p.msgID)
	if err != nil {
		p.err = err
		return err
	}

	p.acked = true
	return nil
}

func (p *publication) Error() error {
	return p.err
}
