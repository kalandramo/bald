// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package ide

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSupportedIDEs(t *testing.T) {
	if len(supportedIDEs()) == 0 {
		t.Fatal("supportedIDEs 为空")
	}
}

func TestNewCommandHasInit(t *testing.T) {
	cmd := NewCommand()
	if cmd == nil {
		t.Fatal("NewCommand 返回 nil")
	}
	var found bool
	for _, c := range cmd.Commands() {
		if c.Name() == "init" {
			found = true
		}
	}
	if !found {
		t.Fatal("ide 命令树缺少 init 子命令")
	}
}

// TestRunInitAllIDEs 在隔离临时目录里对每个 --ide 取值跑 runInit（非 force），
// 覆盖公共面 AGENTS.md、各差异面 seed、通用 .mcp.json 合并、zcode 额外合并、note 打印。
func TestRunInitAllIDEs(t *testing.T) {
	for _, name := range supportedIDEs() {
		spec, ok := ideSpecs[name]
		if !ok {
			t.Fatalf("ideSpecs 缺 %q", name)
		}
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := runInit(name, spec, false); err != nil {
				t.Fatalf("runInit(%q) 出错: %v", name, err)
			}
			if _, err := os.Stat("AGENTS.md"); err != nil {
				t.Fatalf("AGENTS.md 未生成: %v", err)
			}
			if name == "zcode" {
				if _, err := os.Stat(filepath.Join(".zcode", "config.json")); err != nil {
					t.Fatalf("zcode config.json 未生成: %v", err)
				}
			} else if name != "workbuddy" {
				if _, err := os.Stat(".mcp.json"); err != nil {
					t.Fatalf(".mcp.json 未生成: %v", err)
				}
			}
		})
	}
}

// TestSeedKeptAndForce 覆盖 seed 的三分支：不存在→写、存在未 force→kept、
// 存在 + force→覆盖。
func TestSeedKeptAndForce(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := seed("AGENTS.md", "templates/agents.md", "x", false); err != nil {
		t.Fatalf("首次 seed 出错: %v", err)
	}
	before, _ := os.ReadFile("AGENTS.md")
	// 已存在、未开 force → kept，内容不变。
	if err := seed("AGENTS.md", "templates/agents.md", "x", false); err != nil {
		t.Fatalf("kept 分支出错: %v", err)
	}
	after, _ := os.ReadFile("AGENTS.md")
	if string(before) != string(after) {
		t.Fatal("kept 分支不应改动文件内容")
	}
	// force → 覆盖写。
	if err := seed("AGENTS.md", "templates/agents.md", "x", true); err != nil {
		t.Fatalf("force 分支出错: %v", err)
	}
}

// TestMergeMCPJSONBranches 覆盖通用形状新建、zcode 形状新建、内容一致 ok 短路、
// 以及解析失败的错误分支。
func TestMergeMCPJSONBranches(t *testing.T) {
	t.Chdir(t.TempDir())

	// 通用 .mcp.json：不存在→新建。
	if err := mergeMCPJSON(".mcp.json", mcpEntry); err != nil {
		t.Fatalf("通用新建出错: %v", err)
	}
	var doc map[string]any
	if raw, err := os.ReadFile(".mcp.json"); err == nil {
		_ = json.Unmarshal(raw, &doc)
	}
	if _, ok := doc["mcpServers"]; !ok {
		t.Fatal("通用合并未写入 mcpServers")
	}
	// 内容一致 → ok 短路。
	if err := mergeMCPJSON(".mcp.json", mcpEntry); err != nil {
		t.Fatalf("重复合并出错: %v", err)
	}

	// zcode 形状（带 type 键）→ 走 mcp.servers 嵌套分支。mergeMCPJSON 不建目录
	// （runInit 里目录由前置 seed 步创建），故先补 .zcode。
	if err := os.MkdirAll(".zcode", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mergeMCPJSON(filepath.Join(".zcode", "config.json"), zcodeMCPEntry); err != nil {
		t.Fatalf("zcode 合并出错: %v", err)
	}

	// 非法 JSON → 解析错误分支。
	bad := "bad.json"
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeMCPJSON(bad, mcpEntry); err == nil {
		t.Fatal("期望非法 JSON 报错")
	}
}

// TestRunInitMergeErrors 覆盖 runInit 把 mergeMCPJSON 解析错误透传的两条分支：
// 通用 .mcp.json 与 zcode 专有 config.json 各写坏一份。
func TestRunInitMergeErrors(t *testing.T) {
	t.Run("generic", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.WriteFile(".mcp.json", []byte("{bad"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := runInit("claude", ideSpecs["claude"], false); err == nil {
			t.Fatal("期望 .mcp.json 解析错误透传")
		}
	})
	t.Run("zcode", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.MkdirAll(".zcode", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(".zcode", "config.json"), []byte("{bad"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := runInit("zcode", ideSpecs["zcode"], false); err == nil {
			t.Fatal("期望 zcode config 解析错误透传")
		}
	})
}

// TestInitCmdRunE 经命令入口覆盖 initCmd.RunE 的两个分支：不支持的 --ide 报错、
// 合法 --ide（在隔离临时目录）走成功返回。
func TestInitCmdRunE(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		cmd := NewCommand()
		cmd.SetArgs([]string{"init", "--ide", "nonexistent"})
		if err := cmd.Execute(); err == nil {
			t.Fatal("期望不支持的 --ide 返回错误")
		}
	})
	t.Run("supported", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cmd := NewCommand()
		cmd.SetArgs([]string{"init", "--ide", "claude"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("合法 --ide 期望成功，实际: %v", err)
		}
		if _, err := os.Stat("AGENTS.md"); err != nil {
			t.Fatalf("AGENTS.md 未生成: %v", err)
		}
	})
}
