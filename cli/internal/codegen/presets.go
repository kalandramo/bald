// presets.go 是 gen app 的预置 AppSpec 模板集（onexai create 预设体验的 bald
// 对应物）：`bald gen app --preset webserver` 落一份可过三道门的 starter 声明，
// 用户改 meta.name/module 后再跑 gen app。预设是「声明起点」不是生成物——
// 它们也要过 Gate1/Gate2（presets_test.go 对每个预设实跑 --check 验证），
// 与手写声明同一纪律。
//
// 预设只覆盖 schema 形状已核实的类型（webserver/worker/fullstack）；
// mq/mcp/clitool 预设等 cron/mcp/出站段的契约形状核实后再加——猜测字段
// 的预设会在用户的 Gate2 上红，比没有更糟。
package codegen

import (
	"embed"
	"fmt"
	"slices"
	"strings"
)

//go:embed presets/*.json
var presetFS embed.FS

// PresetNames 返回全部预置模板名（字典序）。
func PresetNames() []string {
	entries, err := presetFS.ReadDir("presets")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	slices.Sort(names)
	return names
}

// presetSpec 读取一份预置声明。
func presetSpec(name string) ([]byte, error) {
	raw, err := presetFS.ReadFile("presets/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("unknown preset %q (available: %s)", name, strings.Join(PresetNames(), ", "))
	}
	return raw, nil
}
