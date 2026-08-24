package options

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

// TestInsecureServingOptions_Validate 校验：合法地址通过，非法地址报错。
func TestInsecureServingOptions_Validate(t *testing.T) {
	if errs := NewInsecureServingOptions().Validate(); len(errs) != 0 {
		t.Fatalf("default InsecureServingOptions should be valid, got %v", errs)
	}
	bad := &InsecureServingOptions{Addr: "not-an-addr"}
	if errs := bad.Validate(); len(errs) == 0 {
		t.Fatal("expected validation error for invalid addr")
	}
	// :0 动态端口是合法的监听地址，必须校验通过。
	if errs := (&InsecureServingOptions{Addr: ":0"}).Validate(); len(errs) != 0 {
		t.Fatalf(":0 dynamic port should be valid, got %v", errs)
	}
}

// TestGRPCOptions_AddFlags 校验：AddFlags 后 flag 能被解析并写回字段。
// Join("bald-demo","grpc") 生成带点的 "bald-demo.grpc."，AddFlags 拼接字段名，
// 最终 flag 为 --bald-demo.grpc.addr（无双点）。
func TestGRPCOptions_AddFlags(t *testing.T) {
	o := NewGRPCOptions()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	o.AddFlags(fs, Join("bald-demo", "grpc"))
	if err := fs.Parse([]string{"--bald-demo.grpc.addr=:19090"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if o.Addr != ":19090" {
		t.Fatalf("Addr = %q, want :19090", o.Addr)
	}
}

// TestSecureServingOptions_AddFlags 校验：内嵌 TLSOptions 自然展开为 <prefix>.tls.*。
func TestSecureServingOptions_AddFlags(t *testing.T) {
	o := NewSecureServingOptions()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	o.AddFlags(fs, Join("bald-demo", "http"))
	if err := fs.Parse([]string{
		"--bald-demo.http.addr=:18443",
		"--bald-demo.http.tls.enabled=true",
		"--bald-demo.http.tls.cert=abc",
	}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if o.Addr != ":18443" {
		t.Fatalf("Addr = %q, want :18443", o.Addr)
	}
	if !o.Enabled {
		t.Fatal("expected tls enabled = true")
	}
	if o.Cert != "abc" {
		t.Fatalf("Cert = %q, want abc", o.Cert)
	}
}

// genCertPEM 动态生成一份自签名 P-256 证书+私钥的 PEM 字符串（仅用于测试）。
func genCertPEM(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"bald-test"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}

// TestTLSOptions_SmartMode 校验：TLSConfig 能识别原始 PEM 字符串。
func TestTLSOptions_SmartMode(t *testing.T) {
	certPEM, keyPEM := genCertPEM(t)
	o := &TLSOptions{Enabled: true, Cert: certPEM, Key: keyPEM}
	cfg, err := o.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig with raw PEM: %v", err)
	}
	if cfg == nil || len(cfg.Certificates) == 0 {
		t.Fatal("expected at least one certificate to be loaded")
	}
}

// TestTLSOptions_Disabled 校验：未启用时返回 nil。
func TestTLSOptions_Disabled(t *testing.T) {
	cfg, err := (&TLSOptions{}).TLSConfig()
	if err != nil || cfg != nil {
		t.Fatalf("disabled TLS should return nil,nil; got %v,%v", cfg, err)
	}
}

// TestTLSOptions_MutualTLS 校验：CA 配置后开启客户端证书校验。
func TestTLSOptions_MutualTLS(t *testing.T) {
	certPEM, keyPEM := genCertPEM(t)
	o := &TLSOptions{Enabled: true, CA: certPEM, Cert: certPEM, Key: keyPEM}
	cfg, err := o.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig mTLS: %v", err)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", cfg.ClientAuth)
	}
	if cfg.RootCAs == nil || cfg.ClientCAs == nil {
		t.Fatal("expected RootCAs and ClientCAs to be set")
	}
}
