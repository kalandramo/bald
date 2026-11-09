// Package tlstest 现签一套测试证书（CA + 服务端/客户端叶子），并给出对应的
// 契约 tls 段——供 transport/bootstrap 的 TLS 用例复用。
//
// 仓内不放静态密钥：材料每次调用现场生成，只在内存里存在。证书有效期 1 小时、
// SAN 含 localhost/127.0.0.1，够本机回环握手用。
package tlstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// Certs 是一套 CA 签发的双端证书材料（均为 PEM 字节）。
type Certs struct {
	CA         []byte
	ServerCert []byte
	ServerKey  []byte
	ClientCert []byte
	ClientKey  []byte
}

// Generate 签一套 CA + 服务端/客户端证书。serial 递增只为区分两张叶子。
//
// 下面所有 if err != nil 分支都是 crypto 对「刚生成的合法材料」的错误返回——
// rand.Reader / x509 签发解析必然成功，正确测试无法构造失败，故 cov:excl 排除。
func Generate() (*Certs, error) {
	caKey, caDER, err := newCA()
	if err != nil {
		return nil, err //cov:excl newCA 对合法输入必然成功
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("parse ca cert: %w", err) //cov:excl 刚 CreateCertificate 的 DER 必然可解析
	}
	out := &Certs{CA: encodePEM("CERTIFICATE", caDER)}
	if out.ServerCert, out.ServerKey, err = newLeaf(caCert, caKey, 2); err != nil {
		return nil, err //cov:excl newLeaf 对合法输入必然成功
	}
	if out.ClientCert, out.ClientKey, err = newLeaf(caCert, caKey, 3); err != nil {
		return nil, err //cov:excl newLeaf 对合法输入必然成功
	}
	return out, nil
}

// ServerTLS 是「服务端证书 + 要求客户端证书」的契约写法（内联 PEM，不落盘）。
func (c *Certs) ServerTLS() *bootstrapv1.Server_TLS {
	return &bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{
		CertPem: c.ServerCert, KeyPem: c.ServerKey, CaPem: c.CA,
	}}
}

// ClientTLS 是「信任该 CA + 提供客户端证书」的契约写法。
func (c *Certs) ClientTLS() *bootstrapv1.Server_TLS {
	return &bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{
		CertPem: c.ClientCert, KeyPem: c.ClientKey, CaPem: c.CA,
	}}
}

func newCA() (*ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ca key: %w", err) //cov:excl rand.Reader 签发不会失败
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "bald-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create ca cert: %w", err) //cov:excl 合法模板自签不会失败
	}
	return key, der, nil
}

// newLeaf 用 ca 签一张双用途（服务端 + 客户端）叶子证书。
func newLeaf(ca *x509.Certificate, caKey *ecdsa.PrivateKey, serial int64) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate leaf key: %w", err) //cov:excl rand.Reader 签发不会失败
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "bald-test-leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create leaf cert: %w", err) //cov:excl 合法模板签发不会失败
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal leaf key: %w", err) //cov:excl 合法 EC 私钥 marshal 不会失败
	}
	return encodePEM("CERTIFICATE", der), encodePEM("EC PRIVATE KEY", keyDER), nil
}

func encodePEM(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
