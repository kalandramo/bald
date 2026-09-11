package rocketmq

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/broker/rocketmq/option"
	"github.com/stretchr/testify/assert"
)

func TestKind(t *testing.T) {
	assert.Equal(t, "rocketmq", KindRocketMQ)
}

func TestNewServer(t *testing.T) {
	srv := NewServer(
		option.DriverTypeV2, WithNameServer([]string{"127.0.0.1:9876"}), WithCodec("json"),
	)
	assert.NotNil(t, srv)
	assert.Equal(t, "rocketmq", srv.Name())
	assert.False(t, srv.started.Load())
}

func TestEndpoint(t *testing.T) {
	srv := NewServer(
		option.DriverTypeV2, WithNameServer([]string{"127.0.0.1:9876"}), WithCodec("json"),
	)
	assert.Equal(t, "", srv.Endpoint())
}

func TestStopBeforeStart(t *testing.T) {
	srv := NewServer(
		option.DriverTypeV2, WithNameServer([]string{"127.0.0.1:9876"}), WithCodec("json"),
	)
	err := srv.Stop(context.Background())
	assert.Nil(t, err)
}
