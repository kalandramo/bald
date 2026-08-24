package options

import (
	"time"

	"github.com/spf13/pflag"
)

// 确保接口实现（对齐 onexstack 风格的编译期断言）。
var _ IOptions = (*SecureServingOptions)(nil)

// SecureServingOptions 定义 HTTPS 服务器配置（明文 + TLS 一体的 serving 形态）。
// 通过内嵌 TLSOptions 实现 "Smart Mode"：CA/Cert/Key 可为文件路径、原始 PEM 或 Base64。
// 与 InsecureServingOptions（纯明文 HTTP）形成对称，对应 onexstack 的同名文件结构。
type SecureServingOptions struct {
	// Addr 监听地址，如 ":443"。
	Addr string `json:"addr" mapstructure:"addr"`

	// Timeout 连接超时（如 ReadHeaderTimeout）。
	Timeout time.Duration `json:"timeout" mapstructure:"timeout"`

	// TLSOptions 内嵌：提供 Smart Mode 的 CA/Cert/Key 与 Enabled/SkipVerify。
	TLSOptions `json:",inline" mapstructure:",squash"`
}

// NewSecureServingOptions 构造带默认值的 SecureServingOptions（默认不启用 TLS）。
func NewSecureServingOptions() *SecureServingOptions {
	return &SecureServingOptions{
		Addr:       ":443",
		Timeout:    10 * time.Second,
		TLSOptions: NewTLSOptions(),
	}
}

// Validate 校验 HTTPS 配置：先校验监听地址，再透传 TLS 校验。
func (o *SecureServingOptions) Validate() []error {
	errs := []error{}
	if err := ValidateAddress(o.Addr); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, o.TLSOptions.Validate()...)
	return errs
}

// AddFlags 注册 HTTPS 相关命令行参数（带完整前缀，前缀应已含末尾点）。
// TLS 子字段经内嵌 TLSOptions.AddFlags 自然展开为 --<prefix>.tls.ca 等。
func (o *SecureServingOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.Addr, fullPrefix+"addr", o.Addr, "The address the secure server listens on (e.g. :443).")
	fs.DurationVar(&o.Timeout, fullPrefix+"timeout", o.Timeout, "Timeout for TLS connections.")
	o.TLSOptions.AddFlags(fs, fullPrefix+"tls.")
}
