// Package options 提供 bald 框架各协议服务器所需的配置选项。
// 采用与 onexstack/pkg/options 一致的指针字段风格，便于 viper 配置绑定。
package options

import (
	"fmt"

	"github.com/spf13/pflag"
)

// HTTPOptions 持有 HTTP 服务器配置。
type HTTPOptions struct {
	// Addr 是监听地址，如 ":8080"。
	Addr string `json:"addr" mapstructure:"addr"`
	// TLS 相关配置（可选）。留空则使用纯 HTTP。
	TLS *TLSOptions `json:"tls,omitempty" mapstructure:"tls"`
}

func NewHTTPOptions() *HTTPOptions {
	return &HTTPOptions{Addr: ":8080"}
}

// Flags 注册 HTTP 相关命令行参数。
func (o *HTTPOptions) Flags() *pflag.FlagSet {
	if o.TLS == nil {
		o.TLS = &TLSOptions{}
	}
	fs := pflag.NewFlagSet("http", pflag.ExitOnError)
	fs.StringVar(&o.Addr, "http.addr", o.Addr, "HTTP server listen address.")
	fs.BoolVar(&o.TLS.Enabled, "http.tls.enabled", false, "Enable HTTPS.")
	fs.StringVar(&o.TLS.CertFile, "http.tls.cert-file", "", "HTTPS cert file path.")
	fs.StringVar(&o.TLS.KeyFile, "http.tls.key-file", "", "HTTPS key file path.")
	return fs
}

// GRPCOptions 持有 gRPC 服务器配置。
type GRPCOptions struct {
	Addr string `json:"addr" mapstructure:"addr"`
}

func NewGRPCOptions() *GRPCOptions {
	return &GRPCOptions{Addr: ":9090"}
}

func (o *GRPCOptions) Flags() *pflag.FlagSet {
	fs := pflag.NewFlagSet("grpc", pflag.ExitOnError)
	fs.StringVar(&o.Addr, "grpc.addr", o.Addr, "gRPC server listen address.")
	return fs
}

// TLSOptions 持有 TLS 配置。
type TLSOptions struct {
	Enabled  bool   `json:"enabled" mapstructure:"enabled"`
	CertFile string `json:"cert-file" mapstructure:"cert-file"`
	KeyFile  string `json:"key-file" mapstructure:"key-file"`
}

// Validate 校验 TLS 配置一致性。
func (o *TLSOptions) Validate() error {
	if !o.Enabled {
		return nil
	}
	if o.CertFile == "" || o.KeyFile == "" {
		return fmt.Errorf("tls enabled but cert-file/key-file not set")
	}
	return nil
}

// Validate 校验 HTTP 配置一致性。
func (o *HTTPOptions) Validate() error {
	if o.Addr == "" {
		return fmt.Errorf("http addr must not be empty")
	}
	if o.TLS != nil {
		return o.TLS.Validate()
	}
	return nil
}
