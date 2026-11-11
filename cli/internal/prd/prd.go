// Package prd 是 `bald prd`：PRD 工作流的启动器（onexai harness prd 的 bald
// 对应物）。PRD 的翻译与实现是 AI 会话行为——本 CLI 没有 LLM 面，能做的是
// 把「怎么翻译、按什么规范翻译」固化成会话提示词，交给 AI 会话执行。
package prd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// NewCommand 构造 `bald prd` 命令树（translate 子命令）。
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prd",
		Short: "PRD 工作流：需求描述 → 用户故事清单的翻译启动器",
		Long: `PRD 工作流（对应 onexai harness prd，见 prd 规范卡）：

  人写需求描述（docs/prd/<name>.prompt.md，自然语言 + 能力清单，尾部带「先查规范。」）
  → bald prd translate 产出会话翻译提示词（本命令；bald 无 LLM 面，翻译交给 AI 会话）
  → AI 会话按 prd 卡规范产出 docs/prd/<name>.prd.md
  → bald convention check 校验结构（prd-structure checker）

相关卡：bald convention get prd`,
		SilenceUsage: true,
	}
	cmd.AddCommand(translateCmd())
	return cmd
}

func translateCmd() *cobra.Command {
	o := &struct {
		Input string
		Out   string
		Name  string
	}{}
	cmd := &cobra.Command{
		Use:   "translate",
		Short: "把需求描述包装成 AI 会话的 PRD 翻译提示词",
		Long: `读取需求描述（-i），产出一段可直接交给 AI 会话执行的翻译提示词（-o）。
提示词固化了翻译工作流：先查 prd 卡规范 → 按 prd-r01~r07 约束拆故事 → 产出
docs/prd/<name>.prd.md → 运行 bald convention check 校验结构。

bald 是本地 CLI、没有 LLM 面——「翻译」本身由 AI 会话完成，本命令保证每次
翻译都带着同一套规范上下文起步（对齐 onexai harness prd translate 的语义，
省去每次手写工作流 Prompt）。`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runTranslate(o.Input, o.Out, o.Name)
		},
	}
	cmd.Flags().StringVarP(&o.Input, "input", "i", "", "需求描述文件（必填；建议放 docs/prd/<name>.prompt.md）")
	cmd.Flags().StringVarP(&o.Out, "output", "o", "", "会话提示词输出文件（缺省 docs/prd/<input stem>.translate-prompt.md）")
	cmd.Flags().StringVar(&o.Name, "name", "", "系统名（缺省取 input 文件名 stem）")
	_ = cmd.MarkFlagRequired("input")
	return cmd
}

// runTranslate 读取需求描述，组装会话提示词并写盘。输出文件已存在时 kept
//（Seeded 语义：不覆盖既有工作流文件，人工删除或换名重跑）。
func runTranslate(input, out, name string) error {
	raw, err := os.ReadFile(input)
	if err != nil {
		return fmt.Errorf("read %s: %w", input, err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return fmt.Errorf("%s is empty", input)
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	}
	if out == "" {
		out = filepath.Join(prdDir, name+".translate-prompt.md")
	}
	if _, err := os.Stat(out); err == nil {
		fmt.Println("kept:", out, "（已存在；删除后重跑或换 -o）")
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", out, err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(out), err)
	}
	doc := translatePrompt(name, input, string(raw))
	if err := os.WriteFile(out, []byte(doc), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	fmt.Println("generated:", out)
	fmt.Println("next: 把本文件内容粘贴给 AI 会话执行；产物落 docs/prd/" + name + ".prd.md 后运行 bald convention check")
	return nil
}

// prdDir 是 PRD 工作流产物的约定目录（与 convention 的 prd-structure checker
// 扫描目录一致）。
const prdDir = "docs/prd"

// translatePrompt 组装会话翻译提示词：工作流四步 + 需求原文。提示词里的规范
// 查询指向 bald convention（MCP 工具 bald_convention_get 同源），与 ide init
// 的引导物同一条通道。
func translatePrompt(name, input string, content string) string {
	var b strings.Builder
	b.WriteString("# PRD 翻译会话提示词（由 bald prd translate 生成）\n\n")
	b.WriteString("请把下面的需求描述翻译成 PRD（用户故事清单）。按以下步骤执行：\n\n")
	b.WriteString("1. 先查规范：调用 bald_convention_get（或 `bald convention get prd`）取 PRD 方法论卡，按其规则约束翻译；\n")
	b.WriteString("2. 按「一故事一能力」拆分用户故事（US-NNN 编号递增；认证/审计/后台任务/CLI 等横切能力独立成故事）；\n")
	b.WriteString("3. 产出 docs/prd/" + name + ".prd.md：每个故事四要素齐备（Status/Priority/Description/Acceptance Criteria），验收标准必须可判定；\n")
	b.WriteString("4. 产出后运行 `bald convention check` 确认 prd-structure 无 finding，并给出改动摘要。\n\n")
	b.WriteString("--- 需求描述原文（" + input + "）---\n\n")
	b.WriteString(strings.TrimRight(content, "\n") + "\n")
	return b.String()
}
