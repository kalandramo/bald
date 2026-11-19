// Package brokercontract 是 broker 型传输家族契约装配的共享 kernel：
// server.<家族> 段的「校验 → 构造 → afterBuild」序列此前在 kafka/rabbitmq
// 两份 contract.go 里逐行克隆（J2 单一闸门），本包归并为单份泛型序列。
//
// 分层边界（刻意设计）：本包只 import bconf/encoding——appkit.Provider 的壳
// （Segments/Plan/Build 委派）与 WithServerOptions/WithAfterBuild 的类型面留在
// 各家族 contract 包（transport/{kafka,rabbitmq}/contract，惯例同
// transport/{sse,cron,asynq,…}/contract），核心家族包（transport/kafka 等）
// 依旧零契约依赖。
package brokercontract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/encoding"

	"github.com/kalandramo/bald/transport"
)

// Segment 是家族契约子段的公共形状：bootstrapv1.Server_Kafka /
// bootstrapv1.Server_Rabbitmq 各自满足（子段字段面一致：addrs / codec）。
type Segment interface {
	GetAddrs() []string
	GetCodec() string
}

// Factory 是契约装配序列的家族参数化点。OPT 是家族 ServerOption 类型。
type Factory[OPT any] struct {
	// Kind 是错误消息前缀（"kafka" / "rabbitmq"）。
	Kind string
	// Key 是契约段键（"server.kafka" / "server.rabbitmq"）。
	Key string
	// Section 从 bootstrapv1.Server 取家族子段（nil = 段未配置，引擎跳过）。
	Section func(*bootstrapv1.Server) Segment
	// EmptyAddrErr 产出空地址元素的错误文案——两家族措辞不同（kafka 泛指
	// 内置默认地址、rabbitmq 带下标与 amqp 默认值说明），文案是运维面，保留钩子。
	EmptyAddrErr func(index int) error
	// WithAddrs / WithCodec 把契约字段翻成家族选项（两侧 buildOpts 的同构映射）。
	WithAddrs func(addrs []string) OPT
	WithCodec func(codec string) OPT
	// NewServer 是家族工厂。
	NewServer func(opts ...OPT) transport.Server
}

// Build 是共享的契约装配序列（与归并前的两份逐行同构）：
//   - 段 nil → 跳过（返回 nil server，引擎跳过）；
//   - addrs 空 → fail-fast：「配置写了、落点不由配置决定」——broker Init 会
//     整体兜底到内置默认地址（kafka 127.0.0.1:9092 / rabbitmq 自带 guest 凭证
//     的 amqp 默认），静默失效路径契约侧不接受；
//   - 空元素 → fail-fast（家族文案见 EmptyAddrErr）；
//   - 契约字段在前、业务 serverOpts 在后（Apply 末者胜，业务可覆盖契约 addrs）；
//   - codec 契约值非空即查注册表（未注册装配期报错，比首包诚实）；
//   - afterBuild 逐个执行（早于 appkit 的 Start），错误包裹 Kind 前缀后短路。
func (f Factory[OPT]) Build(ctx context.Context, cfg *bootstrapv1.Server, serverOpts []OPT, afterBuild []func(context.Context, transport.Server) error) (transport.Server, func(), error) {
	sec := f.Section(cfg)
	if sec == nil {
		return nil, nil, nil // 未配置本家族段，跳过
	}
	addrs := sec.GetAddrs()
	if len(addrs) == 0 {
		return nil, nil, fmt.Errorf("%s: %s.addrs is required (remove the %s section to disable)", f.Kind, f.Key, f.Kind)
	}
	for i, a := range addrs {
		if a == "" {
			return nil, nil, f.EmptyAddrErr(i)
		}
	}

	// 契约字段在前，业务 Option 在后（后者可覆盖前者——与 sse/tcp/graphql 同约定）。
	base, err := f.buildOpts(sec)
	if err != nil {
		return nil, nil, err
	}
	allOpts := append(base, serverOpts...) //nolint:gocritic // base 是本函数新建的定长切片

	srv := f.NewServer(allOpts...)

	for _, fn := range afterBuild {
		if err := fn(ctx, srv); err != nil {
			return nil, nil, fmt.Errorf("%s: after-build hook: %w", f.Kind, err)
		}
	}
	return srv, nil, nil
}

// buildOpts 把契约段映射为选项序列：addrs 无条件传（工厂入口已挡空值与空元素）；
// codec 非空即查注册表，注册了才追加——底层 WithCodec 先查 encoding.GetCodec
// （未注册即 nil），首包才炸 errNoCodec。
func (f Factory[OPT]) buildOpts(sec Segment) ([]OPT, error) {
	opts := []OPT{f.WithAddrs(sec.GetAddrs())}
	if codec := sec.GetCodec(); codec != "" {
		if encoding.GetCodec(codec) == nil {
			return nil, fmt.Errorf("%s: %s.codec %q is not registered (import the codec package and register it, e.g. encoding.MustRegister(jsoncodec.New()), or drop the codec field)", f.Kind, f.Key, codec)
		}
		opts = append(opts, f.WithCodec(codec))
	}
	return opts, nil
}

// Contract 是家族 contract 包的泛型骨架（片 3）：把 Option/providerConfig/
// WithServerOptions/WithAfterBuild/Provider 五件套上收为单份——两家族只剩
// Type 常量、骨架实例与一行薄包装。SRV 是家族 Server 具体类型（*kafka.Server），
// OPT 是家族 ServerOption 类型。
type Contract[SRV any, OPT any] struct {
	Factory Factory[OPT]
	// Kind2 是段路径名词（"server.kafka" / "server.rabbitmq"），与 Factory.Key 同值。
	// Factory.Key 是错误消息前缀（不带 server. 前缀）。
	SegmentPath string
}

// Option 是骨架化的家族契约选项（WithServerOptions/WithAfterBuild 的共同底座）。
type Option[SRV any, OPT any] func(*contractConfig[SRV, OPT])

type contractConfig[SRV any, OPT any] struct {
	serverOpts []OPT
	afterBuild []func(context.Context, SRV) error
}

// WithServerOptions 追加家族 ServerOption（契约段表达不了的能力）。
func (c Contract[SRV, OPT]) WithServerOptions(opts ...OPT) Option[SRV, OPT] {
	return func(cc *contractConfig[SRV, OPT]) { cc.serverOpts = append(cc.serverOpts, opts...) }
}

// WithAfterBuild 注册「server 构造后」的回调（早于 appkit 的 Start）。
func (c Contract[SRV, OPT]) WithAfterBuild(fn func(context.Context, SRV) error) Option[SRV, OPT] {
	return func(cc *contractConfig[SRV, OPT]) { cc.afterBuild = append(cc.afterBuild, fn) }
}

// Provider 返回契约驱动的工厂函数（家族 contract 包的 Provider 薄壳委派到此）：
// opts 依次作用于闭包内 config，家族强类型 afterBuild 经 SRV 断言适配为
// transport.Server 面后委派 Factory.Build（序列本体见上）。
func (c Contract[SRV, OPT]) Provider(opts ...Option[SRV, OPT]) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	return func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		cc := &contractConfig[SRV, OPT]{}
		for _, o := range opts {
			o(cc)
		}
		afterBuild := make([]func(context.Context, transport.Server) error, 0, len(cc.afterBuild))
		for _, fn := range cc.afterBuild {
			fn := fn
			afterBuild = append(afterBuild, func(ctx context.Context, ts transport.Server) error {
				srv, ok := ts.(SRV)
				if !ok {
					return fmt.Errorf("%s: after-build got %T, want %T", c.Factory.Kind, ts, srv)
				}
				return fn(ctx, srv)
			})
		}
		return c.Factory.Build(ctx, cfg, cc.serverOpts, afterBuild)
	}
}
