package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTree_LineBudgets 把参考夹具的目标目录树逐文件与行数预算对账：
// main.go ≤25 行为硬断言；其余以 t.Log 输出实测表供验收记录引用。
// 装配物（assemble.gen.go + bootstrap/domains.gen.go）的行数不随域/能力数量
// 线性增长——新增 bconf 段/域时重跑本测试观察增量。
func TestTree_LineBudgets(t *testing.T) {
	dir := t.TempDir()
	if _, err := genFixtureTree(t, dir, false); err != nil {
		t.Fatalf("gen: %v", err)
	}
	type row struct {
		path string
		buy  string // 行数预算口径
	}
	rows := []row{
		{filepath.Join("cmd", "demoapp", "main.go"), "≤25"},
		{filepath.Join("cmd", "demoapp", "app", "command.go"), "~35"}, // appcli 单轨后机制层外移
		{filepath.Join("cmd", "demoapp", "app", "custom.go"), "seed-once"},
		{filepath.Join("internal", "bootstrap", "assemble.gen.go"), "~110"},
		{filepath.Join("internal", "bootstrap", "domains.gen.go"), "~55"},
		{filepath.Join("internal", "bootstrap", "custom.go"), "seed-once"},
		{filepath.Join("internal", "bootstrap", "seed", "seed.go"), "seed-once"},
		{filepath.Join("internal", "bootstrap", "seed", "policies.go"), "seed-once"},
		{filepath.Join("internal", "apiserver", "apiserver.go"), "~40"},
		{filepath.Join("internal", "apiserver", "layering_invariant_test.go"), "~60"},
		{filepath.Join("api", "buf.yaml"), "seed-once"},
		{filepath.Join("api", "buf.gen.yaml"), "seed-once"},
		{filepath.Join("configs", "demoapp.yaml"), "~40"},
		{"taskfile.yml", "~35"},
	}
	for _, r := range rows {
		data, err := os.ReadFile(filepath.Join(dir, r.path))
		if err != nil {
			t.Errorf("missing %s: %v", r.path, err)
			continue
		}
		lines := strings.Count(strings.TrimRight(string(data), "\n"), "\n") + 1
		t.Logf("%-52s %3d 行（预算 %s）", r.path, lines, r.buy)
	}
	// 硬断言：入口文件行数预算。
	data, err := os.ReadFile(filepath.Join(dir, "cmd", "demoapp", "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if lines := strings.Count(strings.TrimRight(string(data), "\n"), "\n") + 1; lines > 25 {
		t.Errorf("main.go = %d lines, budget 25", lines)
	}
}
