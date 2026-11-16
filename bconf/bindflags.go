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
// 与嵌套 message（递归）。
//
// repeated/map 字段注册为隐藏的「指引 flag」：不传则零感知（--help 不出现），
// 误传时 Set 返回带指引的错误（怎么改配置）而非 unknown flag（只说不行）。
// 业务自定义 binder 注册同名 flag 时指引 flag 让位。enum 字段当前契约树
// 上不可达（type 类字段均为 string），暂不处理。
// 生成的 flag 与配置装载的键路径一致，从而接入 config.Load 的 override 层
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
		case fd.IsList() || fd.IsMap():
			// repeated / map 暂不支持自动绑定：注册指引 flag（隐藏、误用时报
			// 「怎么办」）。必须在 MessageKind 判断之前——map 与 repeated
			// message 的 Kind() 均为 MessageKind，落在后面会被静默跳过。
			bindGuideFlag(fs, fd, key)
			continue
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
		default:
			bindScalarFlag(fs, m, fd, key)
		}
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

// bindGuideFlag 为暂不支持自动绑定的字段（repeated/map）注册「指引 flag」：
// flag 名照常注册（pflag 解析不再报 unknown），但 Set 恒返回带指引的错误——
// 用户误以为可用时得到「怎么办」而不只是「不行」。MarkHidden 保证 --help
// 与 flag 列表零噪音；不传则零感知。业务已注册同名 flag（自定义 binder）时
// 让位，避免 "flag redefined" panic。
func bindGuideFlag(fs *pflag.FlagSet, fd protoreflect.FieldDescriptor, key string) {
	if fs.Lookup(key) != nil {
		return // 自定义 binder 优先
	}
	kind := "repeated"
	if fd.IsMap() {
		kind = "map"
	}
	fs.Var(&guideSetter{key: key, kind: kind}, key, usageOf(fd))
	_ = fs.MarkHidden(key)
}

// guideSetter 是指引 flag 的值写入器：任何 Set 都失败并给出路。
type guideSetter struct {
	key  string
	kind string
}

func (s *guideSetter) Set(string) error {
	return fmt.Errorf("%s is a %s field, not flag-bindable; set it via config file or env, or register a custom flag binder", s.key, s.kind)
}
func (s *guideSetter) Type() string   { return "unsupported" }
func (s *guideSetter) String() string { return "" }

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
	// 与 bindDurationFlag 同款两步取型：protoreflect 的 Duration 字段 Value 装箱
	// 后是 protoreflect.Message（具体类型 *impl.messageState），直接断言
	// *durationpb.Duration 恒失败——那会让 bindDurationSetter.String 对任意已设
	// 时长都误报 "0s"。
	if dm, ok := m.Get(fd).Interface().(protoreflect.Message); ok {
		if d, ok := dm.Interface().(*durationpb.Duration); ok {
			return d
		}
	}
	return durationpb.New(0)
}

// --- flag 值写入器：把 flag 解析结果回写到 proto message ---
//
// 七种标量 Kind 共享一个泛型 setter：解析函数、装箱函数、Type 名与 String
// 格式化由 bindScalarFlag 按 Kind 配置（J2 重复税：错误即拒绝、成功即回写
// 这同一不变量不养第二份实现——此前的 7 份复制类型正是 enum/bytes 分支
// 漏注册这类漂移的温床）。Duration 是 message kind，走独立的 bindDurationSetter。

// scalarSetter 把「解析→装箱→回写」的通用流程参数化。
type scalarSetter[T any] struct {
	m      protoreflect.Message
	fd     protoreflect.FieldDescriptor
	parse  func(string) (T, error)
	box    func(T) protoreflect.Value
	typ    string
	format func(protoreflect.Value) string
}

func (s *scalarSetter[T]) Set(val string) error {
	v, err := s.parse(val)
	if err != nil {
		return err
	}
	s.m.Set(s.fd, s.box(v))
	return nil
}
func (s *scalarSetter[T]) Type() string { return s.typ }
func (s *scalarSetter[T]) String() string {
	return s.format(s.m.Get(s.fd))
}

// identityParse 是 string 字段的解析函数：flag 侧字符串无需转换。
func identityParse(s string) (string, error) { return s, nil }

// 各 Kind 的 String() 格式化与旧 setter 逐字节一致（旧实现即
// fmt.Sprintf("%v", m.Get(fd).Bool()/Int()/Uint()/Float())）。
func boolFormat(v protoreflect.Value) string   { return fmt.Sprintf("%v", v.Bool()) }
func intFormat(v protoreflect.Value) string    { return fmt.Sprintf("%v", v.Int()) }
func uintFormat(v protoreflect.Value) string   { return fmt.Sprintf("%v", v.Uint()) }
func floatFormat(v protoreflect.Value) string  { return fmt.Sprintf("%v", v.Float()) }
func stringFormat(v protoreflect.Value) string { return v.String() }

// bindScalarFlag 把单个标量字段注册为 flag，默认值取 message 当前值（已含 proto 注解注入）。
// FloatKind 分支在本仓契约树上不可达（无 float32 字段），单行 //cov:excl 即可
// 整块豁免（块级判定见 tools/coverage-merge/main.go 的 isExcl：block 内任意行
// 携带标记即排除）；配置与其它 Kind 同质——将来出现 float32 契约字段即自动生效，
// 届时注意 String() 经 protoreflect 一律按 float64 宽化显示（与旧 setter 一致）。
func bindScalarFlag(fs *pflag.FlagSet, m protoreflect.Message, fd protoreflect.FieldDescriptor, key string) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		fs.Var(&scalarSetter[string]{m: m, fd: fd, parse: identityParse, box: protoreflect.ValueOfString, typ: "string", format: stringFormat}, key, usageOf(fd))
	case protoreflect.BoolKind:
		fs.Var(&scalarSetter[bool]{m: m, fd: fd, parse: parseBool, box: protoreflect.ValueOfBool, typ: "bool", format: boolFormat}, key, usageOf(fd))
	case protoreflect.Int32Kind:
		fs.Var(&scalarSetter[int32]{m: m, fd: fd, parse: parseInt32, box: protoreflect.ValueOfInt32, typ: "int", format: intFormat}, key, usageOf(fd))
	case protoreflect.Int64Kind:
		fs.Var(&scalarSetter[int64]{m: m, fd: fd, parse: parseInt64, box: protoreflect.ValueOfInt64, typ: "int64", format: intFormat}, key, usageOf(fd))
	case protoreflect.Uint32Kind:
		fs.Var(&scalarSetter[uint32]{m: m, fd: fd, parse: parseUint32, box: protoreflect.ValueOfUint32, typ: "uint", format: uintFormat}, key, usageOf(fd))
	case protoreflect.Uint64Kind:
		fs.Var(&scalarSetter[uint64]{m: m, fd: fd, parse: parseUint64, box: protoreflect.ValueOfUint64, typ: "uint64", format: uintFormat}, key, usageOf(fd))
	case protoreflect.FloatKind:
		fs.Var(&scalarSetter[float32]{m: m, fd: fd, parse: parseFloat32, box: protoreflect.ValueOfFloat32, typ: "float", format: floatFormat}, key, usageOf(fd)) //cov:excl 本仓契约树无 float32 字段
	case protoreflect.DoubleKind:
		fs.Var(&scalarSetter[float64]{m: m, fd: fd, parse: parseFloat64, box: protoreflect.ValueOfFloat64, typ: "float", format: floatFormat}, key, usageOf(fd))
	}
}
