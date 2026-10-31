// Package release 维护 bald 多模块仓库的发布流程，由 `bald release` 子命令驱动。
//
// 仓库里每个 Go module 独立打 tag（根模块 vX.Y.Z，子模块 <dir>/vX.Y.Z）。
// 手工维护版本号既容易漏发，也回答不了「这版为什么升」。发布被拆成四个可单独
// 重放的步骤：
//
//	plan       从上次 tag 到 HEAD 的提交算出待发布模块与下一版本，写入计划文件
//	sync       把计划内模块 go.mod 中对其他待发模块的 require 归位到下一版本
//	changelog  按计划把提交标题写入 CHANGELOG.md（同一版本拒绝重复写入）
//	tag        按计划在 HEAD 打 tag，可选推送
//
// 版本规则遵循 Conventional Commits：feat 升 minor，fix/perf/refactor/revert
// 升 patch；! 或 BREAKING CHANGE 在 0.x 升 minor、1.x+ 升 major；
// docs/chore/test/ci/style/build 不单独触发发布。未发布过的模块固定 v0.1.0。
package release

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Module 描述一个可独立发布（或仅用于归属判定）的 Go module。
type Module struct {
	Dir        string // 相对仓库根的 slash 路径；根模块为 "."
	Path       string // go.mod 的 module 声明
	Latest     string // 最近一个正式 tag（vX.Y.Z 形式），空表示尚未发布
	Releasable bool   // examples 下的演示 module 只参与文件归属，不参与发布
}

// Repo 是待发布的仓库。
type Repo struct {
	Root    string
	Modules []*Module
}

var releaseTagPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// OpenRepo 以当前工作目录所在的 git 仓库为根打开。
func OpenRepo() (*Repo, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %w", err)
	}
	return Discover(strings.TrimSpace(string(out)))
}

// Discover 从 root 开始查找 go.mod，排除消费者示例（_example）、隐藏目录、
// vendor 与 node_modules。examples 下的 module 会被标记为不可发布，但仍保留在
// 模块表中——它们占据的文件不应算到外层模块的变更里。
func Discover(root string) (*Repo, error) {
	r := &Repo{Root: root}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			if d.Name() != "go.mod" {
				return nil
			}
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			dir := filepath.ToSlash(rel)
			modulePath, err := readModulePath(path)
			if err != nil {
				return err
			}
			r.Modules = append(r.Modules, &Module{
				Dir:        dir,
				Path:       modulePath,
				Releasable: !hasSegment(dir, "examples"),
			})
			return nil
		}
		if path == root {
			return nil
		}
		name := d.Name()
		if name == "_example" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(r.Modules) == 0 {
		return nil, fmt.Errorf("no go.mod found under %s", root)
	}
	sort.Slice(r.Modules, func(i, j int) bool { return r.Modules[i].Dir < r.Modules[j].Dir })
	for _, m := range r.Modules {
		tag, err := r.latestTag(m.Dir)
		if err != nil {
			return nil, err
		}
		m.Latest = tag
	}
	return r, nil
}

func readModulePath(gomod string) (string, error) {
	data, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("%s: missing module declaration", gomod)
}

func hasSegment(path, segment string) bool {
	for _, part := range strings.Split(path, "/") {
		if part == segment {
			return true
		}
	}
	return false
}

// latestTag 返回该 module 最近的正式版本 tag。只看 HEAD 可达的 tag，避免
// 其他分支上的发布干扰当前计划。
func (r *Repo) latestTag(dir string) (string, error) {
	pattern := "v*"
	prefix := ""
	if dir != "." {
		prefix = dir + "/"
		pattern = prefix + "v*"
	}
	out, err := r.git("tag", "--list", "--merged", "HEAD", "--sort=-v:refname", pattern)
	if err != nil {
		return "", err
	}
	for _, tag := range strings.Fields(out) {
		version := strings.TrimPrefix(tag, prefix)
		if releaseTagPattern.MatchString(version) {
			return tag, nil
		}
	}
	return "", nil
}

// head 返回当前提交的完整 hash。
func (r *Repo) head() (string, error) {
	out, err := r.git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r *Repo) git(args ...string) (string, error) {
	// core.quotePath=false：非 ASCII 文件名按原始 UTF-8 输出，模块归属判断
	// 才能直接拿路径前缀比较，而不是与转义后的引号串较劲。
	full := append([]string{"-c", "core.quotePath=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = r.Root
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

// owner 返回拥有 file 的最深 module；根模块兜底。
func (r *Repo) owner(file string) *Module {
	var best *Module
	for _, m := range r.Modules {
		if m.Dir == "." {
			if best == nil {
				best = m
			}
			continue
		}
		if file == m.Dir || strings.HasPrefix(file, m.Dir+"/") {
			if best == nil || len(m.Dir) > len(best.Dir) {
				best = m
			}
		}
	}
	return best
}
