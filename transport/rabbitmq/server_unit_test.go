package rabbitmq

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
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
	assert.False(t, srv.started.Load())
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

// TestEndpointAfterStartStripsCredentials 端点是 appkit 就绪闸门读的面：Start 之
// 后必须变描述性，且 AMQP URL 里的 guest:guest（或自建凭证）不能跟着泄进日志/
// 诊断面。这里直接翻 started 标志——离线的 Connect 是完整 AMQP 握手、真 Start
// 必红（假后端不可信，见 contract/mapping_offline_test.go 文件头），所以闸门与
// 脱敏只能在包内注入状态判；「真 Start 之后」那一半归真集群集成。
func TestEndpointAfterStartStripsCredentials(t *testing.T) {
	srv := NewServer(WithAddress([]string{"amqp://user:hunter2@127.0.0.1:15672"}))
	assert.Equal(t, "", srv.Endpoint(), "started 落真前端点必须为空（假就绪）")

	srv.started.Store(true)
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
