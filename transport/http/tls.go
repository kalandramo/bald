package httpserver

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// resolveTLS 根据 bootstrapv1.Server_TLS 配置生成 *tls.Config。
//
// TLS 支持两种互斥来源（契约 Server.TLS 的 file / config 二选一）：
//   - file 模式：cert_path / key_path / ca_path 指向 PEM 文件；
//   - config 模式：cert_pem / key_pem / ca_pem 直接内联 PEM bytes
//     （K8s Secret 挂载或配置中心下发场景，避免落盘）。
//
// ca（任一模式）用于校验对端证书链；minVersion 固定 TLS 1.2。
// cfg 为 nil 时返回 nil（纯 HTTP）。
func resolveTLS(cfg *bootstrapv1.Server_TLS) (*tls.Config, error) {
	if cfg == nil {
		return nil, nil
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	var certPEM, keyPEM, caPEM []byte
	switch {
	case cfg.GetFile() != nil:
		f := cfg.GetFile()
		var err error
		if p := f.GetCertPath(); p != "" {
			if certPEM, err = os.ReadFile(p); err != nil {
				return nil, fmt.Errorf("read tls cert %s: %w", p, err)
			}
		}
		if p := f.GetKeyPath(); p != "" {
			if keyPEM, err = os.ReadFile(p); err != nil {
				return nil, fmt.Errorf("read tls key %s: %w", p, err)
			}
		}
		if p := f.GetCaPath(); p != "" {
			if caPEM, err = os.ReadFile(p); err != nil {
				return nil, fmt.Errorf("read tls ca %s: %w", p, err)
			}
		}
	case cfg.GetConfig() != nil:
		c := cfg.GetConfig()
		certPEM, keyPEM, caPEM = c.GetCertPem(), c.GetKeyPem(), c.GetCaPem()
	}

	if len(certPEM) > 0 && len(keyPEM) > 0 {
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("parse x509 key pair: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("append ca pem: no valid certificate found")
		}
		tlsConfig.RootCAs = pool
	}
	if cfg.GetInsecureSkipVerify() {
		tlsConfig.InsecureSkipVerify = true //nolint:gosec // 契约显式声明（测试/内网场景）
	}
	return tlsConfig, nil
}

// scheme 根据 TLS 配置返回 URL scheme（https / http）：TLS 段非 nil 即 https。
func scheme(cfg *bootstrapv1.Server_TLS) string {
	if cfg != nil {
		return "https"
	}
	return "http"
}
