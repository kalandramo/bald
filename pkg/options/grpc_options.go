// Package options 提供 bald 框架各协议服务器所需的配置选项。
// 设计对齐 onexstack/pkg/options：统一的 IOptions 接口、AddFlags(fs, fullPrefix)
// 前缀式 flag 注册、Validate() []error 校验，以及可复用的地址校验/监听工具。
package options

import (
	"time"

	"github.com/spf13/pflag"
)

// 确保接口实现（对齐 onexstack 风格的编译期断言）。
var _ IOptions = (*GRPCOptions)(nil)

// GRPCOptions 持有 gRPC 服务器配置。
type GRPCOptions struct {
	// Network 网络类型，默认 "tcp"。
	Network string `json:"network" mapstructure:"network"`
	// Addr 监听地址，如 ":9090"。
	Addr string `json:"addr" mapstructure:"addr"`
	// Timeout 连接超时，供 gRPC 客户端侧使用。
	Timeout time.Duration `json:"timeout" mapstructure:"timeout"`
}

// NewGRPCOptions 构造带默认值的 GRPCOptions。
func NewGRPCOptions() *GRPCOptions {
	return &GRPCOptions{
		Network: "tcp",
		Addr:    ":9090",
		Timeout: 30 * time.Second,
	}
}

// Validate 校验 gRPC 配置。
func (o *GRPCOptions) Validate() []error {
	if o == nil {
		return nil
	}
	var errs []error
	if err := ValidateAddress(o.Addr); err != nil {
		errs = append(errs, err)
	}
	return errs
}

// AddFlags 注册 gRPC 相关命令行参数（带完整前缀，前缀应已含末尾点）。
func (o *GRPCOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.Network, fullPrefix+"network", o.Network, "Specify the network for the gRPC server.")
	fs.StringVar(&o.Addr, fullPrefix+"addr", o.Addr, "Specify the gRPC server bind address and port.")
	fs.DurationVar(&o.Timeout, fullPrefix+"timeout", o.Timeout, "Timeout for gRPC server connections.")
}
