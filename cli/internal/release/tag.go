package release

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// tagPushBatch 是单次 git push 的 tag 上限，见 Tag 内注释。
const tagPushBatch = 3

// Tag 按计划在 HEAD 打 tag。默认只打印将执行的命令；apply 为 true 时才真正
// 创建 tag，push 为 true 时再推送到 origin。
//
// 三道保护：工作区必须干净（tag 内容 = 已提交内容）；HEAD 相对计划只能多出
// 发布提交（go.mod 归位与 CHANGELOG），否则说明计划过期，需重新计算；本轮
// 每个待发布模块的文件路径都得能进模块 zip，否则发出去的是装不出来的版本。
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

	if err := checkZipPaths(root, plan); err != nil {
		return err
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
		// 分批推送：GitHub 对一次推送超过 3 个 tag 不产生 push 事件，按 tag
		// 过滤的流水线（如 publish-bconf）会静默漏跑。
		for start := 0; start < len(tags); start += tagPushBatch {
			batch := tags[start:min(start+tagPushBatch, len(tags))]
			if _, err := r.git(append([]string{"push", "origin"}, batch...)...); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "pushed: %s\n", strings.Join(batch, " "))
		}
	} else if apply {
		_, _ = fmt.Fprintln(out, "\n未推送；确认后执行 git push origin <tag>…")
	}
	return nil
}

// zipFilePunct 与 golang.org/x/mod/module 的 fileNameOK 同一判据：ASCII 只放行
// 字母数字与这串标点，非 ASCII 必须是 unicode.IsLetter。
const zipFilePunct = "!#$%&()+,-.=@[]^_{}~ "

func zipPathElemOK(elem string) bool {
	if elem == "" {
		return false
	}
	for _, r := range elem {
		var ok bool
		if r < utf8.RuneSelf {
			ok = r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' ||
				strings.ContainsRune(zipFilePunct, r)
		} else {
			ok = unicode.IsLetter(r)
		}
		if !ok {
			return false
		}
	}
	return true
}

// checkZipPaths 在打 tag 前挡住「zip 建不出来」的文件路径。代理逐条校验模块内
// 路径，一个非法字符就让整个模块装不了——而 tag 已打、变更日志已写，只能再发一版
// 补救：根 v0.35.0 就因知识中心导出目录名里的全角括号（'（' 不是字母）在代理上
// 报 create zip: malformed file path，而本地全仓 build 与 CI 都照绿。
func checkZipPaths(root string, plan *Plan) error {
	repo, err := Discover(root)
	if err != nil {
		return err
	}
	published := make(map[string]string, len(plan.Changes))
	for _, c := range plan.Changes {
		published[c.Dir] = c.Tag
	}
	// -z 让 git 按原始字节以 NUL 分隔输出：默认会把非 ASCII 路径转义成引号串，
	// 那时真正该拦下的字符反而看不见。
	out, err := repo.git("ls-tree", "-r", "--name-only", "-z", "HEAD")
	if err != nil {
		return err
	}
	var offenders []string
	total := 0
	for _, file := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		if file == "" {
			continue
		}
		owner := repo.owner(file)
		if owner == nil {
			continue
		}
		tag, planned := published[owner.Dir]
		if !planned {
			continue // 不发版的模块（演示与消费者示例）不会被打进任何 zip
		}
		rel := file
		if owner.Dir != "." {
			rel = file[len(owner.Dir)+1:]
		}
		for _, elem := range strings.Split(rel, "/") {
			if zipPathElemOK(elem) {
				continue
			}
			total++
			if len(offenders) < 5 {
				offenders = append(offenders, fmt.Sprintf("%s: %s", tag, file))
			}
			break
		}
	}
	if total == 0 {
		return nil
	}
	return fmt.Errorf("%d 条文件路径进不了模块 zip（ASCII 仅字母数字与 %q，非 ASCII 须是字母）：\n  %s",
		total, zipFilePunct, strings.Join(offenders, "\n  "))
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
