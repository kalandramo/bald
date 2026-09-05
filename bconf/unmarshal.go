package bconf

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// durationName 与 durationFullName 用于识别 google.protobuf.Duration 字段。
const (
	durationName     = "Duration"
	durationFullName = "google.protobuf.Duration"
)

// UnmarshalMap 把合并后的配置树（map 形态，键为 proto 字段名）反序列化为
// Protobuf 消息。这是「proto 作为配置契约」的桥接点：配置的形状由 proto 定义，
// 由此获得编译期可查的字段名与类型，而不是依赖 mapstructure tag 的字符串匹配
// （写错键名会静默落到零值）。
//
// 流程：map → json.Marshal → 类型规范化 → protojson.Unmarshal。
//
// 用法：
//
//	cfg := bconf.NewBootstrap()              // 先填默认值
//	if err := bconf.UnmarshalMap(settings, cfg); err != nil { ... }
//
// 加载器（viper/config module、bconfig KV 源、测试桩）只需先合并出 map 即可。
//
// 语义是「合并」而非「替换」：msg 中未被配置覆盖的字段保留原值（通常是
// bconf.NewBootstrap() 填入的默认值）。其中 repeated 字段表现为「替换」
// （配置中出现的列表整体覆盖默认值，而非追加，见 clearPresentLists）。
//
// 注意：proto3 的标量字段没有 presence，无法区分「未配置」与「显式配置为零值」。
// 因此若某字段的默认值是非零值（如 http.addr 默认 ":8080"），配置里显式写
// 空字符串并不会覆盖它——这是 proto3 的固有行为，不是本函数的缺陷。确实需要
// 三态语义时，请在 proto 中用 optional 关键字启用 field presence。
func UnmarshalMap(m map[string]any, msg proto.Message) error {
	if msg == nil {
		return fmt.Errorf("bconf: proto message is nil")
	}

	settings := m

	// 关键：先按 msg 的字段描述符做类型规范化，再交给 protojson。
	// 原因是 env（AutomaticEnv）与 flag（pflag 的 *Var 系列）的值在 viper 里
	// 可能是字符串或整数，而 protojson 是严格模式——"8080" 不会自动转成 int32，
	// 纳秒整数也不会自动转成 Duration 的 "10s" 字符串，直接反序列化会报错。
	if err := coerceMessage(msg.ProtoReflect(), settings); err != nil {
		return fmt.Errorf("bconf: coerce: %w", err)
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("bconf: marshal settings: %w", err)
	}

	// 先解析到同类型的空消息，再合并进 msg。
	// 不能直接用 protojson.Unmarshal(data, msg)：它的语义是「替换」，会先重置
	// 整个 msg，把调用方预设的默认值（conf.NewBootstrap()）一并清掉。
	parsed := msg.ProtoReflect().New().Interface()

	// DiscardUnknown：配置文件里允许出现 proto 未声明的业务配置段，
	// 不影响框架级配置的解析（proto 只约束框架级配置，见设计文档）。
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := opts.Unmarshal(data, parsed); err != nil {
		return fmt.Errorf("bconf: unmarshal into %s: %w", msg.ProtoReflect().Descriptor().FullName(), err)
	}

	// 合并前先清掉「配置中显式出现过的 list 字段」在 msg 中的旧值。
	// 原因：proto.Merge 对 repeated 字段是 append 而非替换，若不清理，
	// 默认值 ["stdout"] 与配置值 ["stdout"] 会合并成 ["stdout", "stdout"]——
	// 表现为日志向 stdout 写两遍。标量字段无此问题（后值覆盖前值）。
	clearPresentLists(msg.ProtoReflect(), parsed.ProtoReflect())

	proto.Merge(msg, parsed)
	return nil
}

// clearPresentLists 对 src 中显式出现（长度 > 0）的 repeated 字段，清空 dst 的同名字段，
// 使随后的 proto.Merge 表现为「替换」而非「追加」。
//
// src 中未出现的 list 字段不动，其默认值得以保留。嵌套 message 递归处理。
func clearPresentLists(dst, src protoreflect.Message) {
	fields := src.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if !src.Has(fd) {
			continue // 配置中未出现，保留 dst 的默认值
		}
		switch {
		case fd.IsList():
			dst.Clear(fd)
		case fd.Kind() == protoreflect.MessageKind:
			// 递归处理嵌套消息（如 http.tls 下的 list 字段）。
			// Mutable 会确保 dst 侧存在该子消息，缺失时创建。
			clearPresentLists(dst.Mutable(fd).Message(), src.Get(fd).Message())
		}
	}
}

// coerceMessage 按 msg 的字段描述符，把 map 中的值转换成 protojson 能接受的类型。
// 只处理 map 中已存在的 key，缺失 key 不动（交给 proto 的零值/默认值体系）。
func coerceMessage(msg protoreflect.Message, m map[string]any) error {
	fields := msg.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)

		// protojson 同时接受 json_name（camelCase）与 proto 原名（snake_case）。
		raw, ok := lookupField(m, string(fd.Name()), fd.JSONName())
		if !ok || raw == nil {
			continue
		}

		coerced, err := coerceValue(fd, raw)
		if err != nil {
			return fmt.Errorf("field %q: %w", fd.Name(), err)
		}

		// 规范化后的值写回规范键（JSON name），protojson 一定能识别。
		delete(m, string(fd.Name()))
		m[fd.JSONName()] = coerced
	}
	return nil
}

// lookupField 按候选键名在 map 中查找（不区分 "-" 与 "_" 的差异）。
func lookupField(m map[string]any, names ...string) (any, bool) {
	for _, n := range names {
		if v, ok := m[n]; ok {
			return v, true
		}
	}
	// 兼容 snake_case 与 kebab-case（配置文件里两种都常见，如 output-paths）。
	for key, v := range m {
		for _, n := range names {
			if strings.EqualFold(strings.ReplaceAll(key, "-", "_"), strings.ReplaceAll(n, "-", "_")) {
				return v, true
			}
		}
	}
	return nil, false
}

// coerceValue 按字段描述符把单个值转换成 protojson 可接受的类型。
func coerceValue(fd protoreflect.FieldDescriptor, raw any) (any, error) {
	// repeated 字段：逐元素转换。
	if fd.IsList() && !fd.IsMap() {
		items, ok := toSlice(raw)
		if !ok {
			// 标量单值也接受（如 output-paths: stdout），包成单元素切片。
			items = []any{raw}
		}
		out := make([]any, 0, len(items))
		for i, it := range items {
			v, err := coerceScalar(fd, it)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out = append(out, v)
		}
		return out, nil
	}

	// map 字段：只转换 value。
	if fd.IsMap() {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expect map, got %T", raw)
		}
		out := make(map[string]any, len(m))
		for k, v := range m {
			cv, err := coerceScalar(fd.MapValue(), v)
			if err != nil {
				return nil, fmt.Errorf("map value %q: %w", k, err)
			}
			out[k] = cv
		}
		return out, nil
	}

	// 消息字段：Duration 需要特殊处理，其余递归。
	if fd.Kind() == protoreflect.MessageKind {
		if isDuration(fd.Message()) {
			return coerceDuration(raw)
		}
		sub, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expect object for message field, got %T", raw)
		}
		// 用动态消息拿描述符即可，coerceMessage 只需要字段描述符做类型判断。
		if err := coerceMessage(dynamicpb.NewMessage(fd.Message()), sub); err != nil {
			return nil, err
		}
		return sub, nil
	}

	return coerceScalar(fd, raw)
}

// coerceScalar 转换标量：解决 env / flag 值与 proto 类型不匹配的问题。
func coerceScalar(fd protoreflect.FieldDescriptor, raw any) (any, error) {
	if fd.Kind() == protoreflect.EnumKind {
		return coerceEnum(fd, raw)
	}

	// 已经是正确类型（来自本地 yaml/json 文件）则原样返回。
	switch fd.Kind() {
	case protoreflect.StringKind:
		// 数字/布尔转字符串，兼容 flag 与 env 的写法。
		switch v := raw.(type) {
		case string:
			return v, nil
		case bool:
			return strconv.FormatBool(v), nil
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return fmt.Sprint(v), nil
		case float32, float64:
			return strconv.FormatFloat(toFloat(v), 'f', -1, 64), nil
		}
		return nil, fmt.Errorf("expect string, got %T", raw)

	case protoreflect.BoolKind:
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("expect bool, got %q", v)
			}
			return b, nil
		}
		return nil, fmt.Errorf("expect bool, got %T", raw)

	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return coerceInt(raw, fd.Kind())

	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return coerceUint(raw)

	case protoreflect.FloatKind, protoreflect.DoubleKind:
		switch v := raw.(type) {
		case float32, float64:
			return toFloat(v), nil
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			n, err := anyToInt64(v)
			if err != nil {
				return nil, err
			}
			return float64(n), nil
		case string:
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("expect number, got %q", v)
			}
			return f, nil
		}
		return nil, fmt.Errorf("expect number, got %T", raw)

	case protoreflect.BytesKind:
		if s, ok := raw.(string); ok {
			return s, nil // protojson 期望 base64 字符串
		}
		return nil, fmt.Errorf("expect base64 string for bytes, got %T", raw)
	}

	return raw, nil
}

// coerceInt 转换成有符号整数。字符串（env 常见）与 Duration 都支持。
func coerceInt(raw any, kind protoreflect.Kind) (any, error) {
	bits := 64
	if kind == protoreflect.Int32Kind || kind == protoreflect.Sint32Kind || kind == protoreflect.Sfixed32Kind {
		bits = 32
	}
	switch v := raw.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		n, err := anyToInt64(v)
		if err != nil {
			return nil, err
		}
		if bits == 32 && (n > math.MaxInt32 || n < math.MinInt32) {
			return nil, fmt.Errorf("value %d overflows int32", n)
		}
		return n, nil
	case float32, float64:
		f := toFloat(v)
		if f != math.Trunc(f) {
			return nil, fmt.Errorf("expect integer, got %v", f)
		}
		return int64(f), nil
	case string:
		n, err := strconv.ParseInt(v, 10, bits)
		if err != nil {
			return nil, fmt.Errorf("expect integer, got %q", v)
		}
		return n, nil
	case time.Duration:
		return int64(v), nil
	}
	return nil, fmt.Errorf("expect integer, got %T", raw)
}

// coerceUint 转换成无符号整数。
func coerceUint(raw any) (any, error) {
	switch v := raw.(type) {
	case uint, uint8, uint16, uint32, uint64:
		return v, nil
	case int, int8, int16, int32, int64:
		n, err := anyToInt64(v)
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, fmt.Errorf("expect unsigned integer, got %d", n)
		}
		return uint64(n), nil
	case string:
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expect unsigned integer, got %q", v)
		}
		return n, nil
	}
	return nil, fmt.Errorf("expect unsigned integer, got %T", raw)
}

// coerceEnum 转换枚举：接受枚举值名（"json"）与数字（0）。
func coerceEnum(fd protoreflect.FieldDescriptor, raw any) (any, error) {
	values := fd.Enum().Values()
	switch v := raw.(type) {
	case string:
		// 已是合法枚举名则直接返回（大小写不敏感由 protojson 处理）。
		if values.ByName(protoreflect.Name(v)) != nil {
			return v, nil
		}
		// 数字字符串按编号解析。
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			if values.ByNumber(protoreflect.EnumNumber(n)) != nil {
				return n, nil
			}
		}
		names := make([]string, 0, values.Len())
		for i := 0; i < values.Len(); i++ {
			names = append(names, string(values.Get(i).Name()))
		}
		return nil, fmt.Errorf("invalid enum value %q, want one of %v", v, names)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		n, err := anyToInt64(v)
		if err != nil {
			return nil, err
		}
		if values.ByNumber(protoreflect.EnumNumber(n)) != nil {
			return n, nil
		}
		return nil, fmt.Errorf("invalid enum number %d", n)
	}
	return nil, fmt.Errorf("expect enum name or number, got %T", raw)
}

// coerceDuration 把配置值转成 protojson 能接受的 Duration 字符串。
//
// 三种来源形态：
//  1. 本地 yaml/json 文件："10s"、"1.5s" —— 已是合法 duration 串，原样返回；
//  2. env（AutomaticEnv）：字符串 "10s" —— 同上；
//  3. flag（pflag.DurationVar）：time.Duration 或纳秒整数 —— 需格式化成 "<seconds>s"。
//
// 注意不能用 time.Duration.String()：它会产生 "1m30s" 这类复合表示，
// 而 protojson 的 Duration 只接受以 s 结尾的十进制秒数（如 "90s"）。
func coerceDuration(raw any) (any, error) {
	switch v := raw.(type) {
	case string:
		// 校验是否为合法 duration 串，提前暴露错误而不是等到 protojson。
		if _, err := time.ParseDuration(v); err != nil {
			return nil, fmt.Errorf("invalid duration %q: %w", v, err)
		}
		return v, nil
	case time.Duration:
		return formatDuration(v), nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		n, err := anyToInt64(v)
		if err != nil {
			return nil, err
		}
		return formatDuration(time.Duration(n)), nil
	case float32, float64:
		return formatDuration(time.Duration(toFloat(v))), nil
	}
	return nil, fmt.Errorf("expect duration (e.g. \"10s\"), got %T", raw)
}

// formatDuration 把 time.Duration 格式化为 protojson Duration 串（十进制秒 + "s"）。
func formatDuration(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}

// isDuration 判断消息描述符是否为 google.protobuf.Duration。
func isDuration(md protoreflect.MessageDescriptor) bool {
	return md != nil && (md.FullName() == durationFullName ||
		(md.Name() == durationName && md.ParentFile().Package() == "google.protobuf"))
}

// toSlice 尝试把值转成 []any，支持反射之外的常见容器类型。
func toSlice(raw any) ([]any, bool) {
	switch v := raw.(type) {
	case []any:
		return v, true
	case []string:
		out := make([]any, 0, len(v))
		for _, s := range v {
			out = append(out, s)
		}
		return out, true
	case []int:
		out := make([]any, 0, len(v))
		for _, n := range v {
			out = append(out, n)
		}
		return out, true
	}
	return nil, false
}

// anyToInt64 把任意整数类型转为 int64。
func anyToInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int8:
		return int64(n), nil
	case int16:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint:
		return int64(n), nil
	case uint8:
		return int64(n), nil
	case uint16:
		return int64(n), nil
	case uint32:
		return int64(n), nil
	case uint64:
		return int64(n), nil
	}
	return 0, fmt.Errorf("not an integer: %T", v)
}

// toFloat 把 float32/float64 归一为 float64。
func toFloat(v any) float64 {
	switch f := v.(type) {
	case float32:
		return float64(f)
	case float64:
		return f
	}
	return 0
}
