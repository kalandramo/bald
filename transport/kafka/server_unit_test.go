package kafka

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKind(t *testing.T) {
	assert.Equal(t, "kafka", KindKafka)
}

func TestNewServer(t *testing.T) {
	srv := NewServer(
		WithAddress([]string{"127.0.0.1:9092"}), WithCodec("json"),
	)
	assert.NotNil(t, srv)
	assert.Equal(t, "kafka", srv.Name())
	assert.False(t, srv.started.Load())
}

func TestEndpoint(t *testing.T) {
	srv := NewServer(
		WithAddress([]string{"127.0.0.1:9092"}), WithCodec("json"),
	)
	assert.Equal(t, "", srv.Endpoint())
}

// TestEndpointDescriptiveAfterStart 起动态就绪闸门的两端：Start 成功后端点变成
// 描述性的 kafka://addr（appkit 的 waitForEndpoints 只认非空非 :0），Stop 复位
// started 后回到空。kafka 的 Connect 只翻标志位，无集群也能 Start 成功。
func TestEndpointDescriptiveAfterStart(t *testing.T) {
	srv := NewServer(WithAddress([]string{"127.0.0.1:19092"}))
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	assert.Equal(t, "kafka://127.0.0.1:19092", srv.Endpoint())

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assert.Equal(t, "", srv.Endpoint(), "started 复位后端点必须回到空（就绪闸门看活的状态，不是历史）")
}

func TestStopBeforeStart(t *testing.T) {
	srv := NewServer(
		WithAddress([]string{"127.0.0.1:9092"}), WithCodec("json"),
	)
	err := srv.Stop(context.Background())
	assert.Nil(t, err)
}
