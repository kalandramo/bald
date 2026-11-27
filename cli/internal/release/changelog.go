package release

import (
	"fmt"
	"github.com/kalandramo/bald/cli/internal/fsatomic"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const changelogFile = "CHANGELOG.md"

const changelogHeader = `# 变更日志

本文件由 ` + "`bald release changelog`" + ` 从提交历史生成：每个模块一节，版本按发布时间倒序。
条目取自该版本区间内的提交标题（Conventional Commits），不要手工调整顺序。
`

// WriteChangelog 把计划中的版本与提交写入 CHANGELOG.md，新版本插在历史记录之前。
// 同一模块的同一版本已存在时拒绝写入——重复执行不会制造重复条目。
func WriteChangelog(root string, plan *Plan, write bool, out io.Writer) error {
	// 守卫放在「没有待发布模块」那句之前：过期计划最可能的形状就是 changes 为空，
	// 先打印那句就等于是用一条旧结论把「没检查」说成「检查过」。
	if err := CheckPlanFresh(root, plan); err != nil {
		return err
	}
	if len(plan.Changes) == 0 {
		_, _ = fmt.Fprintln(out, "没有待发布模块，变更日志不变。")
		return nil
	}
	path := filepath.Join(root, changelogFile)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	sections := renderSections(root, plan)
	for _, c := range plan.Changes {
		heading := "### " + changelogLabel(root, c) + " " + c.Next
		if strings.Contains(string(existing), heading+"\n") {
			return fmt.Errorf("%s 已包含 %q；计划已应用过或版本重复", changelogFile, heading)
		}
	}

	merged := insertSections(string(existing), sections)
	if !write {
		_, _ = fmt.Fprintf(out, "would write %s（%d 个版本）：\n\n%s", changelogFile, len(plan.Changes), sections)
		return nil
	}
	if err := fsatomic.WriteFile(path, []byte(merged), 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "更新 %s（%d 个版本）\n", changelogFile, len(plan.Changes))
	return nil
}

func renderSections(root string, plan *Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", planDate(plan))
	for _, c := range plan.Changes {
		fmt.Fprintf(&b, "### %s %s\n\n", changelogLabel(root, c), c.Next)
		for _, commit := range c.Commits {
			fmt.Fprintf(&b, "- `%s` %s", shortHash(commit.Hash), commit.Subject)
			if commit.Breaking {
				b.WriteString(" **(breaking)**")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	// DX-11：版本配套矩阵。数据全取本批 plan 的真实依赖归位（Change.Requires），
	// 不臆造 tag——「tag 同批即同组」，列的是本批发版模块与其被归位到的内部依赖版本。
	// 未发生依赖归位的批次不产出空表，避免无信息噪音。
	if table := renderCompatibilityTable(plan); table != "" {
		b.WriteString(table)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// renderCompatibilityTable 从本批依赖归位数据渲染版本配套表。
// 每行 = 一个发生了内部依赖归位的发布模块 + 它归位到的依赖版本清单。
// 模块名取 go.mod 路径的最后一段（cli/bconf/pkg/… 的读者口径）。
func renderCompatibilityTable(plan *Plan) string {
	var rows []string
	for _, c := range plan.Changes {
		if len(c.Requires) == 0 {
			continue
		}
		deps := make([]string, 0, len(c.Requires))
		for _, r := range c.Requires {
			deps = append(deps, fmt.Sprintf("`%s` → `%s`", shortModule(r.Module), r.To))
		}
		rows = append(rows, fmt.Sprintf("| `%s` | `%s` | %s |",
			shortModule(c.Module), c.Next, strings.Join(deps, ", ")))
	}
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("#### 本批版本配套（cli ↔ bconf / pkg 等内部依赖）\n\n")
	b.WriteString("| 模块 | 本批版本 | 归位依赖 |\n|---|---|---|\n")
	b.WriteString(strings.Join(rows, "\n"))
	b.WriteString("\n\n本表取自本批计划的依赖归位清单；未列模块沿用其最近正式 tag。" +
		"业务项目升级时用 `go mod tidy` 从各 module tag 解析，按此表核对 cli 与框架库的版本组合。\n\n")
	return b.String()
}

func shortModule(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// planDate 取计划生成日期（RFC3339 的前 10 位）。
func planDate(plan *Plan) string {
	if len(plan.GeneratedAt) >= 10 {
		return plan.GeneratedAt[:10]
	}
	return plan.GeneratedAt
}

// insertSections 把新版本放在文件头部说明之后、最早的历史版本之前。
// 同一天再次发布时并入已有日期节（新版本在前），避免重复的日期标题。
func insertSections(existing, sections string) string {
	if strings.TrimSpace(existing) == "" {
		return changelogHeader + "\n" + sections
	}
	existing = strings.TrimRight(existing, "\n") + "\n"

	heading, body := splitDateSection(sections)
	if idx := dateHeadingIndex(existing, heading); idx >= 0 {
		rest := strings.TrimLeft(existing[idx+len(heading):], "\n")
		out := existing[:idx] + heading + "\n\n" + body + "\n"
		if rest != "" {
			out += "\n" + rest
		}
		return strings.TrimRight(out, "\n") + "\n"
	}

	if idx := strings.Index(existing, "\n## "); idx >= 0 {
		head := strings.TrimRight(existing[:idx], "\n")
		return head + "\n\n" + strings.TrimSpace(sections) + "\n\n" + existing[idx+1:]
	}
	return existing + "\n" + sections
}

// splitDateSection 拆出 "## <date>" 标题与其后的版本节内容。
func splitDateSection(sections string) (heading, body string) {
	trimmed := strings.TrimSpace(sections)
	heading, body, found := strings.Cut(trimmed, "\n")
	if !found || !strings.HasPrefix(heading, "## ") {
		return "", trimmed
	}
	return heading, strings.TrimSpace(body)
}

// dateHeadingIndex 返回日期标题所在行的起始下标；标题必须独占一行。
func dateHeadingIndex(existing, heading string) int {
	if heading == "" {
		return -1
	}
	if strings.HasPrefix(existing, heading+"\n") {
		return 0
	}
	if idx := strings.Index(existing, "\n"+heading+"\n"); idx >= 0 {
		return idx + 1
	}
	return -1
}
