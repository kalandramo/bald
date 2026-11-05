// Package contract 提供 RocketMQ 传输层的契约装配：bootstrapv1.Server 的 rocketmq
// 段 → rocketmq.NewServer + 构造 func（供 server.rocketmq 段 Provider 的 Build 复用）。
//
// 单独成包的原因（与 transport/{tcp,graphql,kafka,redis}/contract 同惯例）：
// transport/rocketmq 保持零契约依赖（核心不 import bconf），只有本包 import
// bconf——业务按需 import，依赖图全程可见。
//
// 契约段三个字段全部有落点（name_server_addrs / codec / driver_type）；凭证、
// namespace、实例名、消费组、重试次数等代码侧能力经 WithServerOptions 注入，
// 消费者注册（RegisterSubscriber）经 WithAfterBuild 注入。
//
// 与 kafka 工厂同形（deferred 订阅 map、Connect 只翻标志位、Endpoint 启动后
// 描述性），两处后端差异决定判据形状：
//   - v2 broker 的地址面（nameServers）在 Init 才从 options.Context 读出——
//     构造期 Address() 恒落 option.DefaultAddr 兜底，「契约地址活在服务端面」
//     只能判 Start 之后（Init 是纯读表，离线必成）。
//   - Start 的订阅 flush 走 SDK 的 live Subscribe，离线会在路由拉取处失败——
//     「带订阅者的离线 Start」不是可判面（假 NameServer 不可导入，broker 侧
//     fakewire 是包私有的），归真集群集成判据（§7.24 同口径）。
//
// driver_type 的契约注释「默认 v5」与代码不符：transport/rocketmq 的 init 恒
// 构造 v2 broker（server.go:55，位置参数 driverType 被存而不用），v5/aliyun
// 驱动从未接线。本工厂按代码实况收口：""/"v2" 放行，其余 fail-fast 并说明
// 原因——把「配了 v5 实际跑 v2」的静默错位挡在装配期（漂移已登记风险评审）。
package contract

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/encoding"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/broker/rocketmq/option"
	"github.com/kalandramo/bald/transport"
	rocketmq "github.com/kalandramo/bald/transport/rocketmq"
)

// Type 是契约 server 段中 rocketmq 后端的段名。
const Type = "rocketmq"

// Option 配置本 Provider 的行为（非 RocketMQ 服务器的 Option——那是
// rocketmq.ServerOption）。
type Option func(*providerConfig)

type providerConfig struct {
	// serverOpts 是「server 构造前」应用的 rocketmq.ServerOption（如凭证、
	// namespace、消费组、TLS、metrics 等）。
	serverOpts []rocketmq.ServerOption
	// afterBuild 是「server 构造后」执行的回调（如业务侧接线：注册消息处理器）。
	// 返回 error 则装配失败并短路。
	afterBuild []func(context.Context, *rocketmq.Server) error
}

// WithServerOptions 追加 rocketmq.ServerOption——覆盖契约段表达不了的能力。
//
// transport/rocketmq/options.go 的全部 ServerOption 均可经此传入（凭证、
// NameServerDomain、namespace、消费组、重试次数、TLS、metrics 等）。追加项在
// 契约字段**之后**应用，故可覆盖契约值——NameServer 走 options.Context 键，
// 末者胜，业务 WithNameServer 会顶掉契约地址。
func WithServerOptions(opts ...rocketmq.ServerOption) Option {
	return func(c *providerConfig) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithAfterBuild 注册「server 构造后」的回调，用于业务侧接线。
//
// **时序**：回调在 rocketmq.NewServer 之后、返回 server 之前执行——早于 appkit
// 的 Start。Start 前 RegisterSubscriber 只进 deferred map；Start 时的 flush 走
// SDK live Subscribe，需要可达的 NameServer（离线会在路由拉取失败并让 Start
// 变红）——「登记动作本身」在这里可判，「flush 上线」属真集群判据。
func WithAfterBuild(fn func(context.Context, *rocketmq.Server) error) Option {
	return func(c *providerConfig) { c.afterBuild = append(c.afterBuild, fn) }
}

// Provider 返回契约驱动的 RocketMQ 工厂函数。
//
// 返回**未命名函数类型**——调用方在 server.rocketmq 段 Provider 的 Build 中
// 直接调用，无需显式类型转换，本包也不必 import bootstrap。
//
// 仅当契约 server.rocketmq 段存在时构造（段缺失返回 nil server，引擎跳过）。
//
// **name_server_addrs 必填**：段存在而地址为空时 fail-fast——v2 broker 的
// Address() 会兜底到内置默认 127.0.0.1:9876（option.DefaultAddr），一条「配置
// 写了、落点不由配置决定」的静默失效路径，所以契约侧不接受空值；含空串元素
// 同样挡住（空元素会顶掉真实首址并让端点/日志落在空串上）。
//
// **codec 未注册即报错**：broker.WithCodec 对未注册的名字静默置 nil
// （broker/options.go → encoding.GetCodec），首条 marshal 才炸 errNoCodec；
// 契约字段非空即查注册表，装配期报错比首包诚实。
func Provider(opts ...Option) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	c := &providerConfig{}
	for _, o := range opts {
		o(c)
	}
	return func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetRocketmq()
		if sec == nil {
			return nil, nil, nil // 未配置 rocketmq 段，跳过
		}
		addrs := sec.GetNameServerAddrs()
		if len(addrs) == 0 {
			return nil, nil, fmt.Errorf("rocketmq: server.rocketmq.name_server_addrs is required (remove the rocketmq section to disable)")
		}
		for i, a := range addrs {
			if a == "" {
				return nil, nil, fmt.Errorf("rocketmq: server.rocketmq.name_server_addrs[%d] is empty (the v2 broker would fall back to the built-in default 127.0.0.1:9876; drop the empty element or the section)", i)
			}
		}
		driver, err := driverType(sec.GetDriverType())
		if err != nil {
			return nil, nil, err
		}

		// 契约字段在前，业务 Option 在后（后者可覆盖前者——与 kafka/tcp 同约定）。
		base, err := buildOpts(addrs, sec.GetCodec())
		if err != nil {
			return nil, nil, err
		}
		allOpts := append(base, c.serverOpts...) //nolint:gocritic // base 是本函数新建的定长切片

		srv := rocketmq.NewServer(driver, allOpts...)

		for _, fn := range c.afterBuild {
			if err := fn(ctx, srv); err != nil {
				return nil, nil, fmt.Errorf("rocketmq: after-build hook: %w", err)
			}
		}
		return srv, nil, nil
	}
}

// driverType 把契约 driver_type 收口到实现真实的驱动：代码里 transport 恒构造
// v2 broker，"" 视作「未配」按实现默认走 v2；v5/aliyun 未接线，报错而不是
// 假装接受。
func driverType(v string) (option.DriverType, error) {
	switch v {
	case "", "v2":
		return option.DriverTypeV2, nil
	default:
		return "", fmt.Errorf("rocketmq: server.rocketmq.driver_type %q is not implemented (transport/rocketmq always builds the v2 broker — the v5/aliyun drivers have no code behind them; use driver_type: v2 or drop the field)", v)
	}
}

// buildOpts 把契约 rocketmq 段映射为 rocketmq.ServerOption 序列。
//
// addrs 无条件传（工厂入口已挡空值/空元素）；codec 非空即查注册表，注册了才追加。
func buildOpts(addrs []string, codec string) ([]rocketmq.ServerOption, error) {
	opts := []rocketmq.ServerOption{rocketmq.WithNameServer(addrs)}
	if codec != "" {
		if encoding.GetCodec(codec) == nil {
			return nil, fmt.Errorf("rocketmq: server.rocketmq.codec %q is not registered (import the codec package and register it, e.g. encoding.MustRegister(jsoncodec.New()), or drop the codec field)", codec)
		}
		opts = append(opts, rocketmq.WithCodec(codec))
	}
	return opts, nil
}

// 类型守卫：Provider 保持构造 func 形状（供 server.rocketmq 段 Provider 的
// Build 调用；不在本包 import bootstrap——避免该依赖）。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider()
