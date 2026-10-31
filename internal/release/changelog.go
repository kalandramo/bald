package release

import (
	"fmt"
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
	if err := os.WriteFile(path, []byte(merged), 0o644); err != nil {
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
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// planDate 取计划生成日期（RFC3339 的前 10 位）。
func planDate(plan *Plan) string {
	if len(plan.GeneratedAt) >= 10 {
		return plan.GeneratedAt[:10]
	}
	return plan.GeneratedAt
}

// insertSections 把新版本放在文件头部说明之后、最早的历史版本之前。
func insertSections(existing, sections string) string {
	if strings.TrimSpace(existing) == "" {
		return changelogHeader + "\n" + sections
	}
	if !strings.HasSuffix(existing, "\n") {
		existing += "\n"
	}
	if idx := strings.Index(existing, "\n## "); idx >= 0 {
		idx++ // 指向 "## "
		return existing[:idx] + "\n" + sections + "\n" + existing[idx:]
	}
	return existing + "\n" + sections
}
