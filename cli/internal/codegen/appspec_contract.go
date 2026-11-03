package codegen

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gopkg.in/yaml.v3"

	bconf "github.com/kalandramo/bald/bconf"
	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// appspec_contract.go 是 Gate2（生成期契约门）与
// 契约配置的发射器：configs/<app>.yaml 的整棵树由 AppSpec.bootstrap 经
// protoreflect 派生——配置键来自字段描述符（fd.Name()，bconf 装载器的键口径），
// 模板与生成器不写任何裸配置键。这是「加一个 bconf 段 = 0 处生成器改动」的
// 机制本体：新段只要出现在 bootstrap 里，就被本遍历带进配置树；键错不了
// （描述符派生），漏不了（段存在性由 Has 判定）。

// contractTree 把 AppSpec 归一化后的 bootstrap 段派生为配置树（map 形态，
// 键 = proto 字段名），并保证 app.name 必有值（缺省取 meta.name）。
func contractTree(as *appspecv1.AppSpec, bs *bootstrapv1.BootstrapConfig) (map[string]any, error) {
	m, err := messageToMap(bs.ProtoReflect())
	if err != nil {
		return nil, err
	}
	tree, ok := m.(map[string]any)
	if !ok || tree == nil {
		tree = map[string]any{}
	}
	appName := as.GetMeta().GetName()
	if appName == "" {
		appName = bs.GetApp().GetName()
	}
	appSeg, _ := tree["app"].(map[string]any)
	if appSeg == nil {
		appSeg = map[string]any{}
		tree["app"] = appSeg
	}
	if name, _ := appSeg["name"].(string); name == "" {
		appSeg["name"] = appName
	}
	return tree, nil
}

// contractYAML 把配置树渲染为 YAML 文本（configs/<app>.yaml 的内容）。
func contractYAML(tree map[string]any) ([]byte, error) {
	out, err := yaml.Marshal(tree)
	if err != nil {
		return nil, fmt.Errorf("marshal contract yaml: %w", err)
	}
	return out, nil
}

// checkContractKeys 是 Gate2 本体：对配置树做未知键校验（复用 bconf 现成
// API，与 bootstrap/config/config.go 的运行期校验同一调用形状）。声明面与
// 生成物过同一道门——键既然由描述符派生，本检查在结构上不可能失败，保留
// 为回归锁（生成器今后任何绕过描述符的键路径都会在此爆红）。
func checkContractKeys(tree map[string]any) error {
	unknown := bconf.FindUnknownKeys(tree, bconf.NewBootstrap())
	if len(unknown) == 0 {
		return nil
	}
	return fmt.Errorf("contract keys unknown to bconf (typo? or declare in AllowBusinessSections): %v", unknown)
}

// messageToMap 递归把 proto 消息派生为配置树。只有「已出现」的字段进树：
// 消息型字段非 nil、标量非零值（proto3 无 presence 的标量 Has=false——
// 零值即契约默认值，不落配置文件）。键 = proto 字段名（非 JSON 驼峰）。
func messageToMap(m protoreflect.Message) (any, error) {
	out := map[string]any{}
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if !m.Has(fd) {
			continue
		}
		if fd.IsMap() {
			entries := map[string]any{}
			var mapErr error
			mv := m.Get(fd).Map()
			mv.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
				vv, err := scalarToConfig(fd, v)
				if err != nil {
					mapErr = fmt.Errorf("field %s: %w", fd.FullName(), err)
					return false
				}
				entries[fmt.Sprint(k.Interface())] = vv
				return true
			})
			if mapErr != nil {
				return nil, mapErr
			}
			out[string(fd.Name())] = entries
			continue
		}
		if fd.IsList() {
			list := m.Get(fd).List()
			items := make([]any, 0, list.Len())
			for j := 0; j < list.Len(); j++ {
				v, err := scalarToConfig(fd, list.Get(j))
				if err != nil {
					return nil, fmt.Errorf("field %s: %w", fd.FullName(), err)
				}
				items = append(items, v)
			}
			out[string(fd.Name())] = items
			continue
		}
		if fd.Kind() == protoreflect.MessageKind {
			// 统一经 scalarToConfig：Duration/Timestamp 转 protojson 字面量
			// （"15s"/RFC3339），其余消息递归为子树。直接在 messageToMap 里递归
			// 会把 Duration 的 {seconds:15} 形状写进配置，而 bconf 装载器要求
			// "15s" 字符串——生成物启动即失败。
			v, err := scalarToConfig(fd, m.Get(fd))
			if err != nil {
				return nil, err
			}
			// 已出现但为空的消息段保持「空对象」形态（YAML `seg: {}`）——
			// 段存在性本身是语义（如 server.http 存在即开通），不能序列化成
			// null（protojson 把 null 当未设置，段会被装载器丢弃）。
			if v == nil {
				out[string(fd.Name())] = map[string]any{}
			} else {
				out[string(fd.Name())] = v
			}
			continue
		}
		v, err := scalarToConfig(fd, m.Get(fd))
		if err != nil {
			return nil, err
		}
		out[string(fd.Name())] = v
	}
	return out, nil
}

// scalarToConfig 把标量（含 well-known 标量语义的 Duration/Timestamp、枚举）
// 转为配置树可序列化的值。消息型值（列表元素为消息时）递归。
func scalarToConfig(fd protoreflect.FieldDescriptor, v protoreflect.Value) (any, error) {
	if fd.Kind() == protoreflect.MessageKind {
		msg := v.Message()
		if !msg.IsValid() {
			return nil, nil
		}
		switch name := msg.Descriptor().FullName(); name {
		case "google.protobuf.Duration":
			d, ok := msg.Interface().(*durationpb.Duration)
			if !ok {
				return nil, fmt.Errorf("field %s: expected *durationpb.Duration, got %T", fd.FullName(), msg.Interface())
			}
			return d.AsDuration().String(), nil // "10s"（viper/protojson 兼容形态）
		case "google.protobuf.Timestamp":
			ts, ok := msg.Interface().(*timestamppb.Timestamp)
			if !ok {
				return nil, fmt.Errorf("field %s: expected *timestamppb.Timestamp, got %T", fd.FullName(), msg.Interface())
			}
			return ts.AsTime().Format(time.RFC3339Nano), nil
		default:
			return messageToMap(msg)
		}
	}
	if fd.Kind() == protoreflect.EnumKind {
		return string(fd.Enum().Values().ByNumber(v.Enum()).Name()), nil // 枚举按名序列化（protojson 口径）
	}
	if fd.Kind() == protoreflect.BytesKind {
		return nil, fmt.Errorf("field %s: bytes is not representable in config yaml", fd.FullName())
	}
	return v.Interface(), nil
}

// contractGate 执行 Gate2 全流程：bootstrap 段 →
// 描述符派生配置树 → 未知键校验 → YAML 渲染 → 回读复检。第 1-2 步管声明面，
// 第 3 步（回读）是回归锁——键由描述符派生时它结构上不可能失败，任何绕过
// 描述符的键路径都会在此爆红。返回 configs/<app>.yaml 的落盘内容。
func contractGate(as *appspecv1.AppSpec, bs *bootstrapv1.BootstrapConfig) ([]byte, error) {
	tree, err := contractTree(as, bs)
	if err != nil {
		return nil, fmt.Errorf("Gate2 (derive contract tree): %w", err)
	}
	if err := checkContractKeys(tree); err != nil {
		return nil, fmt.Errorf("Gate2 (contract keys on AppSpec.bootstrap): %w", err)
	}
	cfgYAML, err := contractYAML(tree)
	if err != nil {
		return nil, err
	}
	var round map[string]any
	if err := yaml.Unmarshal(cfgYAML, &round); err != nil {
		return nil, fmt.Errorf("Gate2 (round-trip generated config): %w", err)
	}
	if err := checkContractKeys(round); err != nil {
		return nil, fmt.Errorf("Gate2 (contract keys on generated config): %w", err)
	}
	return cfgYAML, nil
}
