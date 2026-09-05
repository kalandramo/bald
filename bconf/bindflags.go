package bconf

import (
	"fmt"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"
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
			// 嵌套 message 递归（如 server.http）。Duration 单独处理为 string flag。
			if fd.Message().FullName() == "google.protobuf.Duration" {
				bindDurationFlag(fs, m, fd, key)
				continue
			}
			// nil 子消息直接跳过：Mutable 会实例化空子消息，而契约对「开关型」
			// 子消息（如 Server_TLS）采用「非 nil 即启用」语义——空实例会让
			// server 误入 TLS 路径（cert 为空 → 启动失败）。要经 flag 配置
			// 这类子树，先用配置文件/默认值使子消息非 nil。
			if !m.Has(fd) {
				continue
			}
			// 已设置的子 message 用 Mutable 获取可写实例，保证 flag Set 写回生效。
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
