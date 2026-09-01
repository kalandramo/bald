package conf

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	confv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/config/v1"
	"github.com/kalandramo/bald/pkg/config"
)

// TestNewBootstrap_Defaults 验证 proto 注解注入的默认值正确。
//
// 注意：proto 的 (defaults.value) 注解是框架级默认值的唯一真相源（见 P1 路线图）。
// 此处直接以 proto 契约自身声明的值为基准。P1 阶段 2 已废弃 pkg/options，
// proto 是全栈唯一契约，不再有「双真相源」。
func TestNewBootstrap_Defaults(t *testing.T) {
	cfg := NewBootstrap()

	if got := cfg.GetHttp().GetAddr(); got != ":443" {
		t.Errorf("http.addr = %q, want :443", got)
	}
	if got := cfg.GetHttp().GetTimeout().AsDuration(); got != 10*time.Second {
		t.Errorf("http.timeout = %v, want 10s", got)
	}
	if got := cfg.GetGrpc().GetAddr(); got != ":9090" {
		t.Errorf("grpc.addr = %q, want :9090", got)
	}
	if got := cfg.GetGrpc().GetNetwork(); got != "tcp" {
		t.Errorf("grpc.network = %q, want tcp", got)
	}
	if got := cfg.GetLogger().GetLevel(); got != "info" {
		t.Errorf("log.level = %q, want info", got)
	}
	if got := cfg.GetLogger().GetFormat(); got != "console" {
		t.Errorf("log.format = %q, want console", got)
	}
	if got := cfg.GetLogger().GetOutputPaths(); len(got) != 1 || got[0] != "stdout" {
		t.Errorf("log.output-paths = %v, want [stdout]", got)
	}
	if cfg.GetHttp().GetTls().GetEnabled() {
		t.Errorf("http.tls.enabled = true, want false")
	}
}

// TestUnmarshalFromConfigFile 端到端：viper 加载配置文件 → proto → options。
// 这是推荐的接入方式，验证三个包协同工作。
func TestUnmarshalFromConfigFile(t *testing.T) {
	v := viper.New()
	if err := v.MergeConfigMap(map[string]any{
		"http": map[string]any{
			"addr":    ":8080",
			"timeout": "10s",
			"tls": map[string]any{
				"enabled":     false,
				"skip-verify": false,
			},
		},
		"grpc": map[string]any{
			"network": "tcp",
			"addr":    ":9090",
			"timeout": "30s",
		},
		"log": map[string]any{
			"level":        "debug",
			"format":       "json",
			"output-paths": []any{"stdout"},
		},
	}); err != nil {
		t.Fatalf("MergeConfigMap: %v", err)
	}

	cfg := NewBootstrap()
	if err := config.Unmarshal(v, cfg); err != nil {
		t.Fatalf("config.Unmarshal: %v", err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if got := cfg.GetHttp().GetAddr(); got != ":8080" {
		t.Errorf("http.addr = %q, want :8080", got)
	}
	if got := cfg.GetLogger().GetLevel(); got != "debug" {
		t.Errorf("log.level = %q, want debug", got)
	}

	// P1 阶段 2：server 层直接消费 proto，无需适配回 options。
	// 此处仅验证 proto 字段已正确解析，供 server 直接读取。
	if cfg.GetHttp().GetAddr() != ":8080" {
		t.Errorf("proto http.addr = %q, want :8080", cfg.GetHttp().GetAddr())
	}
	// 未启用 TLS 时 scheme 应为 http。
	if got := Scheme(cfg.GetHttp().GetTls()); got != "http" {
		t.Errorf("Scheme() = %q, want http", got)
	}
}

// TestTLSKeyPath 验证 TLS 配置的键路径是 http.tls.*。
//
// 回归防护：旧的 SecureServingOptions 用 mapstructure squash 内嵌 TLSOptions，
// 导致 viper 期望的键是 http.enabled，而配置文件实际写的是 http.tls.enabled，
// 配置被静默忽略。proto 契约确立 http.tls.* 为正确键路径。
func TestTLSKeyPath(t *testing.T) {
	v := viper.New()
	if err := v.MergeConfigMap(map[string]any{
		"http": map[string]any{
			"addr": ":8443",
			"tls": map[string]any{
				"enabled": "true", // env 场景：字符串
				"cert":    "/etc/certs/tls.crt",
				"key":     "/etc/certs/tls.key",
			},
		},
	}); err != nil {
		t.Fatalf("MergeConfigMap: %v", err)
	}

	cfg := NewBootstrap()
	if err := config.Unmarshal(v, cfg); err != nil {
		t.Fatalf("config.Unmarshal: %v", err)
	}

	tls := cfg.GetHttp().GetTls()
	if !tls.GetEnabled() {
		t.Errorf("http.tls.enabled = false, want true")
	}
	if got := tls.GetCert(); got != "/etc/certs/tls.crt" {
		t.Errorf("http.tls.cert = %q", got)
	}

	// P1 阶段 2：server 层直接消费 proto，无需适配回 options。
	// 关键：配置必须真正写进 proto Tls 子消息，而不是保持默认零值。
	if !tls.GetEnabled() {
		t.Errorf("proto tls.enabled = false, want true")
	}
	if tls.GetCert() != "/etc/certs/tls.crt" {
		t.Errorf("proto tls.cert = %q", tls.GetCert())
	}
	if got := Scheme(tls); got != "https" {
		t.Errorf("Scheme() = %q, want https", got)
	}
}

// TestValidate 验证校验逻辑一次返回所有问题。
func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*confv1.Bootstrap)
		wantErr []string
	}{
		{
			name:    "合法配置",
			mutate:  func(*confv1.Bootstrap) {},
			wantErr: nil,
		},
		{
			name:    "非法 http 地址",
			mutate:  func(c *confv1.Bootstrap) { c.Http.Addr = "not-an-addr" },
			wantErr: []string{"http.addr"},
		},
		{
			name:    "非法 grpc 地址",
			mutate:  func(c *confv1.Bootstrap) { c.Grpc.Addr = "999999" },
			wantErr: []string{"grpc.addr"},
		},
		{
			name:    "TLS 缺 key",
			mutate:  func(c *confv1.Bootstrap) { c.Http.Tls.Enabled = true; c.Http.Tls.Cert = "c" },
			wantErr: []string{"http.tls"},
		},
		{
			name:    "非法日志级别",
			mutate:  func(c *confv1.Bootstrap) { c.Logger.Level = "verbose" },
			wantErr: []string{"log.level"},
		},
		{
			name:    "非法日志格式",
			mutate:  func(c *confv1.Bootstrap) { c.Logger.Format = "xml" },
			wantErr: []string{"log.format"},
		},
		{
			name:    "空输出目标",
			mutate:  func(c *confv1.Bootstrap) { c.Logger.OutputPaths = nil },
			wantErr: []string{"log.output-paths"},
		},
		{
			name: "多问题一次返回",
			mutate: func(c *confv1.Bootstrap) {
				c.Http.Addr = "bad"
				c.Logger.Level = "verbose"
			},
			wantErr: []string{"http.addr", "log.level"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewBootstrap()
			tc.mutate(cfg)
			err := Validate(cfg)

			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %v", tc.wantErr)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Validate() = %v, want containing %q", err, want)
				}
			}
		})
	}
}

// TestValidate_Nil 验证 nil 入参被明确拒绝。
func TestValidate_Nil(t *testing.T) {
	if err := Validate(nil); err == nil {
		t.Error("Validate(nil) succeeded, want error")
	}
}

// TestResolveTLS_NilSafe 验证 ResolveTLS 对 nil / 未启用返回 nil，而非 panic。
func TestResolveTLS_NilSafe(t *testing.T) {
	if cfg, err := ResolveTLS(nil); err != nil || cfg != nil {
		t.Errorf("ResolveTLS(nil) = (%v, %v), want (nil, nil)", cfg, err)
	}
	if cfg, err := ResolveTLS(&confv1.Tls{}); err != nil || cfg != nil {
		t.Errorf("ResolveTLS(disabled) = (%v, %v), want (nil, nil)", cfg, err)
	}
}

// TestResolveTLS_SmartMode 验证 TLS Smart Mode：既接受文件路径，也接受原始 PEM / Base64 PEM。
func TestResolveTLS_SmartMode(t *testing.T) {
	// 构造一对临时 PEM 证书（自签名，仅用于解析测试）。
	caPEM, certPEM, keyPEM := genTestPEM(t)

	cases := []struct {
		name string
		tls  *confv1.Tls
		want string // 期望加载的来源标识（用于断言非 nil）
	}{
		{"file-path", &confv1.Tls{Enabled: true, Ca: caPath(t, caPEM), Cert: certPath(t, certPEM), Key: keyPath(t, keyPEM)}, "file"},
		{"raw-pem", &confv1.Tls{Enabled: true, Ca: string(caPEM), Cert: string(certPEM), Key: string(keyPEM)}, "raw"},
		{"base64-pem", &confv1.Tls{Enabled: true, Ca: b64(caPEM), Cert: b64(certPEM), Key: b64(keyPEM)}, "b64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ResolveTLS(tc.tls)
			if err != nil {
				t.Fatalf("ResolveTLS: %v", err)
			}
			if cfg == nil {
				t.Fatal("ResolveTLS returned nil config, want non-nil")
			}
			if len(cfg.Certificates) == 0 {
				t.Error("no certificate loaded")
			}
			if cfg.RootCAs == nil {
				t.Error("no CA pool loaded")
			}
		})
	}
}

// TestBindFlags_RegistersPrefixed 验证 BindFlags 把 proto 字段注册为带前缀 flag，
// 且 flag 值能写回 proto message（供 server 层直接消费）。
func TestBindFlags_RegistersPrefixed(t *testing.T) {
	// 临时重定向 pflag.CommandLine，避免污染全局。
	old := pflag.CommandLine
	defer func() { pflag.CommandLine = old }()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	pflag.CommandLine = fs

	b := NewBootstrap()
	BindFlags(pflag.CommandLine, b.GetHttp(), "http")
	BindFlags(pflag.CommandLine, b.GetGrpc(), "grpc")

	// 验证 flag 已注册（带前缀）。
	if fs.Lookup("http.addr") == nil {
		t.Fatal("flag http.addr not registered")
	}
	if fs.Lookup("http.timeout") == nil {
		t.Fatal("flag http.timeout not registered")
	}
	if fs.Lookup("http.tls.enabled") == nil {
		t.Fatal("flag http.tls.enabled not registered (nested message)")
	}
	if fs.Lookup("grpc.addr") == nil {
		t.Fatal("flag grpc.addr not registered")
	}

	// 设置 flag 值，验证写回 proto message。
	if err := fs.Set("http.addr", ":18080"); err != nil {
		t.Fatalf("fs.Set: %v", err)
	}
	if b.GetHttp().GetAddr() != ":18080" {
		t.Errorf("after Set http.addr, proto = %q, want :18080", b.GetHttp().GetAddr())
	}
	if err := fs.Set("http.tls.enabled", "true"); err != nil {
		t.Fatalf("fs.Set tls.enabled: %v", err)
	}
	if !b.GetHttp().GetTls().GetEnabled() {
		t.Error("after Set http.tls.enabled, proto tls.enabled = false, want true")
	}
}

// --- TLS Smart Mode 测试辅助 ---

// genTestPEM 生成自签名 ECDSA 证书的 PEM（CA / 证书 / 私钥）。
func genTestPEM(t *testing.T) (caPEM, certPEM, keyPEM []byte) {
	t.Helper()
	caPEM, certPEM, keyPEM = mustGenTestPEM(t)
	return caPEM, certPEM, keyPEM
}

func caPath(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.pem")
	writeFile(t, p, b)
	return p
}
func certPath(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cert.pem")
	writeFile(t, p, b)
	return p
}
func keyPath(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "key.pem")
	writeFile(t, p, b)
	return p
}
func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}
func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// mustGenTestPEM 生成一张自签名证书（CA==Leaf，仅用于 TLS 解析测试，非真实信任链）。
func mustGenTestPEM(t *testing.T) (caPEM, certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "bald-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caPEM = certPEM
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	return caPEM, certPEM, keyPEM
}

// TestLogOptions_RotateRoundTrip 验证 proto Logger.rotate 段正确映射到
// pkg/log.Options.Rotate（即 proto 契约与 log.Options 对齐）。
func TestLogOptions_RotateRoundTrip(t *testing.T) {
	// 1. 显式配置 rotate：所有字段都应被搬运。
	l := &confv1.Logger{
		Level:       "debug",
		Format:      "json",
		OutputPaths: []string{"/var/log/bald.log"},
		Rotate: &confv1.RotateOptions{
			Enabled:    true,
			MaxSize:    50,
			MaxBackups: 3,
			MaxAge:     14,
			Compress:   false,
		},
	}
	o := LogOptions(l)
	if !o.Rotate.Enabled {
		t.Fatal("Rotate.Enabled = false, want true")
	}
	if o.Rotate.MaxSize != 50 {
		t.Errorf("Rotate.MaxSize = %d, want 50", o.Rotate.MaxSize)
	}
	if o.Rotate.MaxBackups != 3 {
		t.Errorf("Rotate.MaxBackups = %d, want 3", o.Rotate.MaxBackups)
	}
	if o.Rotate.MaxAge != 14 {
		t.Errorf("Rotate.MaxAge = %d, want 14", o.Rotate.MaxAge)
	}
	if o.Rotate.Compress {
		t.Error("Rotate.Compress = true, want false")
	}
	if o.Level != "debug" || o.Format != "json" {
		t.Errorf("level/format not mapped: %q/%q", o.Level, o.Format)
	}

	// 2. rotate 段存在但未写标量子字段：应回退到 log.NewOptions 的默认值，
	//    避免 proto3 标量零值（MaxSize=0）触发 log.Options.Validate 失败。
	l2 := &confv1.Logger{Rotate: &confv1.RotateOptions{Enabled: true}}
	o2 := LogOptions(l2)
	if !o2.Rotate.Enabled {
		t.Fatal("case2 Rotate.Enabled = false, want true")
	}
	if o2.Rotate.MaxSize != 100 {
		t.Errorf("case2 Rotate.MaxSize = %d, want 100 (zero-value fallback)", o2.Rotate.MaxSize)
	}

	// 3. 无 rotate 段：保留 NewOptions 默认（Enabled=false）。
	o3 := LogOptions(&confv1.Logger{})
	if o3.Rotate.Enabled {
		t.Error("case3 Rotate.Enabled = true, want false")
	}
}
