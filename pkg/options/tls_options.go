package options

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/pflag"
)

// 确保接口实现。
var _ IOptions = (*TLSOptions)(nil)

// TLSOptions 定义安全传输配置，采用 "Smart Mode"：
// CA / Cert / Key 字段既可以是文件路径（如 /etc/certs/ca.pem），
// 也可以是原始 PEM 内容（-----BEGIN CERTIFICATE-----...），
// 还可以是 Base64 编码的 PEM（K8s Secret 常以 Base64 注入）。
type TLSOptions struct {
	// Enabled 是否启用 TLS 传输。
	Enabled bool `json:"enabled" mapstructure:"enabled"`

	// SkipVerify 客户端是否跳过服务端证书链与主机名校验（仅客户端场景使用，不安全）。
	SkipVerify bool `json:"skip-verify" mapstructure:"skip-verify"`

	// Smart 字段：可为绝对文件路径、原始 PEM 数据或 Base64 编码 PEM 数据。
	CA   string `json:"ca" mapstructure:"ca"`
	Cert string `json:"cert" mapstructure:"cert"`
	Key  string `json:"key" mapstructure:"key"`
}

// NewTLSOptions 构造零值 TLSOptions（默认不启用）。
func NewTLSOptions() TLSOptions {
	return TLSOptions{Enabled: false}
}

// Validate 校验 TLS 配置一致性。
func (o *TLSOptions) Validate() []error {
	errs := []error{}
	if o == nil || !o.Enabled {
		return errs
	}
	// Cert 与 Key 必须同时提供或同时不提供（仅客户端可只配 CA）。
	if (o.Cert != "" && o.Key == "") || (o.Cert == "" && o.Key != "") {
		errs = append(errs, fmt.Errorf("both cert and key must be provided to enable mTLS"))
	}
	return errs
}

// AddFlags 注册 TLS 相关命令行参数（带完整前缀，前缀应已含末尾点）。
func (o *TLSOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.BoolVar(&o.Enabled, fullPrefix+"enabled", o.Enabled, "Enable TLS transport.")
	fs.BoolVar(&o.SkipVerify, fullPrefix+"skip-verify", o.SkipVerify, "Insecurely skip TLS certificate verification.")
	fs.StringVar(&o.CA, fullPrefix+"ca", o.CA, "Path to CA file OR raw PEM data OR base64 pem.")
	fs.StringVar(&o.Cert, fullPrefix+"cert", o.Cert, "Path to Cert file OR raw PEM data OR base64 pem.")
	fs.StringVar(&o.Key, fullPrefix+"key", o.Key, "Path to Key file OR raw PEM data OR base64 pem.")
}

// TLSConfig 生成最终的 *tls.Config；未启用时返回 nil。
func (o *TLSOptions) TLSConfig() (*tls.Config, error) {
	if o == nil || !o.Enabled {
		return nil, nil
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: o.SkipVerify,
		MinVersion:         tls.VersionTLS12,
	}

	// 1. 加载证书对（Cert + Key）。
	var certBytes, keyBytes []byte
	var err error
	if o.Cert != "" {
		if certBytes, err = loadResource(o.Cert); err != nil {
			return nil, fmt.Errorf("failed to load cert: %w", err)
		}
	}
	if o.Key != "" {
		if keyBytes, err = loadResource(o.Key); err != nil {
			return nil, fmt.Errorf("failed to load key: %w", err)
		}
	}
	if len(certBytes) > 0 && len(keyBytes) > 0 {
		cert, err := tls.X509KeyPair(certBytes, keyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse x509 key pair: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	// 2. 加载 CA（可选，常用于 mTLS 客户端校验）。
	if o.CA != "" {
		caBytes, err := loadResource(o.CA)
		if err != nil {
			return nil, fmt.Errorf("failed to load ca: %w", err)
		}
		if len(caBytes) > 0 {
			capool := x509.NewCertPool()
			if !capool.AppendCertsFromPEM(caBytes) {
				return nil, fmt.Errorf("failed to append ca certs from pem")
			}
			tlsConfig.ClientCAs = capool
			tlsConfig.RootCAs = capool
			tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		}
	}

	return tlsConfig, nil
}

// MustTLSConfig 返回 *tls.Config，出错时记日志并返回非安全默认配置（fail-safe）。
func (o *TLSOptions) MustTLSConfig() *tls.Config {
	tlsConf, err := o.TLSConfig()
	if err != nil {
		slog.Error("Failed to build tls config", "error", err)
		return &tls.Config{InsecureSkipVerify: true}
	}
	return tlsConf
}

// Scheme 根据 TLS 配置返回 URL scheme（https / http）。
func (o *TLSOptions) Scheme() string {
	if o != nil && o.Enabled {
		return "https"
	}
	return "http"
}

// loadResource 智能识别输入是"文件路径"、"原始 PEM 字符串"还是"Base64 编码字符串"。
func loadResource(input string) ([]byte, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}
	// 1. 高优先级：含 PEM 头直接返回，避免无谓磁盘 IO。
	if strings.Contains(input, "-----BEGIN") {
		return []byte(input), nil
	}
	// 2. 尝试识别文件路径：无换行且长度合理时若文件存在则读取。
	if !strings.Contains(input, "\n") && len(input) < 1024 {
		if _, err := os.Stat(input); err == nil {
			return os.ReadFile(input)
		}
	}
	// 3. 尝试 Base64 解码（K8s Secret 常以 Base64 注入）。
	//    注意：仅当解码结果形如 PEM 时才采用；普通 base64 文本（不含 -----BEGIN）
	//    不在此分支消费，避免掩盖"文件路径"语义。顺序上文件分支已优先于本分支。
	if decoded, err := base64.StdEncoding.DecodeString(input); err == nil {
		if strings.Contains(string(decoded), "-----BEGIN") {
			return decoded, nil
		}
	}
	// 4. 既非文件、原始 PEM，也非合法 Base64 PEM。
	return nil, fmt.Errorf("input is neither a valid file path, nor raw PEM data, nor base64 encoded PEM")
}
