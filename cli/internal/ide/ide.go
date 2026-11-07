// Package ide 是 `bald ide init`：为 AI 会话生成 bald 的引导物，让 AI 知道
// 去哪里查规范（onexai ide init 的 bald 对应物）。
//
// 生成的文件分两类归属（与 codegen 的归属三态同思想）：
//   - Seeded 模板（不存在才写，已存在 kept: 不覆盖，--force 放行）：
//     AGENTS.md、.claude/commands/bald.md、.claude/skills/bald-conventions/SKILL.md
//     ——消费者项目的会话引导物，生成一次后归项目维护。
//   - 合并写入（读现有 JSON，只补 mcpServers.bald 键，其余原样保留）：
//     .mcp.json（Claude Code 项目级 MCP 配置）。
//
// Phase 1 只做 claude（手动 stdio 配置——cobramcp 不管理 Claude 系配置）；
// VSCode/Cursor 用 cobramcp 现成的 `bald mcp vscode|cursor enable`。
package ide

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

//go:embed templates
var tmplFS embed.FS

// mcpEntry 是 .mcp.json 里 bald server 的配置段（stdio 传输）。
var mcpEntry = map[string]any{
	"command": "bald",
	"args":    []string{"mcp", "start"},
}

// seededFile 是一份 Seeded 模板：目标路径 + 内嵌模板路径。
type seededFile struct {
	path    string // 相对项目根
	tmpl    string // templates/ 内路径
	comment string // kept/generated 提示的补充说明
}

// seededFiles 是 ide init 的落盘清单（顺序即输出顺序）。
var seededFiles = []seededFile{
	{path: "AGENTS.md", tmpl: "templates/agents.md", comment: "会话引导：先查规范 + 最小强制集"},
	{path: filepath.Join(".claude", "commands", "bald.md"), tmpl: "templates/command_bald.md", comment: "/bald 斜杠命令"},
	{path: filepath.Join(".claude", "skills", "bald-conventions", "SKILL.md"), tmpl: "templates/skill.md", comment: "规范查询 SKILL（模型自判调用）"},
}

// NewCommand 构造 `bald ide` 命令树（当前只有 init 子命令）。
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ide",
		Short: "AI 会话引导物管理",
		Long: `初始化与维护 AI 编码会话对 bald 规范通道的接入。

目标是让 AI 知道去哪里查规范：bald ide init 生成会话引导物，bald convention
提供被查询的数据面（overview/get/check），bald mcp 把查询面暴露为 MCP 工具。`,
		SilenceUsage: true,
	}
	cmd.AddCommand(initCmd())
	return cmd
}

func initCmd() *cobra.Command {
	o := &struct {
		IDE   string
		Force bool
	}{IDE: "claude"}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "初始化 AI 会话引导物（AGENTS.md / .mcp.json / .claude）",
		Long: `在当前目录（业务项目根）生成 bald 的 AI 会话引导物：

  AGENTS.md                                  先查规范 + 最小强制集（Seeded）
  .mcp.json                                  合并 mcpServers.bald（bald mcp start）
  .claude/commands/bald.md                   /bald 斜杠命令（Seeded）
  .claude/skills/bald-conventions/SKILL.md   规范查询 SKILL（Seeded）

Seeded 文件不存在才写，已存在输出 kept: 不覆盖（--force 放行）；.mcp.json
是合并写入，现有键原样保留。目标一致：让 AI 知道去哪里查规范。

VSCode/Cursor 的 MCP 配置用 cobramcp 的现成命令：
bald mcp vscode enable / bald mcp cursor enable。`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if o.IDE != "claude" {
				return fmt.Errorf("--ide %q not supported yet (claude only; vscode/cursor use `bald mcp vscode|cursor enable`)", o.IDE)
			}
			return runInit(o.Force)
		},
	}
	cmd.Flags().StringVar(&o.IDE, "ide", "claude", "AI 编码工具（当前支持 claude）")
	cmd.Flags().BoolVar(&o.Force, "force", false, "覆盖已存在的 Seeded 文件（默认保留）")
	return cmd
}

// runInit 落盘全部引导物。任一步失败即返回错误（已写文件保留——各文件独立可用）。
func runInit(force bool) error {
	for _, f := range seededFiles {
		content, err := fs.ReadFile(tmplFS, f.tmpl)
		if err != nil {
			return fmt.Errorf("read template %s: %w", f.tmpl, err)
		}
		switch written, err := seedFile(f.path, content, force); {
		case err != nil:
			return err
		case written:
			fmt.Println("generated:", f.path, "("+f.comment+")")
		default:
			fmt.Println("kept:", f.path)
		}
	}
	if err := mergeMCPJSON(); err != nil {
		return err
	}
	fmt.Println("next: restart your AI session so the new config takes effect; query conventions with `bald convention overview`")
	return nil
}

// seedFile 写一份 Seeded 文件：不存在写（true）；存在且未开 --force 则保留（false）；
// --force 覆盖（true）。
func seedFile(path string, content []byte, force bool) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		if !force {
			return false, nil
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// mergeMCPJSON 把 mcpServers.bald 合并进 .mcp.json：文件不存在则新建；存在则
// 解析、只补这一个键、原样写回（缩进统一两空格）。内容未变时输出 ok: 不落盘。
func mergeMCPJSON() error {
	const path = ".mcp.json"
	root := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &root); err != nil {
			return fmt.Errorf("parse %s: %w (fix or remove it, then rerun)", path, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		root["mcpServers"] = servers
	}
	if cur, ok := servers["bald"].(map[string]any); ok && fmt.Sprint(cur) == fmt.Sprint(mcpEntry) {
		fmt.Println("ok:", path, "(mcpServers.bald already present)")
		return nil
	}
	changed := false
	if _, exists := servers["bald"]; !exists {
		changed = true
	}
	servers["bald"] = mcpEntry
	raw, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	if changed {
		fmt.Println("updated:", path, "(added mcpServers.bald)")
	} else {
		fmt.Println("updated:", path, "(refreshed mcpServers.bald)")
	}
	return nil
}
