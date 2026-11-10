// Package ide 是 `bald ide init`：为 AI 会话生成 bald 的引导物，让 AI 知道
// 去哪里查规范（onexai ide init 的 bald 对应物）。
//
// 生成的文件分两类归属（与 codegen 的归属三态同思想）：
//   - Seeded 模板（不存在才写，已存在 kept: 不覆盖，--force 放行）：
//     AGENTS.md 与各工具的规则/SKILL/命令文件——消费者项目的会话引导物，
//     生成一次后归项目维护。
//   - 合并写入（读现有 JSON，只补 bald 的键，其余原样保留）：
//     .mcp.json（MCP 项目级通用配置）、.zcode/config.json（zcode 的
//     mcp.servers，schema 严格——条目只含 type/command/args 合法键）与
//     ~/.workbuddy/mcp.json（workbuddy 只有全局作用域，条目 schema 与
//     CodeBuddy 同源：type/command/args/env/description）。
//
// 工具面（--ide）：claude / zcode / trae / qoder / workbuddy。所有工具都落
// AGENTS.md 与 .mcp.json（两者是跨工具的通用约定面）；差异在各工具的私有
// 规则文件——只有能核实格式的才生成，不猜路径（workbuddy 无私有规则文件
// 格式，其差异面是全局 MCP 配置的合并）。
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

// mcpEntry 是通用 .mcp.json 里 bald server 的配置段（stdio 传输）。
var mcpEntry = map[string]any{
	"command": "bald",
	"args":    []string{"mcp", "start"},
}

// zcodeMCPEntry 是 .zcode/config.json 的 mcp.servers.bald 条目。schema 严格
// （未知键整条被丢），只放 type/command/args 三个合法键；zcode 要求 command
// 是字符串、args 是数组（OpenCode 的数组式 command 会被拒）。
var zcodeMCPEntry = map[string]any{
	"type":    "stdio",
	"command": "bald",
	"args":    []string{"mcp", "start"},
}

// workbuddyMCPEntry 是 ~/.workbuddy/mcp.json 的 mcpServers.bald 条目（schema
// 与 CodeBuddy 同源：type/command/args/env/description，command 是字符串）。
// description 进 workbuddy 连接器管理页的展示文案，帮助人工识别。
var workbuddyMCPEntry = map[string]any{
	"type":        "stdio",
	"command":     "bald",
	"args":        []string{"mcp", "start"},
	"description": "bald 规范查询与门禁（convention overview/get/check）",
}

// mergeMCPJSON 的外壳形状：承载服务器映射的 JSON 节点。显式传参、不从 entry
// 键形推断（J5）——workbuddy 与 zcode 的条目都带 type 键，但外壳一个是顶层
// mcpServers、一个是嵌套 mcp.servers。
const (
	shapeTopLevel = "top"   // 顶层 mcpServers（.mcp.json / ~/.workbuddy/mcp.json）
	shapeZcode    = "zcode" // 嵌套 mcp.servers（.zcode/config.json）
)

// seededFile 是一份 Seeded 模板：目标路径 + 内嵌模板路径 + 提示说明。
type seededFile struct {
	path    string // 相对项目根
	tmpl    string // templates/ 内路径
	comment string // generated/kept 提示的补充说明
}

// ideSpec 是一个 --ide 取值的生成面。
type ideSpec struct {
	files []seededFile
	note  string
}

// ideSpecs 是每个 --ide 取值的生成面。AGENTS.md 与 .mcp.json 是所有工具的
// 公共面（在 runInit 里另行处理），这里只登记差异面。
var ideSpecs = map[string]ideSpec{
	"claude": {
		files: []seededFile{
			{path: filepath.Join(".claude", "commands", "bald.md"), tmpl: "templates/command_bald.md", comment: "/bald 斜杠命令"},
			{path: filepath.Join(".claude", "skills", "bald-conventions", "SKILL.md"), tmpl: "templates/skill.md", comment: "规范查询 SKILL（模型自判调用）"},
		},
	},
	"zcode": {
		files: []seededFile{
			{path: filepath.Join(".zcode", "skills", "bald-conventions", "SKILL.md"), tmpl: "templates/skill.md", comment: "项目级 SKILL（模型自判调用）"},
		},
		note: "MCP 已合并进 .zcode/config.json（zcode 不读 .mcp.json）",
	},
	"trae": {
		files: []seededFile{
			{path: filepath.Join(".trae", "rules", "project_rules.md"), tmpl: "templates/rules.md", comment: "Trae 项目规则"},
		},
	},
	"qoder": {
		files: []seededFile{
			{path: filepath.Join(".qoder", "rules", "bald.md"), tmpl: "templates/rules.md", comment: "Qoder 项目规则"},
		},
	},
	"workbuddy": {
		note: "MCP 已合并进 ~/.workbuddy/mcp.json（workbuddy 只有全局作用域：所有会话、所有项目可用 bald 工具）；无私有项目规则文件",
	},
}

// supportedIDEs 返回 --ide 的合法取值（错误信息与补全用，稳定排序由调用方处理）。
func supportedIDEs() []string {
	return []string{"claude", "zcode", "trae", "qoder", "workbuddy"}
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
	}{IDE: "zcode"}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "初始化 AI 会话引导物（AGENTS.md / MCP 配置 / 工具规则文件）",
		Long: `在当前目录（业务项目根）生成 bald 的 AI 会话引导物。

公共面（所有 --ide 都落）：
  AGENTS.md            先查规范 + 最小强制集（Seeded）
  .mcp.json            合并 mcpServers.bald（bald mcp start）

差异面（按 --ide）：
  claude      .claude/commands/bald.md + .claude/skills/.../SKILL.md
  zcode       .zcode/config.json（合并 mcp.servers.bald，严格 schema）
              + .zcode/skills/bald-conventions/SKILL.md
  trae        .trae/rules/project_rules.md
  qoder       .qoder/rules/bald.md
  workbuddy   ~/.workbuddy/mcp.json（合并 mcpServers.bald；全局作用域）

Seeded 文件不存在才写，已存在输出 kept: 不覆盖（--force 放行）；JSON 配置
是合并写入，现有键原样保留。`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			spec, ok := ideSpecs[o.IDE]
			if !ok {
				return fmt.Errorf("--ide %q not supported (available: %v)", o.IDE, supportedIDEs())
			}
			// workbuddy 的 MCP 配置在用户主目录（全局作用域），先解析 home。
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home dir: %w", err) //cov:excl 正常环境 UserHomeDir 不失败
			}
			return runInit(o.IDE, spec, home, o.Force)
		},
	}
	cmd.Flags().StringVar(&o.IDE, "ide", "zcode", "AI 编码工具：claude | zcode | trae | qoder | workbuddy")
	cmd.Flags().BoolVar(&o.Force, "force", false, "覆盖已存在的 Seeded 文件（默认保留）")
	return cmd
}

// runInit 落盘公共面 + 差异面。home 是用户主目录（workbuddy 的全局 MCP 配置
// 落在 home 下，其余工具不用它）。任一步失败即返回错误（已写文件保留——各文件独立可用）。
func runInit(ide string, spec ideSpec, home string, force bool) error {
	// 公共面：AGENTS.md（Seeded）。
	// seed 只有在 embed 读失败/I-O 权限错误时才返回错误，正确测试在 root CI 无法构造。
	if err := seed("AGENTS.md", "templates/agents.md", "会话引导：先查规范 + 最小强制集", force); err != nil {
		return err //cov:excl seed 错误分支不可达（见上）
	}
	// 差异面。
	for _, f := range spec.files {
		if err := seed(f.path, f.tmpl, f.comment, force); err != nil {
			return err //cov:excl seed 错误分支不可达（见上）
		}
	}
	// MCP 配置：通用 .mcp.json 所有工具都合并；zcode 合并自己的 config.json
	//（项目级）；workbuddy 合并全局 ~/.workbuddy/mcp.json（它无项目级配置）。
	if err := mergeMCPJSON(".mcp.json", shapeTopLevel, mcpEntry); err != nil {
		return err
	}
	if ide == "zcode" {
		if err := mergeMCPJSON(filepath.Join(".zcode", "config.json"), shapeZcode, zcodeMCPEntry); err != nil {
			return err
		}
	}
	if ide == "workbuddy" {
		if err := mergeMCPJSON(filepath.Join(home, ".workbuddy", "mcp.json"), shapeTopLevel, workbuddyMCPEntry); err != nil {
			return err
		}
	}
	if spec.note != "" {
		fmt.Println("note:", spec.note)
	}
	fmt.Println("next: restart your AI session so the new config takes effect; query conventions with `bald convention overview`")
	return nil
}

// seed 写一份 Seeded 文件：不存在写（true）；存在且未开 --force 则保留（false）；
// --force 覆盖（true）。
func seed(path, tmpl, comment string, force bool) error {
	content, err := fs.ReadFile(tmplFS, tmpl)
	if err != nil {
		// tmpl 是编译期内嵌模板路径，必然存在——正确测试无法让 embed 读失败。
		return fmt.Errorf("read template %s: %w", tmpl, err) //cov:excl 内嵌模板读不会失败
	}
	if _, err := os.Stat(path); err == nil {
		if !force {
			fmt.Println("kept:", path)
			return nil
		}
	} else if !os.IsNotExist(err) {
		// 只有 root 也无法构造的权限/IO 错误才走到这里（不存在→IsNotExist 分支）。
		return fmt.Errorf("stat %s: %w", path, err) //cov:excl root CI 无法构造非 NotExist 的 Stat 错误
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err //cov:excl root 绕过权限，MkdirAll 不会失败
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return err //cov:excl root 绕过权限，WriteFile 不会失败
	}
	fmt.Println("generated:", path, "("+comment+")")
	return nil
}

// mergeMCPJSON 把 entry 合并进一张 MCP 服务器 JSON 配置。shape 显式指定承载
// 服务器映射的外壳节点（不从 entry 键形推断——J5，workbuddy 与 zcode 条目都
// 带 type 键而外壳不同）：
//   - shapeTopLevel：顶层 mcpServers（.mcp.json 通用约定 / ~/.workbuddy/mcp.json）；
//   - shapeZcode：嵌套 mcp.servers（.zcode/config.json，zcode schema）。
//
// 文件不存在则新建（父目录一并创建）；存在则解析、只补 bald 一项、原样写回
//（缩进统一两空格）。bald 条目已存在且内容一致时输出 ok: 不落盘。
func mergeMCPJSON(path, shape string, entry map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err //cov:excl root 绕过权限，MkdirAll 不会失败
	}
	root := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &root); err != nil {
			return fmt.Errorf("parse %s: %w (fix or remove it, then rerun)", path, err)
		}
	} else if !os.IsNotExist(err) {
		// 不存在→走新建分支；只有 root 也无法构造的权限/IO 错误才走到这里。
		return fmt.Errorf("read %s: %w", path, err) //cov:excl root CI 无法构造非 NotExist 的 ReadFile 错误
	}

	// 找到（或建）承载服务器映射的节点。
	var servers map[string]any
	if shape == shapeZcode {
		mcpNode, _ := root["mcp"].(map[string]any)
		if mcpNode == nil {
			mcpNode = map[string]any{}
			root["mcp"] = mcpNode
		}
		sv, _ := mcpNode["servers"].(map[string]any)
		if sv == nil {
			sv = map[string]any{}
			mcpNode["servers"] = sv
		}
		servers = sv
	} else {
		sv, _ := root["mcpServers"].(map[string]any)
		if sv == nil {
			sv = map[string]any{}
			root["mcpServers"] = sv
		}
		servers = sv
	}

	if cur, ok := servers["bald"].(map[string]any); ok && fmt.Sprint(cur) == fmt.Sprint(entry) {
		fmt.Println("ok:", path, "(bald entry already present)")
		return nil
	}
	servers["bald"] = entry
	raw, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err //cov:excl root 由合法 JSON 解码而来，Marshal 必然成功
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return err //cov:excl root 绕过权限，WriteFile 不会失败
	}
	fmt.Println("updated:", path, "(merged bald entry)")
	return nil
}
