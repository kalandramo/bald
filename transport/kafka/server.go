package kafka

import (
	"context"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/kafka"

	"github.com/kalandramo/bald/transport/internal/brokertype"
)

// Server 是 kafka 家族的传输服务器：生命周期、订阅簿（deferred + live 双态）、
// metrics 包装与家族日志全部住在 transport/internal/brokertype 共享 kernel
// （J2 单一闸门——此前两份 ~84% 复制的 server.go 使 Stop 无锁遍历等家族同构
// 缺陷必然双发）。本壳只保留家族参数化点（Family：Kind/topic 名词/NewBroker
// 工厂/kafka:// 端点）与公共 API 形状：RegisterSubscriber 的
// queue/disableAutoAck 参数面与泛型登记糖。
type Server struct {
	*brokertype.Server
}

// Family 是 kafka 家族的参数化点（kernel 装配用）。
// 端点是描述性的 kafka://addr——代理型传输不监听本地端口，带 scheme 的描述性
// 端点会被 registrableEndpoint 挡在服务注册之外，不影响发现。
func Family() brokertype.Family {
	return brokertype.Family{
		Kind:      KindKafka,
		KeyNoun:   "topic",
		NewBroker: kafka.NewBroker,
		Endpoint:  func(addr string) string { return "kafka://" + addr },
	}
}

func NewServer(opts ...ServerOption) *Server {
	s := &Server{Server: brokertype.NewServer(Family())}
	for _, o := range opts {
		o(s)
	}
	s.BuildBroker()

	return s
}

// RegisterSubscriber 注册一个订阅者
// @param ctx 上下文
// @param topic 订阅的主题
// @param queue 订阅的分组
// @param handler 订阅者的处理函数
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
