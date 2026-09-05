package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

// deepMerge 返回 src 覆盖 dst 后的新 map（递归合并嵌套 map，叶子值覆盖）。
//
// 结果每层都新建（copy-on-write）：dst/src 的子树不被原地修改，
// 因此已发布的快照（Store.Settings 旧引用）不受后续重合并影响。
// 典型用法（优先级低 → 高）：deepMerge(remote, file) → deepMerge(_, env) → deepMerge(_, flag)。
func deepMerge(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if sub, ok := v.(map[string]any); ok {
			if base, ok := out[k].(map[string]any); ok {
				out[k] = deepMerge(base, sub)
				continue
			}
			out[k] = deepMerge(nil, sub)
			continue
		}
		out[k] = v
	}
	return out
}

// setPath 把值写入点路径（"server.http.addr" → {"server":{"http":{"addr":...}}}）。
// 路径与 bconf.BindFlags 的 flag 名、契约键路径一致。
func setPath(m map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	cur := m
	for i, p := range parts {
		if i == len(parts)-1 {
			cur[p] = value
			return
		}
		sub, ok := cur[p].(map[string]any)
		if !ok {
			sub = map[string]any{}
			cur[p] = sub
		}
		cur = sub
	}
}

// flattenFlags 把 FlagSet 中「用户显式传入」（Changed==true）的 flag 展开为嵌套 map。
//
// 对齐 viper BindPFlags 语义：未传的 flag 不参与合并（零值 flag 压过 env/文件的
// 反直觉行为）。值取 Value.String()（bindflags.go 的 setter 均返回字符串形式），
// 类型规范化交由下游 bconf.UnmarshalMap 的 coerce 处理。
func flattenFlags(fs *pflag.FlagSet) map[string]any {
	if fs == nil {
		return nil
	}
	m := map[string]any{}
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Changed {
			setPath(m, f.Name, f.Value.String())
		}
	})
	if len(m) == 0 {
		return nil
	}
	return m
}

// environMap 枚举环境变量为嵌套 map（NAME_ 前缀语义）。
//
// 规则：BALD_DEMO_SERVER_HTTP_ADDR → 去前缀 → 全小写 → 下划线/连字符一律解释为
// 点路径分隔符 → server.http.addr。即环境变量名按下划线切段映射点路径。
// 注：与 viper 的「查询驱动 + replacer」不同，本实现为枚举驱动；契约字段名均为
// 单词（无下划线），因此切段歧义在契约键上不存在，业务键命名请避免下划线
// （用点路径命名或 yaml/flag 覆盖）。
func environMap(name string) map[string]any {
	// 前缀规范化：连字符转下划线（name "bald-demo" → 前缀 "BALD_DEMO_"），
	// 与 K8s/etcd 环境变量命名习惯一致；负载内的 "_"/"-" 均视为路径分隔。
	prefix := strings.ToUpper(strings.NewReplacer("-", "_").Replace(name)) + "_"
	m := map[string]any{}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, prefix) {
			continue
		}
		path := strings.ToLower(strings.TrimPrefix(k, prefix))
		path = strings.NewReplacer("_", ".", "-", ".").Replace(path)
		if path == "" {
			continue
		}
		setPath(m, path, v)
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// decodeDocument 把原始字节按格式解码为嵌套 map。
// 仅支持 yaml/yml/json：proto-first 框架收敛配置格式（toml/hcl/ini 随 viper 退役）。
func decodeDocument(data []byte, format string) (map[string]any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var m map[string]any
	var err error
	switch strings.ToLower(format) {
	case "yaml", "yml":
		err = yaml.Unmarshal(data, &m)
	case "json":
		err = json.Unmarshal(data, &m)
	default:
		return nil, fmt.Errorf("config: unsupported format %q (only yaml/yml/json)", format)
	}
	if err != nil {
		return nil, fmt.Errorf("config: decode %s document: %w", format, err)
	}
	return m, nil
}

// FormatOf 返回文件扩展名对应的配置格式名（yaml/yml/json）；无法识别时返回空串。
// 供 Registry 装配契约 file 源时推断层格式。
func FormatOf(path string) string {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	switch strings.ToLower(ext) {
	case "yaml":
		return "yaml"
	case "yml":
		return "yml"
	case "json":
		return "json"
	default:
		return ""
	}
}

// deepCopyMap 递归拷贝嵌套 map，产出与源结构完全隔离的副本。
func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out[k] = deepCopyMap(sub)
			continue
		}
		out[k] = v
	}
	return out
}
