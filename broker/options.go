package broker

import (
	"context"
	"crypto/tls"

	"github.com/kalandramo/bald/encoding"
)

// defaultCodecName 是 broker 的默认编解码器名。
//
// 查表刻意是**惰性**的（在 [NewOptions] 调用时，而非包初始化时）：encoding
// 注册表要求显式注册（[encoding.MustRegister]），包初始化期必然为空，
// 包级 var 求值只会得到一个恒 nil 的假默认值。未注册时 [encoding.GetCodec]
// 返回 nil，[Marshal]/[Unmarshal] 随即 fail-fast 并给出注册指引。
const defaultCodecName = "json"

///////////////////////////////////////////////////////////////////////////////

// Options broker options
type Options struct {
	// Addrs is a Broker addresses
	Addrs []string

	// Codec is a Broker codec
	Codec encoding.Codec

	// ErrorHandler is a Broker error handler
	ErrorHandler Handler

	// Secure enable secure connection
	Secure bool
	// TLSConfig is tls config for secure connection
	TLSConfig *tls.Config

	// Context is broker option context
	Context context.Context

	// SubscriberMiddlewares applies to subscribe handlers
	SubscriberMiddlewares []SubscriberMiddleware

	// PublishMiddlewares applies to publish handlers
	PublishMiddlewares []PublishMiddleware
}

// Option defines a function which sets some option.
type Option func(*Options)

// Apply applies all options to the Options.
func (o *Options) Apply(opts ...Option) {
	if o == nil {
		return
	}
	for _, opt := range opts {
		opt(o)
	}
}

// NewOptions creates default Options.
//
// Codec 在此处（而非包初始化期）惰性查表，使「main 里先 MustRegister 再构造」
// 的用法能拿到真实 codec；未注册时为 nil，由 [Marshal]/[Unmarshal] fail-fast。
func NewOptions() Options {
	opt := Options{
		Addrs: []string{},
		Codec: encoding.GetCodec(defaultCodecName),

		ErrorHandler: nil,

		Secure:    false,
		TLSConfig: nil,

		Context: context.Background(),
	}

	return opt
}

// NewOptionsAndApply creates Options and applies given Option functions.
func NewOptionsAndApply(opts ...Option) Options {
	opt := NewOptions()
	opt.Apply(opts...)
	return opt
}

// WithOptionContext sets the broker option context
func WithOptionContext(ctx context.Context) Option {
	return func(o *Options) {
		if o == nil {
			return
		}
		o.Context = ctx
	}
}

// OptionContextWithValue sets a value in the broker option context
func OptionContextWithValue(k, v any) Option {
	return func(o *Options) {
		if o == nil {
			return
		}
		if o.Context == nil {
			o.Context = context.Background()
		}
		o.Context = context.WithValue(o.Context, k, v)
	}
}

// WithAddress set broker address
func WithAddress(addressList ...string) Option {
	addrsCopy := append([]string(nil), addressList...)
	return func(o *Options) {
		if o == nil {
			return
		}
		o.Addrs = addrsCopy
	}
}

// WithCodec set codec, support: json, proto.
//
// 名称查表失败（未注册）时 Codec 保持 nil，随后 [Marshal]/[Unmarshal] 会
// fail-fast 并给出注册指引——不再静默回落到 gob。
func WithCodec(name string) Option {
	return func(o *Options) {
		if o == nil {
			return
		}
		o.Codec = encoding.GetCodec(name)
	}
}

// WithErrorHandler sets error handler
func WithErrorHandler(handler Handler) Option {
	return func(o *Options) {
		if o == nil {
			return
		}
		o.ErrorHandler = handler
	}
}

// WithEnableSecure sets enable secure connection
func WithEnableSecure(enable bool) Option {
	return func(o *Options) {
		if o == nil {
			return
		}
		o.Secure = enable
	}
}

// WithTLSConfig sets tls config for secure connection
func WithTLSConfig(config *tls.Config) Option {
	return func(o *Options) {
		if o == nil {
			return
		}

		o.TLSConfig = config
		if o.TLSConfig != nil {
			o.Secure = true
		}
	}
}

// WithSubscriberMiddlewares sets subscriber middlewares
func WithSubscriberMiddlewares(mws ...SubscriberMiddleware) Option {
	m := append([]SubscriberMiddleware(nil), mws...)
	return func(o *Options) {
		if o == nil {
			return
		}
		o.SubscriberMiddlewares = m
	}
}

// WithPublishMiddlewares sets publish middlewares
func WithPublishMiddlewares(mws ...PublishMiddleware) Option {
	m := append([]PublishMiddleware(nil), mws...)
	return func(o *Options) {
		if o == nil {
			return
		}
		o.PublishMiddlewares = m
	}
}
