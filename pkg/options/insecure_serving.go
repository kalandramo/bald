// Package options 提供 bald 框架各协议服务器所需的配置选项。
// 设计对齐 onexstack/pkg/options：统一的 IOptions 接口、AddFlags(fs, fullPrefix)
// 前缀式 flag 注册、Validate() []error 校验，以及可复用的地址校验/监听工具。
package options

import (
	"time"

	"github.com/spf13/pflag"
)

// 确保接口实现（对齐 onexstack 风格的编译期断言）。
var _ IOptions = (*InsecureServingOptions)(nil)

// InsecureServingOptions 持有 HTTP 服务器配置（明文 HTTP，非 TLS）。
// 与 SecureServingOptions（HTTPS）形成对称，对应 onexstack 的同名文件结构。
type InsecureServingOptions struct {
	// Addr 监听地址，如 ":8080"。
	Addr string `json:"addr" mapstructure:"addr"`
	// Timeout 连接超时，供 HTTP 客户端侧使用。
	Timeout time.Duration `json:"timeout" mapstructure:"timeout"`
}

// NewInsecureServingOptions 构造带默认值的 InsecureServingOptions。
func NewInsecureServingOptions() *InsecureServingOptions {
	return &InsecureServingOptions{
		Addr:    ":8080",
		Timeout: 30 * time.Second,
	}
}

// Validate 校验 HTTP 配置。
func (o *InsecureServingOptions) Validate() []error {
	if o == nil {
		return nil
	}
	var errs []error
	if err := ValidateAddress(o.Addr); err != nil {
		errs = append(errs, err)
	}
	return errs
}

// AddFlags 注册 HTTP 相关命令行参数（带完整前缀）。
// fullPrefix 应已含末尾点（如 Join("bald-demo","http") 产生的 "bald-demo.http."），
// 字段名直接拼接其后，避免双点（如 "bald-demo.http.addr"）。
func (o *InsecureServingOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.Addr, fullPrefix+"addr", o.Addr,
		"Listen address for the HTTP server (e.g., :8080, 0.0.0.0:8443).")
	fs.DurationVar(&o.Timeout, fullPrefix+"timeout", o.Timeout,
		"Timeout for incoming HTTP connections.")
}
