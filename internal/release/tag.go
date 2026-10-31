package release

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Tag 按计划在 HEAD 打 tag。默认只打印将执行的命令；apply 为 true 时才真正
// 创建 tag，push 为 true 时再推送到 origin。
//
// 两道保护：工作区必须干净（tag 内容 = 已提交内容）；HEAD 相对计划只能多出
// 发布提交（go.mod 归位与 CHANGELOG），否则说明计划过期，需重新计算。
func Tag(root string, plan *Plan, apply, push bool, out io.Writer) error {
	if len(plan.Changes) == 0 {
		_, _ = fmt.Fprintln(out, "没有待发布模块，未打 tag。")
		return nil
	}
	r := &Repo{Root: root}
	head, err := r.head()
	if err != nil {
		return err
	}

	for _, c := range plan.Changes {
		exists, err := r.tagExists(c.Tag)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("tag %s 已存在；计划已应用过或版本重复", c.Tag)
		}
	}

	if apply {
		dirty, err := r.dirty()
		if err != nil {
			return err
		}
		if dirty {
			return fmt.Errorf("工作区不干净；先提交依赖归位与 CHANGELOG，再打 tag")
		}
		if plan.Head != "" && plan.Head != head {
			if err := r.onlyReleaseEdits(plan.Head, head); err != nil {
				return err
			}
		}
	}

	for _, c := range plan.Changes {
		if apply {
			if _, err := r.git("tag", c.Tag, head); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "tagged: %s -> %s\n", c.Tag, shortHash(head))
		} else {
			_, _ = fmt.Fprintf(out, "would tag: %s -> %s\n", c.Tag, shortHash(head))
		}
	}

	if push {
		if !apply {
			return fmt.Errorf("--push 需要同时传 --apply")
		}
		tags := make([]string, 0, len(plan.Changes))
		for _, c := range plan.Changes {
			tags = append(tags, c.Tag)
		}
		if _, err := r.git(append([]string{"push", "origin"}, tags...)...); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "pushed: %s\n", strings.Join(tags, " "))
	} else if apply {
		_, _ = fmt.Fprintln(out, "\n未推送；确认后执行 git push origin <tag>…")
	}
	return nil
}

func (r *Repo) tagExists(tag string) (bool, error) {
	out, err := r.git("tag", "--list", tag)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == tag {
			return true, nil
		}
	}
	return false, nil
}

func (r *Repo) dirty() (bool, error) {
	out, err := r.git("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// onlyReleaseEdits 校验 base..head 之间只改动了发布提交应有的文件。
func (r *Repo) onlyReleaseEdits(base, head string) error {
	out, err := r.git("diff", "--name-only", base+".."+head)
	if err != nil {
		return fmt.Errorf("计划记录的提交 %s 不可用，请重新运行 `bald release plan`: %w", shortHash(base), err)
	}
	for _, file := range strings.Fields(out) {
		if file == changelogFile || filepath.Base(file) == "go.mod" {
			continue
		}
		return fmt.Errorf("HEAD 比计划多出非发布改动 %s；请重新运行 `bald release plan`", file)
	}
	return nil
}
