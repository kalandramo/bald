// walk.go 单次遍历的解析缓存：所有 AST/文本类 checker 共享一份全树解析。
//
// 此前 14 个 AST 类 checker 各自 walk + parse 全树（还有 2 份自带的重复
// WalkDir），一次 `bald convention check` ≈ O(checkers × files) 的重复解析
// ——本仓自述「1200+ Go 文件每 CI run」，即 ~14k 次解析/次检查。本文件把
// 遍历收敛为一次（J2 重复税的单一闸门）：
//
//	treeFor(dir)  — 首次调用遍历+解析，进程内缓存；
//	invalidateTree(dir) — runCheck 入口调用：一次 check 会话一棵新鲜树，
//	                      会话内共享；测试里「改树后重跑」不会吃到陈旧缓存。
//
// 遍历口径与旧 walkAST / 两份自带 WalkDir 逐条对齐：
//   - 跳过 vendor/.git/node_modules/_example/testdata；
//   - 解析失败的非合法 Go 文件保留在 files（file=nil，raw 保留）——文本类
//     checker（logging-stdlib）的 scanned 计数含它们，AST 类经 selectFiles 过滤；
//   - 豁免前缀不在遍历层滤——豁免文件计入 skip 判据的 scanned（与旧实现
//     一致：全豁免的树是 ran 不是 skip），由消费方在回调分发处跳过；
//   - rel 恒 ToSlash（豁免前缀与 Finding.File 的平台一致口径）。
package convention

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// goTree 是一次遍历的解析产物。
type goTree struct {
	hasGoMod bool        // go.mod 存在与否（各 checker 的 skip 判据）
	files    []*goSource // 含 _test.go 与解析失败文件；rel 已 ToSlash
}

// selectFiles 按消费方口径过滤：includeTests 决定 _test.go 去留；解析失败
// （file==nil）的文件不进 AST 消费面。豁免前缀不在此滤——豁免文件保留在
// 返回值里（skip 判据的 scanned 口径含豁免，与旧 walkAST 一致），由消费方
// 在回调分发处跳过（checkAST）；文本类自管豁免、直接吃 tree.files。
func (t *goTree) selectFiles(includeTests bool) []*goSource {
	var out []*goSource
	for _, s := range t.files {
		if s.file == nil {
			continue
		}
		if !includeTests && strings.HasSuffix(s.rel, "_test.go") {
			continue
		}
		out = append(out, s)
	}
	return out
}

func hasPrefixAny(rel string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// treeCache 缓存键是 dir 的绝对路径。CLI 是单次进程，缓存无需失效钩子以外的
// 生命周期管理（对齐 bconf 描述符缓存的先例：键是不可变输入，值只读共享）。
// 键是字面 Abs 串：Windows 下不同盘符大小写拼写成两个键，invalidateTree 只删
// 当前拼写——单次 runCheck 会话内拼写恒定，不构成现网问题。
var treeCache sync.Map // abs dir(string) → *goTree

// treeFor 返回 dir 的解析树（首次调用遍历+解析，此后命中缓存）。
func treeFor(dir string) (*goTree, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("abs %s: %w", dir, err)
	}
	if v, ok := treeCache.Load(abs); ok {
		return v.(*goTree), nil
	}
	t, err := buildTree(dir, abs)
	if err != nil {
		return nil, err
	}
	treeCache.Store(abs, t)
	return t, nil
}

// invalidateTree 丢弃 dir 的缓存树。runCheck 在入口调用：一次 check 会话
// 恰好一棵新鲜树——会话内 checker 共享，跨会话（含测试里改树后重跑）不吃
// 陈旧缓存。
func invalidateTree(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		treeCache.Delete(abs)
	}
}

// buildTree 遍历 abs 并解析全部 .go 文件。遍历口径与旧 walkAST 及两份自带
// WalkDir 一致：单文件/子目录错误不中断（非法 Go 与不可读文件归 go build /
// 操作系统管，检查器静默跳过）；目录白名单跳过；go.mod 缺失时不遍历（旧实现
// stat 即返 skip——消费方全部先在 hasGoMod 上 skip，files 不会被读）。
func buildTree(dir, abs string) (*goTree, error) {
	hasGoMod := false
	if _, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil {
		hasGoMod = true
	} else if !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTDIR) {
		return nil, fmt.Errorf("stat %s: %w", dir, err)
	}
	t := &goTree{hasGoMod: hasGoMod}
	if !hasGoMod {
		return t, nil
	}
	fset := token.NewFileSet()
	walkErr := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case "vendor", ".git", "node_modules", "_example", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(abs, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		s := &goSource{rel: rel, raw: string(raw), fset: fset}
		if f, perr := parser.ParseFile(fset, path, raw, 0); perr == nil {
			s.file = f
		}
		t.files = append(t.files, s)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, walkErr)
	}
	return t, nil
}
