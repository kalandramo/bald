package rabbitmq

import (
	"context"
	"net/url"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/rabbitmq"

	"github.com/kalandramo/bald/transport/internal/brokertype"
)

// Server 是 rabbitmq 家族的传输服务器：生命周期、订阅簿（deferred + live
// 双态）、metrics 包装与家族日志全部住在 transport/internal/brokertype 共享
// kernel（J2 单一闸门——此前两份 ~84% 复制的 server.go 使 Stop 无锁遍历等
// 家族同构缺陷必然双发）。本壳只保留家族参数化点（Family：Kind/routingKey
// 名词/NewBroker 工厂/剥凭证端点）与公共 API 形状。
type Server struct {
	*brokertype.Server
}

// Family 是 rabbitmq 家族的参数化点（kernel 装配用）。
// 端点是描述性的 amqp://host:port——代理型传输不监听本地端口；AMQP URL 常内嵌
// 用户名/密码（DefaultRabbitURL 即 guest:guest），端点会进日志与服务注册面，
// 凭证必须剥掉。
// （先例：transport/asynq 的 asynq:// 端点、pkg/registrableEndpoint 过滤 scheme 端点。）
func Family() brokertype.Family {
	return brokertype.Family{
		Kind:      KindRabbitMQ,
		KeyNoun:   "routingKey",
		NewBroker: rabbitmq.NewBroker,
		Endpoint:  sanitizeEndpoint,
	}
}

// sanitizeEndpoint 剥掉 URL 的用户信息段；解析不了或本就无凭证时原样返回。
func sanitizeEndpoint(addr string) string {
	u, err := url.Parse(addr)
	if err != nil || u.User == nil {
		return addr
	}
	u.User = nil
	return u.String()
}

func NewServer(opts ...ServerOption) *Server {
	s := &Server{Server: brokertype.NewServer(Family())}
	for _, o := range opts {
		o(s)
	}
	s.BuildBroker()

	return s
}

// RegisterSubscriber 注册一个订阅者（routingKey 维度）。
func (s *Server) RegisterSubscriber(ctx context.Context, topic, queue string, disableAutoAck bool, handler broker.Handler, binder broker.Binder, opts ...broker.SubscribeOption) error {
	return brokertype.RegisterUntypedSubscriber(s.Server, ctx, topic, queue, disableAutoAck, handler, binder, opts...)
}

// RegisterSubscriber 泛型糖：类型断言包装已上收 brokertype（片 3），家族壳保留一行委托。
func RegisterSubscriber[T any](
	srv *Server,
	ctx context.Context,
	topic, queue string,
	disableAutoAck bool,
	handler broker.TypedHandler[T],
	opts ...broker.SubscribeOption,
) error {
	return brokertype.RegisterTypedSubscriber(srv.Server, ctx, topic, queue, disableAutoAck, handler, opts...)
}
