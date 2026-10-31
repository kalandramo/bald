package release

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Sync 把计划内模块 go.mod 中对其他待发模块的 require 升到下一版本。
//
// 为什么必须做：模块 A 的源码若用到了同批发布的 B 的新 API，A 在 tag 上的
// go.mod 仍写着 B 的旧版本，下游 `go get A@新版本` 会按旧 B 编译而失败。
// 只处理计划内模块——不发版的模块不动，发布范围不会因为一条 require 无限扩散。
//
// write 为 false 时只打印将要做的修改（默认路径），便于先看 diff。
func Sync(root string, plan *Plan, write bool, out io.Writer) error {
	changedFiles := 0
	for _, c := range plan.Changes {
		if len(c.Requires) == 0 {
			continue
		}
		rel := "go.mod"
		if c.Dir != "." {
			rel = c.Dir + "/go.mod"
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		content := string(data)
		changed := false
		for _, u := range c.Requires {
			next, err := setRequireVersion(content, u.Module, u.From, u.To)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			if next == content {
				continue
			}
			content = next
			changed = true
			verb := "would update"
			if write {
				verb = "updated"
			}
			_, _ = fmt.Fprintf(out, "%s: %s require %s %s -> %s\n", rel, verb, u.Module, u.From, u.To)
		}
		if !changed {
			continue
		}
		changedFiles++
		if write {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
		}
	}
	if changedFiles == 0 {
		_, _ = fmt.Fprintln(out, "依赖已是最新，无需归位。")
	} else if !write {
		_, _ = fmt.Fprintln(out, "\n未写入文件；确认后加 --write。")
	}
	return nil
}

// setRequireVersion 改写 go.mod 中某个 module 的 require 版本。
// 已经是目标版本时原样返回（幂等）；文件里找不到旧版本说明计划已过期，报错。
func setRequireVersion(content, modulePath, from, to string) (string, error) {
	lines := strings.Split(content, "\n")
	found := false
	hasTarget := false
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// 块内为 "<path> <version>"，单行为 "require <path> <version>"。
		path, version := fields[0], fields[1]
		if path == "require" && len(fields) >= 3 {
			path, version = fields[1], fields[2]
		}
		if path != modulePath {
			continue
		}
		found = true
		switch version {
		case to:
			hasTarget = true
		case from:
			lines[i] = strings.Replace(line, path+" "+from, path+" "+to, 1)
			hasTarget = true
		}
	}
	if !found {
		return content, fmt.Errorf("require %s not found", modulePath)
	}
	if !hasTarget {
		return content, fmt.Errorf("require %s is neither %s nor %s; rerun `bald release plan`", modulePath, from, to)
	}
	return strings.Join(lines, "\n"), nil
}
