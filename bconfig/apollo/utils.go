package apollo

import (
	"log"
	"strings"
)

const (
	yaml       = "yaml"
	yml        = "yml"
	json       = "json"
	properties = "properties"
)

// structuredFormats 结构化命名空间后缀集合。
var structuredFormats = map[string]struct{}{
	yaml:       {},
	yml:        {},
	json:       {},
	properties: {},
}

// format 返回命名空间后缀对应的格式名；无法识别或 properties 一律按 json。
func format(ns string) string {
	arr := strings.Split(ns, ".")
	suffix := arr[len(arr)-1]
	if len(arr) <= 1 || suffix == properties {
		return json
	}
	if _, ok := structuredFormats[suffix]; !ok {
		return json
	}
	return suffix
}

// resolve 把扁平 KV 键展开为嵌套 map：app.name = v => map[app][name] = v。
func resolve(key string, value any, target map[string]any) {
	keys := strings.Split(key, ".")
	last := len(keys) - 1
	cursor := target

	for i, k := range keys {
		if i == last {
			cursor[k] = value
			break
		}

		v, ok := cursor[k]
		if !ok {
			deeper := make(map[string]any)
			cursor[k] = deeper
			cursor = deeper
			continue
		}

		// 已存在且非 map：重复键冲突，保留先到者。
		if cursor, ok = v.(map[string]any); !ok {
			log.Printf("apollo: duplicate key: %v", strings.Join(keys[:i+1], "."))
			break
		}
	}
}

// genKey 生成 KV 的展开键：namespace.ext 与 subKey 组合为 namespace.subKey
// （后缀为已知格式时剥离格式段）。
func genKey(ns, sub string) string {
	arr := strings.Split(ns, ".")
	if len(arr) == 1 {
		if ns == "" {
			return sub
		}
		return ns + "." + sub
	}

	suffix := arr[len(arr)-1]
	if _, ok := structuredFormats[suffix]; ok {
		return strings.Join(arr[:len(arr)-1], ".") + "." + sub
	}
	return ns + "." + sub
}

// jsonExtParser 让 agollo 对 json 命名空间返回原始内容（配合 WithOriginalConfig）。
type jsonExtParser struct{}

func (parser jsonExtParser) Parse(configContent any) (map[string]any, error) {
	return map[string]any{"content": configContent}, nil
}

// yamlExtParser 让 agollo 对 yaml/yml 命名空间返回原始内容（配合 WithOriginalConfig）。
type yamlExtParser struct{}

func (parser yamlExtParser) Parse(configContent any) (map[string]any, error) {
	return map[string]any{"content": configContent}, nil
}
