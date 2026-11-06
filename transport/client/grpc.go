// Package client 提供契约无关的出站连接构造：gRPC 连接与 HTTP 客户端。
//
// 与 transport 包（入站服务器）对称：本包只做「按已定的目标与凭据建立连接」，
// 不认识 bconf 契约；把契约段映射到这里的构造放在本模块的 contract 子包
// （与 transport/gateway/contract 同分工）。
//
// 连接是惰性的：构造不拨号、不要求后端已在运行——首次调用时由 gRPC /
// http.Transport 按自身退避重连。构造期只拦「配置本身表达不了」的错误
// （空目标、discovery 目标却没有发现来源、非法 URL）。
package client

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/kalandramo/bald/pkg/grpcdiscovery"
	"github.com/kalandramo/bald/registry"
)

// GRPCOption 配置 NewGRPC 的行为。
type GRPCOption func(*grpcConfig)

type grpcConfig struct {
	dialOpts  []grpc.DialOption
	discovery registry.Discovery
	timeout   time.Duration
}

// WithDialOptions 追加原生拨号选项（TLS、认证、业务拦截器、负载均衡）。
//
// 追加在默认明文凭据之后：传入 WithTransportCredentials 即覆盖为 TLS。
func WithDialOptions(opts ...grpc.DialOption) GRPCOption {
	return func(c *grpcConfig) { c.dialOpts = append(c.dialOpts, opts...) }
}

// WithDiscovery 让 `discovery:///<服务名>` 目标经注册中心解析；其它目标不受
// 影响（仍走 gRPC 自带的 dns/passthrough resolver）。
func WithDiscovery(d registry.Discovery) GRPCOption {
	return func(c *grpcConfig) { c.discovery = d }
}

// WithCallTimeout 给未自带截止期的调用设置默认超时：单次 RPC 与整条流。
// 0 表示不设置——由调用方上下文决定何时放弃。
//
// 超时拦截器挂在链的最外层、先于 WithDialOptions 的拦截器，因此每个 attempt
// 都拿到完整的超时窗口（重试类拦截器不会把窗口在多轮间累加掉）。
func WithCallTimeout(d time.Duration) GRPCOption {
	return func(c *grpcConfig) { c.timeout = d }
}

// NewGRPC 建立 gRPC 出站连接。
//
// 非阻塞：grpc.NewClient 只解析目标、不在建连期拨号，后端未起不阻止启动。
// 目标写了 discovery scheme 却没给 WithDiscovery 时在此 fail-fast——那种目标
// 交给 gRPC 会因未注册的 scheme 静默拨不通，错误要在构造期出现。
//
// 凭据缺省明文（与契约「无 tls 段即明文」同义）：要 TLS 用
// WithDialOptions(grpc.WithTransportCredentials(credentials.NewTLS(cfg)))。
func NewGRPC(target string, opts ...GRPCOption) (*grpc.ClientConn, error) {
	if target == "" {
		return nil, fmt.Errorf("client: grpc target is empty")
	}
	var c grpcConfig
	for _, o := range opts {
		o(&c)
	}
	useDiscovery := grpcdiscovery.UsesTarget(target)
	if useDiscovery && c.discovery == nil {
		return nil, fmt.Errorf("client: target %q uses the %q scheme but no discovery is configured (client.WithDiscovery)",
			target, grpcdiscovery.Scheme)
	}

	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if c.timeout > 0 {
		dialOpts = append(dialOpts,
			grpc.WithChainUnaryInterceptor(unaryCallTimeout(c.timeout)),
			grpc.WithChainStreamInterceptor(streamCallTimeout(c.timeout)),
		)
	}
	dialOpts = append(dialOpts, c.dialOpts...)
	if useDiscovery {
		dialOpts = append(dialOpts, grpc.WithResolvers(grpcdiscovery.New(c.discovery)))
	}

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("client: grpc target %q: %w", target, err)
	}
	return conn, nil
}

// unaryCallTimeout 只在调用方没有截止期时补一个。
func unaryCallTimeout(d time.Duration) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if _, ok := ctx.Deadline(); ok {
			return invoker(ctx, method, req, reply, cc, opts...)
		}
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// streamCallTimeout 同上；流的超时覆盖整条流（截止期到期即中止）。
func streamCallTimeout(d time.Duration) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string,
		streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		if _, ok := ctx.Deadline(); ok {
			return streamer(ctx, desc, cc, method, opts...)
		}
		ctx, cancel := context.WithTimeout(ctx, d)
		cs, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			cancel() // 没有流可等，立刻释放定时器
			return nil, err
		}
		return &timedStream{ClientStream: cs, cancel: cancel}, nil
	}
}

// timedStream 在流结束时取消定时器。客户端只能通过 RecvMsg 得知流终止
// （io.EOF 或错误），那是唯一可靠的取消点。
type timedStream struct {
	grpc.ClientStream
	cancel context.CancelFunc
}

func (s *timedStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if err != nil {
		s.cancel()
	}
	return err
}
