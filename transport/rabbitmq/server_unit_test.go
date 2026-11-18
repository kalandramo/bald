package rabbitmq

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/broker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKind(t *testing.T) {
	assert.Equal(t, "rabbitmq", KindRabbitMQ)
}

func TestNewServer(t *testing.T) {
	srv := NewServer(
		WithAddress([]string{"127.0.0.1:5672"}), WithCodec("json"),
	)
	assert.NotNil(t, srv)
	assert.Equal(t, "rabbitmq", srv.Name())
	assert.Empty(t, srv.Endpoint(), "started 未置真端点为空")
}

func TestEndpoint(t *testing.T) {
	srv := NewServer(
		WithAddress([]string{"127.0.0.1:5672"}), WithCodec("json"),
	)
	assert.Equal(t, "", srv.Endpoint())
}

func TestStopBeforeStart(t *testing.T) {
	srv := NewServer(
		WithAddress([]string{"127.0.0.1:5672"}), WithCodec("json"),
	)
	err := srv.Stop(context.Background())
	assert.Nil(t, err)
}

// credentialedStubBroker 端点脱敏见证用：Address 返回带凭证的 AMQP URL，
// Init/Connect 零错误让 Start 走真实路径置 started（离线无需真握手）。
type credentialedStubBroker struct {
	broker.Broker
}

func (b *credentialedStubBroker) Init(...broker.Option) error { return nil }
func (b *credentialedStubBroker) Connect() error              { return nil }
func (b *credentialedStubBroker) Disconnect() error           { return nil }
func (b *credentialedStubBroker) Address() string             { return "amqp://user:hunter2@127.0.0.1:15672" }

// TestEndpointAfterStartStripsCredentials 端点是 appkit 就绪闸门读的面：Start 之
// 后必须变描述性，且 AMQP URL 里的 guest:guest（或自建凭证）不能跟着泄进日志/
// 诊断面。started 经真实 Start 路径置真（凭证桩的 Connect 零错误，离线绿）；
// 「真 AMQP 握手之后」那一半归真集群集成（假后端不可信，见
// contract/mapping_offline_test.go 文件头）。
func TestEndpointAfterStartStripsCredentials(t *testing.T) {
	srv := NewServer(WithAddress([]string{"amqp://user:hunter2@127.0.0.1:15672"}))
	assert.Equal(t, "", srv.Endpoint(), "started 落真前端点必须为空（假就绪）")

	srv.Broker = &credentialedStubBroker{}
	require.NoError(t, srv.Start(context.Background()))
	defer func() { _ = srv.Stop(context.Background()) }()

	assert.Equal(t, "amqp://127.0.0.1:15672", srv.Endpoint())
	assert.NotContains(t, srv.Endpoint(), "hunter2")
	assert.NotContains(t, srv.Endpoint(), "user:")
}

// TestSanitizeEndpoint 纯函数表：带凭证剥掉、不带凭证与解析不了的原样返回。
// 裸 host:port 那行是「解析失败 ⇒ 不谎报」的正证——url.Parse 对它报错
// （first path segment in URL cannot contain colon），兜底必须放行而不是返回空串，
// 否则就绪闸门会把已在服务的地址读成未就绪。
func TestSanitizeEndpoint(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"amqp://guest:guest@127.0.0.1:5672", "amqp://127.0.0.1:5672"},
		{"amqps://u:p@rabbit.internal:5671/vhost", "amqps://rabbit.internal:5671/vhost"},
		{"amqp://127.0.0.1:5672", "amqp://127.0.0.1:5672"},
		{"127.0.0.1:5672", "127.0.0.1:5672"},
		{"", ""},
	} {
		assert.Equal(t, tc.want, sanitizeEndpoint(tc.in), "input %q", tc.in)
	}
}
