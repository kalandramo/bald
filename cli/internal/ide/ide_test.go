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
// 覆盖公共面 AGENTS.md 与 .mcp.json、各差异面 seed、zcode 项目级合并、
// workbuddy 全局合并、note 打印。
func TestRunInitAllIDEs(t *testing.T) {
	for _, name := range supportedIDEs() {
		spec, ok := ideSpecs[name]
		if !ok {
			t.Fatalf("ideSpecs 缺 %q", name)
		}
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			home := t.TempDir()
			if err := runInit(name, spec, home, false); err != nil {
				t.Fatalf("runInit(%q) 出错: %v", name, err)
			}
			if _, err := os.Stat("AGENTS.md"); err != nil {
				t.Fatalf("AGENTS.md 未生成: %v", err)
			}
			if _, err := os.Stat(".mcp.json"); err != nil {
				t.Fatalf(".mcp.json 未生成: %v", err)
			}
			if name == "zcode" {
				if _, err := os.Stat(filepath.Join(".zcode", "config.json")); err != nil {
					t.Fatalf("zcode config.json 未生成: %v", err)
				}
			}
			if name == "workbuddy" {
				// 全局作用域：bald 条目落在 home 下的 workbuddy 配置里，
				// 形状为 CodeBuddy 同源 schema（type/command/description）。
				raw, err := os.ReadFile(filepath.Join(home, ".workbuddy", "mcp.json"))
				if err != nil {
					t.Fatalf("workbuddy 全局配置未生成: %v", err)
				}
				var doc struct {
					MCPServers map[string]struct {
						Type        string   `json:"type"`
						Command     string   `json:"command"`
						Args        []string `json:"args"`
						Description string   `json:"description"`
					} `json:"mcpServers"`
				}
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatalf("workbuddy 全局配置非法 JSON: %v", err)
				}
				e, ok := doc.MCPServers["bald"]
				if !ok {
					t.Fatal("workbuddy 全局配置缺 bald 条目")
				}
				if e.Type != "stdio" || e.Command != "bald" || e.Description == "" || len(e.Args) == 0 {
					t.Fatalf("bald 条目形状错误: %+v", e)
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

// TestMergeMCPJSONBranches 覆盖顶层形状新建、zcode 形状新建、workbuddy 条目
// 形状（带 type 键但外壳仍是顶层 mcpServers）、内容一致 ok 短路、以及解析
// 失败的错误分支。
func TestMergeMCPJSONBranches(t *testing.T) {
	t.Chdir(t.TempDir())

	// 顶层 .mcp.json：不存在→新建。
	if err := mergeMCPJSON(".mcp.json", shapeTopLevel, mcpEntry); err != nil {
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
	if err := mergeMCPJSON(".mcp.json", shapeTopLevel, mcpEntry); err != nil {
		t.Fatalf("重复合并出错: %v", err)
	}

	// zcode 形状 → 走 mcp.servers 嵌套分支（父目录由 mergeMCPJSON 自建）。
	if err := mergeMCPJSON(filepath.Join(".zcode", "config.json"), shapeZcode, zcodeMCPEntry); err != nil {
		t.Fatalf("zcode 合并出错: %v", err)
	}

	// workbuddy 形状：条目带 type/description，但外壳仍是顶层 mcpServers——
	// 显式 shape 防止键形推断把它错路由到 zcode 嵌套。
	wb := filepath.Join("home", ".workbuddy", "mcp.json")
	if err := mergeMCPJSON(wb, shapeTopLevel, workbuddyMCPEntry); err != nil {
		t.Fatalf("workbuddy 合并出错: %v", err)
	}
	var wbDoc map[string]map[string]map[string]any
	if raw, err := os.ReadFile(wb); err == nil {
		if err := json.Unmarshal(raw, &wbDoc); err != nil {
			t.Fatalf("workbuddy 配置非法 JSON: %v", err)
		}
	}
	e := wbDoc["mcpServers"]["bald"]
	if e == nil || e["type"] != "stdio" || e["command"] != "bald" {
		t.Fatalf("workbuddy bald 条目形状错误: %v", e)
	}
	if _, ok := e["description"]; !ok {
		t.Fatal("workbuddy bald 条目缺 description")
	}
	if _, hasNested := wbDoc["mcp"]; hasNested {
		t.Fatal("workbuddy 条目被错误路由到 zcode 嵌套形状")
	}

	// 非法 JSON → 解析错误分支。
	bad := "bad.json"
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeMCPJSON(bad, shapeTopLevel, mcpEntry); err == nil {
		t.Fatal("期望非法 JSON 报错")
	}
}

// TestRunInitMergeErrors 覆盖 runInit 把 mergeMCPJSON 解析错误透传的分支：
// 通用 .mcp.json、zcode 专有 config.json 与 workbuddy 全局配置各写坏一份。
func TestRunInitMergeErrors(t *testing.T) {
	t.Run("generic", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.WriteFile(".mcp.json", []byte("{bad"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := runInit("claude", ideSpecs["claude"], t.TempDir(), false); err == nil {
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
		if err := runInit("zcode", ideSpecs["zcode"], t.TempDir(), false); err == nil {
			t.Fatal("期望 zcode config 解析错误透传")
		}
	})
	t.Run("workbuddy", func(t *testing.T) {
		t.Chdir(t.TempDir())
		home := t.TempDir()
		wb := filepath.Join(home, ".workbuddy")
		if err := os.MkdirAll(wb, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wb, "mcp.json"), []byte("{bad"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := runInit("workbuddy", ideSpecs["workbuddy"], home, false); err == nil {
			t.Fatal("期望 workbuddy 全局配置解析错误透传")
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
