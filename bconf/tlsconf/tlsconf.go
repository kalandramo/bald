// Package tlsconf 把契约的 Server.TLS 段翻译成 crypto/tls 配置。
//
// 它是契约自带的语义，不是某个传输实现的私货：任何按契约声明了 tls 的
// 服务端/客户端都经本包取配置，避免「字段写了但没人读」的静默失效。
package tlsconf

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// Server 返回服务端 TLS 配置；cfg 为 nil 表示不启用 TLS。
//
// 证书来源二选一（契约 Server.TLS 的 file / config）：
//   - file：cert_path / key_path / ca_path 指向 PEM 文件；
//   - config：cert_pem / key_pem / ca_pem 内联 PEM（K8s Secret 挂载或配置中心
//     下发场景，避免落盘）。
//
// ca 非空即双向校验（mTLS）：ClientCAs = ca 池，且要求并校验客户端证书。
// 最低 TLS 1.2；契约的 insecure_skip_verify 在服务端不生效（它放宽的是「对端」
// 校验，服务端的对端校验由 ca 决定）。
func Server(cfg *bootstrapv1.Server_TLS) (*tls.Config, error) {
	if cfg == nil {
		return nil, nil
	}
	m, err := read(cfg)
	if err != nil {
		return nil, err
	}
	switch {
	case len(m.cert) == 0:
		return nil, fmt.Errorf("tls: server needs a certificate (file.cert_path / config.cert_pem)")
	case len(m.key) == 0:
		return nil, fmt.Errorf("tls: server needs a private key (file.key_path / config.key_pem)")
	}
	cert, err := tls.X509KeyPair(m.cert, m.key)
	if err != nil {
		return nil, fmt.Errorf("parse server key pair: %w", err)
	}
	out := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	if len(m.ca) > 0 {
		pool, err := certPool(m.ca)
		if err != nil {
			return nil, err
		}
		out.ClientCAs = pool
		out.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return out, nil
}

// Client 返回客户端 TLS 配置（用于按契约拨已启用 TLS 的对端）。
//
// ca 非空时用该池校验服务端证书链；缺失且未跳过校验时用系统根证书池。
// 声明了 cert+key 即提供客户端证书（对端要求 mTLS 时生效）。
// insecure_skip_verify 跳过服务端证书校验（测试/内网场景）。
func Client(cfg *bootstrapv1.Server_TLS) (*tls.Config, error) {
	if cfg == nil {
		return nil, nil
	}
	m, err := read(cfg)
	if err != nil {
		return nil, err
	}
	out := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.GetInsecureSkipVerify()} //nolint:gosec // 契约显式声明（测试/内网场景）
	if len(m.cert) > 0 || len(m.key) > 0 {
		cert, err := tls.X509KeyPair(m.cert, m.key)
		if err != nil {
			return nil, fmt.Errorf("parse client key pair: %w", err)
		}
		out.Certificates = []tls.Certificate{cert}
	}
	switch {
	case len(m.ca) > 0:
		if out.RootCAs, err = certPool(m.ca); err != nil {
			return nil, err
		}
	case !out.InsecureSkipVerify:
		if out.RootCAs, err = x509.SystemCertPool(); err != nil {
			return nil, fmt.Errorf("load system root pool: %w", err)
		}
	}
	return out, nil
}

// material 是契约 tls 段解析出的 PEM 材料；缺项为空切片。
type material struct {
	cert, key, ca []byte
}

// read 按 file / config 二选一取出 PEM 材料（两者都没有则三项皆空）。
func read(cfg *bootstrapv1.Server_TLS) (material, error) {
	switch {
	case cfg.GetFile() != nil:
		f := cfg.GetFile()
		cert, err := readOpt(f.GetCertPath())
		if err != nil {
			return material{}, err
		}
		key, err := readOpt(f.GetKeyPath())
		if err != nil {
			return material{}, err
		}
		ca, err := readOpt(f.GetCaPath())
		if err != nil {
			return material{}, err
		}
		return material{cert: cert, key: key, ca: ca}, nil
	case cfg.GetConfig() != nil:
		c := cfg.GetConfig()
		return material{cert: c.GetCertPem(), key: c.GetKeyPem(), ca: c.GetCaPem()}, nil
	}
	return material{}, nil
}

func certPool(caPEM []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("tls: ca has no valid certificate")
	}
	return pool, nil
}

func readOpt(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tls material %s: %w", path, err)
	}
	return b, nil
}
