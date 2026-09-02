package conf

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"

	confv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/config/v1"
)

// BindFlags 把一个 proto message 的字段注册为带前缀的命令行 flag。
//
// fs 为目标 FlagSet（通常传 appkit 自有的 FlagSet，或 pflag.CommandLine 用于简单场景），
// 这样 flag 注册作用域可控，避免重复 loadConfig 时污染全局 pflag.CommandLine 造成
// "flag redefined" panic。
//
// prefix 是配置键前缀（不带末尾点，如 "http"），最终 flag 名为 --http.addr、
// 嵌套 message 递归为 --http.tls.enabled。支持标量（string/bool/int32/int64/
// uint32/uint64/float/double）、duration（proto Duration → 形如 "10s" 的字符串）
// 与嵌套 message（递归）。repeated/map/enum 等暂不支持自动绑定，需业务自行处理。
// 生成的 flag 与 viper 的键路径一致，从而接入 config.Load 的 override 层
// （flag > env > 文件 > 远程）。
func BindFlags(fs *pflag.FlagSet, msg proto.Message, prefix string) {
	bindMessageFlags(fs, msg.ProtoReflect(), prefix)
}

// bindMessageFlags 递归地把一个 message 的字段注册为 flag。
func bindMessageFlags(fs *pflag.FlagSet, m protoreflect.Message, prefix string) {
	full := prefix
	if full != "" {
		full += "."
	}
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		key := full + string(fd.Name())
		switch {
		case fd.Kind() == protoreflect.MessageKind:
			// 嵌套 message 递归（如 http.tls）。Duration 单独处理为 string flag。
			if fd.Message().FullName() == "google.protobuf.Duration" {
				bindDurationFlag(fs, m, fd, key)
				continue
			}
			// 嵌套 message 递归。用 Mutable 获取（并在未设置时自动分配）一个可写
			// 子 message，否则对已注册的 flag 调 Set 写回 nil 字段会 nil pointer panic。
			sub := m.Mutable(fd)
			if msg, ok := sub.Interface().(protoreflect.Message); ok {
				bindMessageFlags(fs, msg, key)
			}
		case fd.IsList() || fd.IsMap():
			// repeated / map 暂不支持自动绑定，跳过（业务可用自定义 binder）。
			continue
		default:
			bindScalarFlag(fs, m, fd, key)
		}
	}
}

// bindScalarFlag 把单个标量字段注册为 flag，默认值取 message 当前值（已含 proto 注解注入）。
func bindScalarFlag(fs *pflag.FlagSet, m protoreflect.Message, fd protoreflect.FieldDescriptor, key string) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		fs.Var(&bindStringSetter{m, fd}, key, usageOf(fd))
	case protoreflect.BoolKind:
		fs.Var(&bindBoolSetter{m, fd}, key, usageOf(fd))
	case protoreflect.Int32Kind:
		fs.Var(&bindIntSetter{m, fd}, key, usageOf(fd))
	case protoreflect.Int64Kind:
		fs.Var(&bindInt64Setter{m, fd}, key, usageOf(fd))
	case protoreflect.Uint32Kind:
		fs.Var(&bindUintSetter{m, fd}, key, usageOf(fd))
	case protoreflect.Uint64Kind:
		fs.Var(&bindUint64Setter{m, fd}, key, usageOf(fd))
	case protoreflect.FloatKind:
		fs.Var(&bindFloat32Setter{m, fd}, key, usageOf(fd))
	case protoreflect.DoubleKind:
		fs.Var(&bindFloat64Setter{m, fd}, key, usageOf(fd))
	}
}

// usageOf 返回字段的简短用途说明（取自 proto 注释，运行时不易获取，统一用字段名）。
func usageOf(fd protoreflect.FieldDescriptor) string {
	return fmt.Sprintf("config field %s", fd.Name())
}

// bindDurationFlag 把 proto Duration 字段注册为字符串 flag（形如 "10s"）。
func bindDurationFlag(fs *pflag.FlagSet, m protoreflect.Message, fd protoreflect.FieldDescriptor, key string) {
	v := m.Get(fd)
	if dm, ok := v.Interface().(protoreflect.Message); ok {
		if _, ok := dm.Interface().(*durationpb.Duration); ok {
			fs.Var(&bindDurationSetter{m, fd}, key, usageOf(fd))
		}
	}
}

type bindDurationSetter struct {
	m  protoreflect.Message
	fd protoreflect.FieldDescriptor
}

func (s *bindDurationSetter) Set(val string) error {
	d, err := parseDuration(val)
	if err != nil {
		return err
	}
	s.m.Set(s.fd, protoreflect.ValueOfMessage(d.ProtoReflect()))
	return nil
}
func (s *bindDurationSetter) Type() string   { return "duration" }
func (s *bindDurationSetter) String() string { return durationToString(durationOf(s.m, s.fd)) }

func durationOf(m protoreflect.Message, fd protoreflect.FieldDescriptor) *durationpb.Duration {
	if d, ok := m.Get(fd).Interface().(*durationpb.Duration); ok {
		return d
	}
	return durationpb.New(0)
}

// --- flag 值写入器：把 flag 解析结果回写到 proto message ---

type bindStringSetter struct {
	m  protoreflect.Message
	fd protoreflect.FieldDescriptor
}

func (s *bindStringSetter) Set(val string) error {
	s.m.Set(s.fd, protoreflect.ValueOfString(val))
	return nil
}
func (s *bindStringSetter) Type() string   { return "string" }
func (s *bindStringSetter) String() string { return s.m.Get(s.fd).String() }

type bindBoolSetter struct {
	m  protoreflect.Message
	fd protoreflect.FieldDescriptor
}

func (s *bindBoolSetter) Set(val string) error {
	b, err := parseBool(val)
	if err != nil {
		return err
	}
	s.m.Set(s.fd, protoreflect.ValueOfBool(b))
	return nil
}
func (s *bindBoolSetter) Type() string   { return "bool" }
func (s *bindBoolSetter) String() string { return fmt.Sprintf("%v", s.m.Get(s.fd).Bool()) }

type bindIntSetter struct {
	m  protoreflect.Message
	fd protoreflect.FieldDescriptor
}

func (s *bindIntSetter) Set(val string) error {
	n, err := parseInt32(val)
	if err != nil {
		return err
	}
	s.m.Set(s.fd, protoreflect.ValueOfInt32(n))
	return nil
}
func (s *bindIntSetter) Type() string   { return "int" }
func (s *bindIntSetter) String() string { return fmt.Sprintf("%v", s.m.Get(s.fd).Int()) }

type bindInt64Setter struct {
	m  protoreflect.Message
	fd protoreflect.FieldDescriptor
}

func (s *bindInt64Setter) Set(val string) error {
	n, err := parseInt64(val)
	if err != nil {
		return err
	}
	s.m.Set(s.fd, protoreflect.ValueOfInt64(n))
	return nil
}
func (s *bindInt64Setter) Type() string   { return "int64" }
func (s *bindInt64Setter) String() string { return fmt.Sprintf("%v", s.m.Get(s.fd).Int()) }

type bindUintSetter struct {
	m  protoreflect.Message
	fd protoreflect.FieldDescriptor
}

func (s *bindUintSetter) Set(val string) error {
	n, err := parseUint32(val)
	if err != nil {
		return err
	}
	s.m.Set(s.fd, protoreflect.ValueOfUint32(n))
	return nil
}
func (s *bindUintSetter) Type() string   { return "uint" }
func (s *bindUintSetter) String() string { return fmt.Sprintf("%v", s.m.Get(s.fd).Uint()) }

type bindUint64Setter struct {
	m  protoreflect.Message
	fd protoreflect.FieldDescriptor
}

func (s *bindUint64Setter) Set(val string) error {
	n, err := parseUint64(val)
	if err != nil {
		return err
	}
	s.m.Set(s.fd, protoreflect.ValueOfUint64(n))
	return nil
}
func (s *bindUint64Setter) Type() string   { return "uint64" }
func (s *bindUint64Setter) String() string { return fmt.Sprintf("%v", s.m.Get(s.fd).Uint()) }

type bindFloat32Setter struct {
	m  protoreflect.Message
	fd protoreflect.FieldDescriptor
}

func (s *bindFloat32Setter) Set(val string) error {
	f, err := parseFloat32(val)
	if err != nil {
		return err
	}
	s.m.Set(s.fd, protoreflect.ValueOfFloat32(f))
	return nil
}
func (s *bindFloat32Setter) Type() string   { return "float" }
func (s *bindFloat32Setter) String() string { return fmt.Sprintf("%v", s.m.Get(s.fd).Float()) }

type bindFloat64Setter struct {
	m  protoreflect.Message
	fd protoreflect.FieldDescriptor
}

func (s *bindFloat64Setter) Set(val string) error {
	f, err := parseFloat64(val)
	if err != nil {
		return err
	}
	s.m.Set(s.fd, protoreflect.ValueOfFloat64(f))
	return nil
}
func (s *bindFloat64Setter) Type() string   { return "float" }
func (s *bindFloat64Setter) String() string { return fmt.Sprintf("%v", s.m.Get(s.fd).Float()) }

// --- ResolveTLS：从 options.TLSOptions 迁来的 Smart Mode 实现 ---

// ResolveTLS 根据 proto 的 Tls 配置生成 *tls.Config；未启用时返回 nil。
//
// 采用 "Smart Mode"：Tls.Ca/Cert/Key 字段既可以是文件路径、原始 PEM 内容，
// 也可以是 Base64 编码的 PEM（K8s Secret 常以 Base64 注入）。
func ResolveTLS(cfg *confv1.Tls) (*tls.Config, error) {
	if cfg == nil || !cfg.GetEnabled() {
		return nil, nil
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: cfg.GetSkipVerify(),
		MinVersion:         tls.VersionTLS12,
	}

	var certBytes, keyBytes []byte
	var err error
	if cfg.GetCert() != "" {
		if certBytes, err = loadResource(cfg.GetCert()); err != nil {
			return nil, fmt.Errorf("failed to load cert: %w", err)
		}
	}
	if cfg.GetKey() != "" {
		if keyBytes, err = loadResource(cfg.GetKey()); err != nil {
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

	// root_ca（或弃用别名 ca）：验证对端证书链（服务端校验出站/对端、客户端校验
	// 服务端）。仅设置它**不**强制 mTLS（D10：不再由 ca 非空隐式触发
	// RequireAndVerifyClientCert，那会让只想做链校验的服务拒绝所有普通客户端）。
	rootCA := cfg.GetRootCa()
	if rootCA == "" {
		rootCA = cfg.GetCa() // 弃用别名向后兼容
	}
	if rootCA != "" {
		caBytes, err := loadResource(rootCA)
		if err != nil {
			return nil, fmt.Errorf("failed to load root_ca: %w", err)
		}
		if len(caBytes) > 0 {
			capool := x509.NewCertPool()
			if !capool.AppendCertsFromPEM(caBytes) {
				return nil, fmt.Errorf("failed to append root_ca certs from pem")
			}
			tlsConfig.RootCAs = capool
		}
	}

	// client_ca：mTLS 专用——显式要求并校验客户端证书。
	if clientCA := cfg.GetClientCa(); clientCA != "" {
		caBytes, err := loadResource(clientCA)
		if err != nil {
			return nil, fmt.Errorf("failed to load client_ca: %w", err)
		}
		if len(caBytes) > 0 {
			capool := x509.NewCertPool()
			if !capool.AppendCertsFromPEM(caBytes) {
				return nil, fmt.Errorf("failed to append client_ca certs from pem")
			}
			tlsConfig.ClientCAs = capool
			tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		}
	}

	return tlsConfig, nil
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
	if decoded, err := base64.StdEncoding.DecodeString(input); err == nil {
		if strings.Contains(string(decoded), "-----BEGIN") {
			return decoded, nil
		}
	}
	// 4. 既非文件、原始 PEM，也非合法 Base64 PEM。
	return nil, fmt.Errorf("input is neither a valid file path, nor raw PEM data, nor base64 encoded PEM")
}

// Scheme 根据 TLS 配置返回 URL scheme（https / http）。
func Scheme(cfg *confv1.Tls) string {
	if cfg != nil && cfg.GetEnabled() {
		return "https"
	}
	return "http"
}
