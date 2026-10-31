// Package docscheck 守卫文档里相对链接的目标必须存在：文档漂移（引用了已
// 删除的文件/错误的相对路径）在 `go test ./...` 里直接暴露，而不是等读者
// 点开才发现 404。
package docscheck

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// linkRe 匹配内联 Markdown 链接 `[text](target)`。目标里的空格按 Markdown
// 约定应写作 %20，因此这里不接受裸空格。
var linkRe = regexp.MustCompile(`\[[^\]]*\]\(\s*([^)\s]+)\s*\)`)

// TestDocsLocalLinksExist 检查仓库内所有 Markdown 的相对链接目标存在。
func TestDocsLocalLinksExist(t *testing.T) {
	root := repoRoot(t)
	broken, err := brokenLinks(root)
	if err != nil {
		t.Fatalf("scan docs: %v", err)
	}
	for _, b := range broken {
		t.Errorf("broken link: %s", b)
	}
}

// goDocRefRe 匹配 Go 注释里指向仓库文档的路径。文档名可含空格（如
// "Bald 中间件设计.md"），因此以 ".md" 作右边界而非按空白切词。
var goDocRefRe = regexp.MustCompile(`docs/[^\n]*?\.md`)

// docRef 是 Go 注释里对仓库文档的一次引用。
type docRef struct {
	line int
	path string
}

// goCommentDocRefs 返回 Go 源码注释里引用的 docs/ 路径（行号从 1 起）；
// 外链里的 docs/ 段不算本地引用。
func goCommentDocRefs(src string) []docRef {
	var refs []docRef
	for i, line := range strings.Split(src, "\n") {
		slash := strings.Index(line, "//")
		if slash < 0 {
			continue
		}
		comment := line[slash:]
		for _, loc := range goDocRefRe.FindAllStringIndex(comment, -1) {
			start := strings.LastIndexAny(comment[:loc[0]], " \t(") + 1
			if strings.Contains(comment[start:loc[1]], "://") {
				continue
			}
			refs = append(refs, docRef{line: i + 1, path: comment[loc[0]:loc[1]]})
		}
	}
	return refs
}

// TestGoCommentsReferenceExistingDocs 校验 Go 注释里引用的 docs/ 路径存在：
// 代码注释是文档的第二入口，路径漂移同样会让读者扑空。
func TestGoCommentsReferenceExistingDocs(t *testing.T) {
	root := repoRoot(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		for _, ref := range goCommentDocRefs(string(src)) {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ref.path))); err != nil {
				t.Errorf("%s:%d -> %s", rel, ref.line, ref.path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan go comments: %v", err)
	}
}

// TestGoCommentDocRefs 锚定提取规则：多词文件名、中文名与外链都要按预期处理。
func TestGoCommentDocRefs(t *testing.T) {
	src := strings.Join([]string{
		"// 见 docs/devel/zh-CN/Bald 中间件设计.md 与 docs/devel/zh-CN/应用框架设计.md。",
		"// 外链 https://example.com/docs/README.md 不算本地引用。",
		`x := "docs/not-a-comment.md"`,
	}, "\n")
	refs := goCommentDocRefs(src)
	want := []docRef{
		{line: 1, path: "docs/devel/zh-CN/Bald 中间件设计.md"},
		{line: 1, path: "docs/devel/zh-CN/应用框架设计.md"},
	}
	if len(refs) != len(want) {
		t.Fatalf("got %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("ref[%d] = %v, want %v", i, refs[i], want[i])
		}
	}
}

// TestBrokenLinksDetectsMissingTarget 用临时目录锚定检查器本身：缺目标必须
// 报错、外链/锚点/代码块必须放行，避免检查器退化成永远通过或误报。
func TestBrokenLinksDetectsMissingTarget(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("docs/ok.md", "exists\n")
	write("docs/README.md", strings.Join([]string{
		"[ok](./ok.md)",
		"[space](./ok%20space.md)",
		"[anchor](#local-heading)",
		"[external](https://example.com/doc.md)",
		"[mail](mailto:dev@example.com)",
		"```",
		"[in fence](./missing-in-fence.md)",
		"```",
		"[missing](./missing.md)",
	}, "\n"))
	write("docs/ok space.md", "exists\n")

	broken, err := brokenLinks(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(broken) != 1 || !strings.Contains(broken[0], "missing.md") {
		t.Fatalf("want exactly the missing target reported, got %v", broken)
	}
}

// brokenLinks 遍历 root 下全部 Markdown（跳过版本库/依赖目录），返回不可
// 解析的相对链接清单（`文件:行 -> 目标`）。外链、纯锚点、站点绝对路径与
// 代码围栏内的示例一律跳过。
func brokenLinks(root string) ([]string, error) {
	var broken []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		for _, b := range checkFile(path, filepath.ToSlash(rel), string(src)) {
			broken = append(broken, b)
		}
		return nil
	})
	return broken, err
}

// checkFile 返回单个 Markdown 文件里的坏链接（`rel:line -> target`）；相对
// 目标以 absPath 所在目录为基准解析。
func checkFile(absPath, rel, src string) []string {
	var broken []string
	inFence := false
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, m := range linkRe.FindAllStringSubmatchIndex(line, -1) {
			// 排除 Go 泛型调用的误匹配：`Key[T](args)` 里的 `[T](`。
			if m[0] > 0 && isIdentChar(line[m[0]-1]) {
				continue
			}
			target := line[m[2]:m[3]]
			if !localLink(target) {
				continue
			}
			pathPart := target
			if j := strings.IndexByte(pathPart, '#'); j >= 0 {
				pathPart = pathPart[:j]
			}
			if pathPart == "" {
				continue // 纯锚点
			}
			decoded, err := url.PathUnescape(pathPart)
			if err != nil {
				broken = append(broken, rel+":"+strconv.Itoa(i+1)+" -> "+target+" (bad escape)")
				continue
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(absPath), filepath.FromSlash(decoded))); err != nil {
				broken = append(broken, rel+":"+strconv.Itoa(i+1)+" -> "+target)
			}
		}
	}
	return broken
}

// localLink 报告链接目标是否需要按相对路径解析到本地文件。只接受「像路径」
// 的目标（含 "/" 或文件扩展名），避免把正文里的 `[T](args)` / 裸标识符当
// 链接。
func localLink(target string) bool {
	switch {
	case target == "", strings.HasPrefix(target, "#"), strings.HasPrefix(target, "/"):
		return false
	case strings.Contains(target, "://"), strings.HasPrefix(target, "mailto:"):
		return false
	case strings.ContainsAny(target, "\"'`<>"):
		return false // 代码示例/占位符，不是真实链接
	case !strings.ContainsAny(target, "./"), strings.HasSuffix(target, "..."):
		return false
	}
	u, err := url.Parse(target)
	return err == nil && u.Scheme == ""
}

// isIdentChar 判断字节是否属于 Go/Markdown 文本里的标识符字符。
func isIdentChar(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// repoRoot 从测试工作目录（包目录）上溯到含主 module go.mod 的仓库根。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil &&
			strings.Contains(string(b), "module github.com/kalandramo/bald\n") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root (go.mod with module github.com/kalandramo/bald) not found")
		}
		dir = parent
	}
}
