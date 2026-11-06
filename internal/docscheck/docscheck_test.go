// Package docscheck 守卫文档里相对链接的目标必须存在：文档漂移（引用了已
// 删除的文件/错误的相对路径）在 `go test ./...` 里直接暴露，而不是等读者
// 点开才发现 404。
package docscheck

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
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
		src, err := readText(path)
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
		src, err := readText(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		broken = append(broken, checkFile(path, filepath.ToSlash(rel), string(src))...)
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

// moduleLine 取 go.mod 的 module 声明路径（无声明返回空串）。
// 按行解析而不是匹配 `"module <path>\n"`：Windows runner 的检出默认
// core.autocrlf=true，go.mod 落盘成 CRLF 时精确换行匹配会让仓库根
// 「找不到」，三条用例一起红（2026-11-02 ci-windows schedule 实测）。
func moduleLine(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "module" {
			return f[1]
		}
	}
	return ""
}

// readText 读文本文件并把 CRLF 归一成 LF。Windows runner 的检出默认
// core.autocrlf=true，正文带 CR 会让 module-list 围栏正则与链接目标判定
// 失真（2026-11-02 ci-windows schedule 实测「清单消失」）。
func readText(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), nil
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
			moduleLine(b) == "github.com/kalandramo/bald" {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root (go.mod with module github.com/kalandramo/bald) not found")
		}
		dir = parent
	}
}

// moduleListRe 抓取 Markdown 里以 ```module-list 开栏的代码块——文档中的
// module 清单与派生结果的接缝（见 current-architecture.md §2）。
var moduleListRe = regexp.MustCompile("(?ms)^```module-list\n(.*?)\n```")

// derivedModules 复刻 scripts/list-modules.sh 的谓词：仓库内含 go.mod 的目录，
// 排除 .git/ 与 _example*，根模块记作 "."，字典序返回（相对根、slash 分隔）。
func derivedModules(root string) []string {
	var mods []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		slash := filepath.ToSlash(rel)
		if d.IsDir() {
			if slash == ".git" || d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			if strings.HasPrefix(slash, "_example") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "go.mod" {
			dir, relErr := filepath.Rel(root, filepath.Dir(path))
			if relErr != nil {
				return relErr
			}
			if dir == "." {
				mods = append(mods, ".")
			} else {
				mods = append(mods, filepath.ToSlash(dir))
			}
		}
		return nil
	})
	if err != nil {
		return nil
	}
	sort.Strings(mods)
	return mods
}

// documentedModules 返回全部 Markdown 里 module-list 代码块声明的模块集合。
func documentedModules(t *testing.T, root string) map[string]string {
	t.Helper()
	doc := map[string]string{}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		src, err := readText(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		for _, m := range moduleListRe.FindAllStringSubmatch(string(src), -1) {
			for _, line := range strings.Split(m[1], "\n") {
				if name := strings.TrimSpace(line); name != "" {
					doc[name] = filepath.ToSlash(rel)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan module-list blocks: %v", err)
	}
	return doc
}

// TestModuleListMatchesDerivation 把文档里的 module 清单钉在真实派生结果上：
// 「新增模块没进文档」这类漂移光靠链接检查测不出来，此处逐条比对两个方向。
func TestModuleListMatchesDerivation(t *testing.T) {
	root := repoRoot(t)
	mods := derivedModules(root)
	if len(mods) < 50 {
		t.Fatalf("派生到 %d 个 module，明显低于仓库实际数量——谓词失效，本闸门已空转", len(mods))
	}
	doc := documentedModules(t, root)
	if len(doc) == 0 {
		t.Fatal("没有任何 ```module-list 代码块：清单消失，闸门已空转")
	}
	for _, m := range mods {
		if _, ok := doc[m]; !ok {
			t.Errorf("module %s 未出现在文档清单里", m)
		}
	}
	for m, where := range doc {
		if !slices.Contains(mods, m) {
			t.Errorf("文档清单里的 %s（%s）在仓库中不是 module", m, where)
		}
	}
}

// TestModuleDerivationMatchesScript 用真实脚本给上面的复刻背书：Go 侧谓词必须
// 与 scripts/list-modules.sh（CI 与 Taskfile 唯一的清单来源）逐行一致。
// Windows 无 POSIX sh，跳过。
func TestModuleDerivationMatchesScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scripts/list-modules.sh 需要 POSIX sh")
	}
	root := repoRoot(t)
	out, err := exec.Command("sh", filepath.Join(root, "scripts", "list-modules.sh")).Output()
	if err != nil {
		t.Fatalf("run scripts/list-modules.sh: %v", err)
	}
	var want []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			want = append(want, strings.TrimPrefix(line, "./"))
		}
	}
	sort.Strings(want)
	got := derivedModules(root)
	if len(got) != len(want) {
		t.Fatalf("Go 派生 %d 个，脚本派生 %d 个", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("derivation[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

// rootModulePath 是聚合根的 module 声明路径。
const rootModulePath = "github.com/kalandramo/bald"

// moduleOfImport 按最长前缀把 import 路径归到属主 module（返回其在仓库里的
// 目录，根 module 为 "."）；不属于本仓任何 module 的外部导入返回 ""。
func moduleOfImport(importPath string, mods []string) string {
	bestDir, bestLen := "", -1
	for _, dir := range mods {
		mp := rootModulePath
		if dir != "." {
			mp += "/" + dir
		}
		if importPath == mp || strings.HasPrefix(importPath, mp+"/") {
			if len(mp) > bestLen {
				bestDir, bestLen = dir, len(mp)
			}
		}
	}
	return bestDir
}

// importPathsFromGoSource 取真实 import 声明里的路径（单行与括号块两种形态），
// 不看注释与字符串字面量——模板字符串里的 import 字面量不是依赖。
func importPathsFromGoSource(src string) []string {
	var out []string
	for _, m := range singleImportRe.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	for _, blk := range importBlockRe.FindAllStringSubmatch(src, -1) {
		for _, m := range importSpecRe.FindAllStringSubmatch(blk[1], -1) {
			out = append(out, m[1])
		}
	}
	return out
}

var (
	singleImportRe = regexp.MustCompile(`(?m)^import\s+(?:[\w_.]+\s+)?"([^"]+)"`)
	importBlockRe  = regexp.MustCompile(`(?ms)^import\s*\(\n(.*?)^\)`)
	importSpecRe   = regexp.MustCompile(`(?m)^\s*(?:[\w_.]+\s+)?"([^"]+)"\s*(?://.*)?$`)
)

// moduleOfDir 按最长前缀把仓库内目录（slash 相对根）归到属主 module；没有更长的
// module 前缀命中时归根 module（"."）。与 moduleOfImport 同源，只是输入是目录。
func moduleOfDir(dirSlash string, mods []string) string {
	bestDir, bestLen := ".", -1
	for _, m := range mods {
		if m == "." {
			continue
		}
		if dirSlash == m || strings.HasPrefix(dirSlash, m+"/") {
			if len(m) > bestLen {
				bestDir, bestLen = m, len(m)
			}
		}
	}
	return bestDir
}

// packagesByModule 把仓库里每个 .go 文件所在目录归到属主 module，返回
// module → 包目录集合（slash 相对根）。仓库根目录本身不参与（根不是包目录）。
func packagesByModule(root string, mods []string) (map[string]map[string]bool, error) {
	pkgs := map[string]map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".qoder", "node_modules", "vendor":
				return filepath.SkipDir
			}
			if strings.HasPrefix(filepath.Base(path), "_example") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		dir, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(dir)
		if slash == "." {
			return nil
		}
		owner := moduleOfDir(slash, mods)
		if pkgs[owner] == nil {
			pkgs[owner] = map[string]bool{}
		}
		pkgs[owner][slash] = true
		return nil
	})
	return pkgs, err
}

// diffPkgs 比对包集合：返回预期外的（extra）与不见了的（missing），各自字典序。
func diffPkgs(got map[string]bool, want []string) (extra, missing []string) {
	for p := range got {
		if !slices.Contains(want, p) {
			extra = append(extra, p)
		}
	}
	for _, p := range want {
		if !got[p] {
			missing = append(missing, p)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	return extra, missing
}

// TestModulePackageSets 钉住 risk-review.md §3.1 与 §3.4 的收口形态：聚合根只剩
// 本仓工具包，CLI 住在自己的 module 里。包集合是逐字比对的——挪走或新增包都会红，
// 因为「根只剩工具」这句话只有在根真的没有别人时才成立。
func TestModulePackageSets(t *testing.T) {
	root := repoRoot(t)
	mods := derivedModules(root)
	pkgs, err := packagesByModule(root, mods)
	if err != nil {
		t.Fatalf("scan packages: %v", err)
	}
	if len(pkgs) < 50 {
		t.Fatalf("只归集到 %d 个 module 的包，明显低于仓库实际数量——遍历失效，本闸门已空转", len(pkgs))
	}
	wantPkgs := map[string][]string{
		".":   {"internal/docscheck"},
		"cli": {"cli/cmd/bald", "cli/internal/codegen", "cli/internal/release"},
	}
	for mod, want := range wantPkgs {
		if !slices.Contains(mods, mod) {
			t.Fatalf("派生清单里没有 module %s：包集合无从比对", mod)
		}
		extra, missing := diffPkgs(pkgs[mod], want)
		if len(extra) > 0 {
			t.Errorf("module %s 里出现了预期外的包 %v：应归属哪个 module？（risk-review.md §3.1/§3.4）", mod, extra)
		}
		if len(missing) > 0 {
			t.Errorf("module %s 里预期的包不见了 %v：判据已过期，请核对 §3.1/§3.4 后更新本清单", mod, missing)
		}
	}
}

// TestPkgModuleIsLeaf 钉住 §3.1 的第二个条件：`pkg` module 不 import 任何归根
// module 的包。本地 `replace` 让违反它的代码照样能编译，扇入复辟在 CI 上是静默的
// ——只有发版时才炸，所以必须在此断言。
func TestPkgModuleIsLeaf(t *testing.T) {
	root := repoRoot(t)
	mods := derivedModules(root)
	if !slices.Contains(mods, "pkg") {
		t.Fatal("pkg 不是独立 module：派生清单里没有 pkg/")
	}

	// pkg 不得 import 根 module 的包。
	hits := 0
	pkgDir := filepath.Join(root, "pkg")
	err := filepath.WalkDir(pkgDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		src, err := readText(path)
		if err != nil {
			return err
		}
		hits++
		rel, _ := filepath.Rel(root, path)
		for _, imp := range importPathsFromGoSource(string(src)) {
			if moduleOfImport(imp, mods) == "." {
				t.Errorf("%s imports %s：pkg module 反向依赖聚合根，扇入复辟", filepath.ToSlash(rel), imp)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan pkg imports: %v", err)
	}
	if hits < 50 {
		t.Fatalf("只扫到 %d 个 pkg/ 下的 .go 文件——遍历失效，本闸门已空转", hits)
	}
}

// TestModuleOfImport 锚定归属判定本身：嵌套 module 必须按最长前缀胜出，
// 否则上面的叶子判据会把 `pkg/...` 误判成根依赖（或反过来漏判）。
// 第二组故意把长前缀排在短前缀之前——只靠「后到的匹配覆盖」的实现会在这组上红，
// 从而证明长度比较真的在起作用。
func TestModuleOfImport(t *testing.T) {
	cases := []struct {
		imp, want string
	}{
		{"github.com/kalandramo/bald/pkg/appkit", "pkg"},
		{"github.com/kalandramo/bald/transport/grpc", "transport/grpc"},
		{"github.com/kalandramo/bald/transport", "transport"},
		{"github.com/kalandramo/bald/log", "log"},
		{"github.com/kalandramo/bald/cli/internal/codegen", "cli"},
		{"github.com/kalandramo/bald/cli", "cli"},
		{"github.com/kalandramo/bald/internal/docscheck", "."},
		{"github.com/you/other", ""},
	}
	for _, mods := range [][]string{
		{".", "cli", "log", "pkg", "transport", "transport/grpc"},
		{"transport/grpc", ".", "cli", "transport", "pkg", "log"},
	} {
		for _, c := range cases {
			if got := moduleOfImport(c.imp, mods); got != c.want {
				t.Errorf("moduleOfImport(%q, %v) = %q, want %q", c.imp, mods, got, c.want)
			}
		}
	}
}

// requiresModulePath 判断 go.mod 是否 require 了指定 module（单行与 require 块
// 两种形态都算，`// indirect` 同样命中——它意味着该 module 真的在构建图里）。
func requiresModulePath(raw []byte, mod string) bool {
	inBlock := false
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if f[0] == "require" {
			if len(f) >= 3 && f[1] == mod {
				return true
			}
			inBlock = f[len(f)-1] == "("
			continue
		}
		if inBlock {
			if f[0] == ")" {
				inBlock = false
				continue
			}
			if f[0] == mod {
				return true
			}
		}
	}
	return false
}

// TestNoModuleRequiresAggregateRoot 钉住 risk-review.md §3.1 的消解条件：任何
// 非示例模块都不再 require 聚合根。这条必须硬失败——本地 `replace` 让违反它的
// 代码照样编译通过，CI 的 build/vet/test 全绿，扇入复辟只有到发版才显现。
func TestNoModuleRequiresAggregateRoot(t *testing.T) {
	root := repoRoot(t)
	checked := 0
	for _, m := range derivedModules(root) {
		if m == "." {
			continue
		}
		raw, err := readText(filepath.Join(root, filepath.FromSlash(m), "go.mod"))
		if err != nil {
			t.Fatalf("read %s/go.mod: %v", m, err)
		}
		checked++
		if requiresModulePath(raw, rootModulePath) {
			t.Errorf("module %s require 了聚合根 %s：应改 require %s/pkg（risk-review.md §3.1）",
				m, rootModulePath, rootModulePath)
		}
	}
	if checked < 50 {
		t.Fatalf("只检查了 %d 个非根 module，明显低于仓库实际数量——派生已空转", checked)
	}
}

// goInstallRe 匹配代码围栏内的 `go install <path>@<version>` 行。围栏是文档承诺
// 「复制即可跑」的边界：正文里用内联 code 引用同一条命令往往是在说明它失败
// （《用 bald CLI 起步新服务》§2 正是这么写的），那种不算承诺。
var goInstallRe = regexp.MustCompile(`^[ \t]*go[ \t]+install[ \t]+(\S+@\S+)`)

// goModReplaceCount 数 go.mod 里的 replace 指令条数：单行 `replace a => b` 与
// `replace ( … )` 块内每一行都算。Go 的判据只看条数是否大于 0，与某条 replace
// 是否真被 build 用到无关，所以这里也只数不筛。
func goModReplaceCount(raw []byte) int {
	inBlock, n := false, 0
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if inBlock {
			if f[0] == ")" {
				inBlock = false
				continue
			}
			n++
			continue
		}
		if f[0] != "replace" {
			continue
		}
		if f[len(f)-1] == "(" {
			inBlock = true
			continue
		}
		n++
	}
	return n
}

// installViolations 返回全部 Markdown 代码围栏里「指向本仓 module、而该 module 的
// go.mod 含 replace」的 `go install <path>@<ver>` 行。Go 安装命名包时会抓取目标
// module 的**已发布** go.mod，只要有任何 replace 指令就直接拒绝（本机 Go 1.27.1 的
// cmd/go/internal/load/pkg.go：`if len(f.Replace) > 0`），因此这种命令永远装不上。
// 根模块因 19 条同仓 replace 中招的实况见 risk-review.md §3.4。
func installViolations(root string) ([]string, error) {
	mods := derivedModules(root)
	counts := make(map[string]int, len(mods))
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".qoder", "node_modules", "vendor":
				// .qoder/ 是知识中心的导出物，已 gitignore、随时重生成，
				// 闸门管不到它，也不能让它把仓库自己的文档判红。
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		src, err := readText(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		inFence := false
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				inFence = !inFence
				continue
			}
			if !inFence {
				continue
			}
			m := goInstallRe.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}
			dir := moduleOfImport(strings.SplitN(m[1], "@", 2)[0], mods)
			if dir == "" {
				continue // 外部 module，不在本闸范围
			}
			if _, ok := counts[dir]; !ok {
				raw, err := readText(filepath.Join(root, filepath.FromSlash(dir), "go.mod"))
				if err != nil {
					return err
				}
				counts[dir] = goModReplaceCount(raw)
			}
			if counts[dir] > 0 {
				out = append(out, fmt.Sprintf("%s:%d: go install %s → 本仓 module %s（其 go.mod 有 %d 条 replace，Go 拒绝为它安装命名包，见 risk-review.md §3.4）",
					rel, i+1, m[1], dir, counts[dir]))
			}
		}
		return nil
	})
	return out, err
}

// TestDocsInstallsAreReplaceFree 钉住「文档给的安装命令真的装得上」：仓库 Markdown
// 的围栏里不得出现指向含 replace 之本仓 module 的 go install。
func TestDocsInstallsAreReplaceFree(t *testing.T) {
	violations, err := installViolations(repoRoot(t))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, v := range violations {
		t.Errorf("%s", v)
	}
}

// TestInstallGateJudgesReplacesNotPaths 锚定判据本身，两个方向都要有：含 replace 的
// 目标必须红（把命令改成合法路径、但 module 仍带 replace 也不行）、零 replace 的
// 目标必须绿、外部 module 与围栏外的命令不参与。缺了后半，这条闸会退化成
// 「凡本仓 module 一律红」；缺了前半，它会在扫描器失效时空转。
func TestInstallGateJudgesReplacesNotPaths(t *testing.T) {
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
	write("go.mod", "module github.com/kalandramo/bald\n\ngo 1.27.1\n\nreplace github.com/kalandramo/bald/bconf => ./bconf\n")
	write("bconf/go.mod", "module github.com/kalandramo/bald/bconf\n\ngo 1.27.1\n")
	write("docs/guide.md", strings.Join([]string{
		// 围栏外的裸命令与内联 code 一样只是正文引用，不构成「复制即可跑」的承诺。
		"go install github.com/kalandramo/bald/cmd/bald@v0.36.0",
		"正文里的 `go install github.com/kalandramo/bald/cmd/bald@v1.0.0` 是在说明它装不上，不算承诺。",
		"```bash",
		"go install github.com/kalandramo/bald/cmd/bald@v1.0.0",
		"go install github.com/kalandramo/bald/bconf@v0.17.0",
		"go install github.com/hibiken/asynq/tools/asynq@latest",
		"go install ./cmd/bald",
		"```",
	}, "\n"))

	violations, err := installViolations(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("want exactly 1 violation, got %v", violations)
	}
	if !strings.Contains(violations[0], "docs/guide.md:4") || !strings.Contains(violations[0], "cmd/bald@v1.0.0") {
		t.Fatalf("violation should name the fenced root-module target, got %q", violations[0])
	}
}

// TestCliModuleIsReplaceFree 是 §3.4 走 B（CLI 拆成独立 module）的机器保证。
// Go 为命名包安装某个版本时抓取目标 module 的**已发布** go.mod，只要里面有任意一条
// replace 就直接拒绝（本机 Go 1.27.1 的 cmd/go/internal/load/pkg.go：
// `if len(f.Replace) > 0`），与这条 replace 是否被 build 用到无关。
// 「零 replace」是 cli 唯一的卖点，所以必须钉死：日后顺手加一条
// `replace github.com/kalandramo/bald/bconf => ../bconf` 能让本地 build、task test
// 与 CI 全绿，同时把 go install @tag 永久变红。
func TestCliModuleIsReplaceFree(t *testing.T) {
	root := repoRoot(t)
	if !slices.Contains(derivedModules(root), "cli") {
		t.Fatal("派生清单里没有 cli/：CLI 还没独立成 module（risk-review.md §3.4 走 B）")
	}
	raw, err := readText(filepath.Join(root, "cli", "go.mod"))
	if err != nil {
		t.Fatalf("read cli/go.mod: %v", err)
	}
	if n := goModReplaceCount(raw); n > 0 {
		t.Errorf("cli/go.mod 含 %d 条 replace：Go 会拒绝为它安装命名包，"+
			"go install github.com/kalandramo/bald/cli/cmd/bald@<tag> 永远装不上（§3.4）", n)
	}
	// 入口得真的存在，否则「零 replace 的 module」是空的，装机承诺无从谈起。
	if _, err := os.Stat(filepath.Join(root, "cli", "cmd", "bald", "main.go")); err != nil {
		t.Errorf("cli/cmd/bald/main.go 缺失：闸门失去判定对象（%v）", err)
	}
}

// goWorkTracked 从文件名清单里挑出 go.work / go.work.sum（按 basename，子目录也算）。
func goWorkTracked(files []string) []string {
	var out []string
	for _, f := range files {
		switch filepath.Base(f) {
		case "go.work", "go.work.sum":
			out = append(out, filepath.ToSlash(f))
		}
	}
	return out
}

// TestGoWorkIsNotTracked 钉住 B-iii 的联合开发姿势：go.work 只存在于本机，绝不进
// 版本库。一旦提交，CI 的逐模块 go mod tidy -diff / build / vet 会优先吃到磁盘上的
// 同仓代码，「已发布形态」的判据静默退化成「本机形态」。
func TestGoWorkIsNotTracked(t *testing.T) {
	// 判据本身先验活：夹具命中数不对，后面的实仓断言就是空转。
	if got := goWorkTracked([]string{"README.md", "go.work", "cli/go.work.sum"}); len(got) != 2 {
		t.Fatalf("goWorkTracked 夹具命中 %v，want 2 项——判据失效", got)
	}
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v（本闸门需要版本库，无 git 检出时不要指望它绿）", err)
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	if len(files) < 100 {
		t.Fatalf("git ls-files 只返回 %d 个文件——清单异常，本闸门已空转", len(files))
	}
	if got := goWorkTracked(files); len(got) > 0 {
		t.Errorf("go.work 进了版本库 %v：应删掉并加进 .gitignore，否则 CI 的逐模块判据不再是发布形态（§3.4）", got)
	}
}

// TestTaskfileNoPerCommandDir 钉住 Taskfile.yml 的形状：`dir` 只有写在**任务级**
// 才生效，go-task 会静默忽略 `cmds` 条目里的 `dir`（实测 3.52.0：`- cmd: pwd` 配
// `dir: sub` 打印的仍是任务目录）。被忽略时不报错也不失败，于是跨目录的
// `go test ./...` 退化成「把根 module 再跑一遍」——覆盖面凭空消失而结果全绿，
// 正是 §8 那一类假绿。跨目录一律拆成带任务级 dir 的 internal 子任务。
func TestTaskfileNoPerCommandDir(t *testing.T) {
	root := repoRoot(t)
	raw, err := readText(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	var perCmd, taskLevel []int
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, "dir:") {
			continue
		}
		lineNo := i + 1
		if len(line)-len(trimmed) > 4 {
			perCmd = append(perCmd, lineNo)
		} else {
			taskLevel = append(taskLevel, lineNo)
		}
	}
	// 防空转：任务级 dir 是这条判据的对照物，一个都没有说明解析没落到形状上。
	if len(taskLevel) == 0 {
		t.Fatal("Taskfile.yml 里没有一条任务级 dir——判据失去对照，本闸门在空转")
	}
	for _, lineNo := range perCmd {
		t.Errorf("Taskfile.yml:%d 在 cmds 条目里写了 dir，go-task 会忽略它；把这条腿拆成带任务级 dir 的 internal 子任务再用 task: 调用", lineNo)
	}
}

// TestWorkflowGoVersionsAgree 钉住「所有作业文件用同一个 Go 补丁版本」。
// 漂移是真实发生过的：`ci-windows` 建出来时 pin 1.27.1，主 ci 后来升到 1.27.3，
// 周界作业继续在不发布的那个工具链上取证（登记册 §7.5「刻意没做 ②」）。
// 只比对 pin 了的文件——`publish-bconf.yml` 不跑 Go 测试，没有这一维。
func TestWorkflowGoVersionsAgree(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read .github/workflows: %v", err)
	}
	pinned := 0
	byValue := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		raw, err := readText(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			value, ok := strings.CutPrefix(trimmed, "go-version:")
			if !ok {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			if value == "" {
				t.Errorf("%s:%d go-version 的值为空", e.Name(), i+1)
				continue
			}
			byValue[value] = append(byValue[value], fmt.Sprintf("%s:%d", e.Name(), i+1))
			pinned++
		}
	}
	// 防空转：仓库现在至少有 ci.yml 与 windows.yml 两条 pin，一条都没有说明
	// 匹配没落到形状上（比如 pin 换成了 setup-go 的 `go-version-file`）。
	if pinned < 2 {
		t.Fatalf("只找到 %d 处 go-version pin——判据失去对照，本闸门已空转", pinned)
	}
	if len(byValue) > 1 {
		t.Errorf("作业文件 pin 了 %d 个不同的 Go 版本 %v——周界/发布取证必须用同一个工具链", len(byValue), byValue)
	}
}

// ---- §7.2：有逻辑的 module 必须有测试 ----

// moduleGoFiles 返回 modDir 内**属于该 module** 的 .go 文件（slash 相对路径）。
// 嵌套 module 各自统计（否则根 module 会把全仓吞进来）；`_example*` 与 CI 的
// 清单同源地排除；testdata/.git/vendor/node_modules 不参与编译，也不算覆盖。
func moduleGoFiles(root, modDir string, all map[string]bool) ([]string, error) {
	var files []string
	err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(modDir)), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			if strings.HasPrefix(slash, "_example") {
				return filepath.SkipDir
			}
			if slash != modDir && all[slash] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(slash, ".go") {
			files = append(files, slash)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// topLevelFuncBodies 数出文件里**带函数体**的顶层 func 声明。接口方法没有 Body，
// 所以「只有 interface/type/const 的契约包」天然是 0——这正是闸门不向纯声明包
// 索要测试的依据，而不是一份需要人工维护的豁免名单。
func topLevelFuncBodies(root string, files []string) (int, error) {
	var n int
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(f)), nil, parser.SkipObjectResolution)
		if err != nil {
			return 0, fmt.Errorf("parse %s: %w", f, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Body != nil {
				n++
			}
		}
	}
	return n, nil
}

// TestModulesWithLogicHaveTests 是风险登记册 §7.2 的闸门。「一个 module 有没有测试」
// 原先只能靠人评审时数文件点数出来——§1.3 那两个零测试适配器（broker/kafka、
// broker/rabbitmq）就是这么发现的，而不是任何闸门响出来的；修完之后这条判据第一次
// 能自己响。
//
// 谓词分两面：
//   - 有可执行逻辑（非测试文件里存在顶层带体 func）的 module 必须有测试文件；
//   - 纯声明 module 不作要求。实测豁免的只有 metrics（两个接口）与
//     transport/subscribe（两个类型），为它们写「断言接口存在」的用例只会让闸门
//     变绿，属假覆盖而不是覆盖。
//
// 百分比阈值刻意没做，且 2026-11-04 把 94 个 module 的基线实测出来之后仍然不做，
// 理由换了：分母不是质量的函数。实测里「每 module ≥ X%」会当场判红 `.`、`cache`、
// `ratelimit` 三个 0.0% —— 它们的 module 根零可执行语句，`go test` 直接报
// `coverage: [no statements]`；而 `registry/nacos` 同一个 module 在 `-short` 是
// 29.3%、去掉 `-short`（真 nacos 在场）是 45.7%。要先给它一条阈值，就得先裁决
// 「零语句 module 算不算分母、生成码算不算、`-short` 算不算」——那是口径裁决而不是
// 覆盖率问题。两条分布与推导见风险登记册 §7.2「7.2 补」；本闸门管的是那条不会随
// 口径漂移的缺口：有可执行逻辑却没有一个测试文件。
func TestModulesWithLogicHaveTests(t *testing.T) {
	root := repoRoot(t)
	mods := derivedModules(root)
	if len(mods) < 50 {
		t.Fatalf("派生到 %d 个 module，明显低于仓库实际数量——谓词失效，本闸门已空转", len(mods))
	}
	set := make(map[string]bool, len(mods))
	for _, m := range mods {
		set[m] = true
	}
	var withLogic, tested int
	for _, m := range mods {
		files, err := moduleGoFiles(root, m, set)
		if err != nil {
			t.Fatalf("scan %s: %v", m, err)
		}
		bodies, err := topLevelFuncBodies(root, files)
		if err != nil {
			t.Fatalf("count func bodies in %s: %v", m, err)
		}
		var tests int
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				tests++
			}
		}
		if tests > 0 {
			tested++
		}
		if bodies == 0 {
			t.Logf("纯声明 module（顶层带体 func=0，测试文件=%d）：%s", tests, m)
			continue
		}
		withLogic++
		if tests == 0 {
			t.Errorf("module %s 有 %d 个顶层函数却一个测试文件都没有——§7.2 的口径是每个有逻辑的 module 都要被执行证据覆盖", m, bodies)
		}
	}
	// 防空转：两面判据各自都要有量，否则「零违规」只是遍历没落到文件上。
	if withLogic < 50 {
		t.Fatalf("只识别到 %d 个有逻辑的 module，明显低于仓库实际数量——函数体统计失效，本闸门已空转", withLogic)
	}
	if tested < 50 {
		t.Fatalf("只识别到 %d 个含测试文件的 module，明显低于仓库实际数量——测试文件发现失效，本闸门已空转", tested)
	}
	t.Logf("有逻辑的 module=%d，含测试的 module=%d，总 module=%d", withLogic, tested, len(mods))
}

// acceptanceTabSep 是 scripts/acceptance-gate.sh 里 $'...' 行的 TAB 字面量——
// 文件里写的是「反斜杠 + t」两个字符，不是真空白，所以按这两字节切列。
const acceptanceTabSep = `\t`

// acceptanceLeg 是判据表里的一条腿（列序与脚本文件头一致，tags 恒在最后）。
type acceptanceLeg struct {
	scope, dir, pkg, regex string
}

// acceptanceLegs 从脚本里解析判据表。表与执行在同一个文件里，所以闸门读的是
// 真跑的那张表，不是文档里抄的第二份。
func acceptanceLegs(t *testing.T, root string) []acceptanceLeg {
	t.Helper()
	raw, err := readText(filepath.Join(root, "scripts", "acceptance-gate.sh"))
	if err != nil {
		t.Fatalf("读 acceptance 脚本: %v", err)
	}
	var legs []acceptanceLeg
	for i, line := range strings.Split(string(raw), "\n") {
		// 表在 `printf '%s\n' \` 的续行里，行尾的反斜杠在引号外面。
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "$'") {
			continue
		}
		trimmed = strings.TrimSuffix(trimmed, `\`)
		trimmed = strings.TrimSpace(trimmed)
		if !strings.HasSuffix(trimmed, "'") {
			continue
		}
		cols := strings.Split(strings.TrimSuffix(strings.TrimPrefix(trimmed, "$'"), "'"), acceptanceTabSep)
		if len(cols) != 5 {
			t.Fatalf("acceptance-gate.sh:%d 判据行不是 5 列（%q）——列序是 <ci|no>\\t<模块>\\t<包>\\t<-run 正则>\\t<tags>，改了形状要同步本闸门", i+1, trimmed)
		}
		if cols[0] != "ci" && cols[0] != "no" {
			t.Fatalf("acceptance-gate.sh:%d 的 scope 是 %q，只认 ci/no", i+1, cols[0])
		}
		legs = append(legs, acceptanceLeg{scope: cols[0], dir: cols[1], pkg: cols[2], regex: cols[3]})
	}
	return legs
}

// shortGatedTests 返回 pkgDir 里在 `-short` 下会跳过的顶层测试名。归因做一层：
// 守卫写在 helper 里（示例的 etcdDiscovery / nacosDiscovery）也算——「测试函数自己
// 没调 testing.Short()」不代表它在 -short 下会跑。两层以上不追，仓库里没有那种形状。
func shortGatedTests(t *testing.T, pkgDir string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("读包目录 %s: %v", pkgDir, err)
	}
	guarded := map[string]bool{}
	calls := map[string][]string{}
	tests := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(pkgDir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("解析 %s: %v", e.Name(), err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if strings.HasPrefix(name, "Test") {
				tests[name] = true
			}
			var called []string
			hasShort := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				ce, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := ce.Fun.(type) {
				case *ast.SelectorExpr:
					if recv, ok := f.X.(*ast.Ident); ok && recv.Name == "testing" && f.Sel.Name == "Short" {
						hasShort = true
					}
					called = append(called, f.Sel.Name)
				case *ast.Ident:
					called = append(called, f.Name)
				}
				return true
			})
			guarded[name] = hasShort
			calls[name] = called
		}
	}
	out := map[string]bool{}
	for name := range tests {
		if guarded[name] {
			out[name] = true
			continue
		}
		for _, c := range calls[name] {
			if guarded[c] {
				out[name] = true
				break
			}
		}
	}
	return out
}

// shortSkipExemptions 是「既不在判据表里、也没被任何 CI 作业点名」的 -short 跳过
// 用例。每条都带具体理由，且下面会验它**仍然成立**——用例改名、被重新排进表或排进
// CI，这条豁免就变成红，名单不会烂在那里。
//
// 现在为空，而且**空本身是判据**：这四条示例用例此前靠豁免存续，理由都写着「示例
// module 不在 CI（见 §7.1）」；ci.yml 的 `example` 作业把那句话变成假话之后，把它们
// 留在名单里就是拿一条死条目把判据关掉——`seen` 那侧的过期检查正是为此存在。
// 要重新加条目，先确认这条腿真的没有任何作业点名它（第 2 判据只查名字在
// .github/workflows/*.yml 里出现过，方向是误报红，不是误报绿）。
var shortSkipExemptions = map[string]string{}

// TestAcceptanceTableCoversShortSkippedTests 是风险登记册 §8 那条「未闭合的形状」的
// 闸门。acceptance 判据按 `-run` 正则挑用例，而 CI 的 test/race 作业跑的是
// `go test -short`——两条口径之间没有任何东西保证「-short 跳过的用例」真被某条
// 腿接住。原先只能靠人对着包数：本轮实测 appkit 269 个顶层测试里一个都不调
// testing.Short()（所以那五条 appkit 腿不存在这类洞），洞只可能开在 codegen 与
// 示例包上；这条闸门把「有没有开」变成机器判。
//
// 判据：判据表覆盖的每个包里，-short 下会跳过的顶层测试必须满足三者之一——
//  1. 被同包的某条腿的正则命中（= 进表）；
//  2. 名字出现在 .github/workflows/ 里（= 有别的作业点名跑它，比如 ci.yml 的
//     非 -short 步骤）；
//  3. 在 shortSkipExemptions 里有理由。
//
// 第 2 条故意只查「名字出现过」：换掉那一步的标题或整步删掉都会让它消失，方向是
// 误报红而不是误报绿。
func TestAcceptanceTableCoversShortSkippedTests(t *testing.T) {
	root := repoRoot(t)
	legs := acceptanceLegs(t, root)
	// 防空转①：表读不出腿，后面「零违规」就只是没遍历到东西。
	if len(legs) < 6 {
		t.Fatalf("只解析出 %d 条判据腿，明显低于表里现有的条数——表格式漂了，本闸门已空转", len(legs))
	}

	pkgRegexes := map[string][]*regexp.Regexp{}
	pkgDirs := map[string]string{}
	for _, l := range legs {
		key := l.dir + "|" + l.pkg
		if _, ok := pkgDirs[key]; !ok {
			pkgDirs[key] = filepath.Join(root, filepath.FromSlash(l.dir), filepath.FromSlash(strings.TrimPrefix(l.pkg, "./")))
			pkgRegexes[key] = nil
		}
		re, err := regexp.Compile(l.regex)
		if err != nil {
			t.Fatalf("判据腿 %s 的正则编译失败: %v", key, err)
		}
		pkgRegexes[key] = append(pkgRegexes[key], re)
	}

	workflows := ""
	err := filepath.WalkDir(filepath.Join(root, ".github", "workflows"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yml") {
			return err
		}
		b, err := readText(p)
		if err != nil {
			return err
		}
		workflows += string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("读 workflows: %v", err)
	}
	// 防空转②：CI 文本读空了，第 2 条判据会退化成「全部违规」；更要紧的是它可能
	// 被误当成闸门坏了而被反向放宽。
	if !strings.Contains(workflows, "go test") {
		t.Fatal("没读到任何含 go test 的 CI 文本——第 2 条判据失去依据")
	}

	var candidates, inTable, scheduled, exempt int
	var problems []string
	keys := make([]string, 0, len(pkgDirs))
	for k := range pkgDirs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, key := range keys {
		gated := shortGatedTests(t, pkgDirs[key])
		names := make([]string, 0, len(gated))
		for n := range gated {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			candidates++
			seen[key+"|"+name] = true
			matched := false
			for _, re := range pkgRegexes[key] {
				if re.MatchString(name) {
					matched = true
					break
				}
			}
			switch {
			case matched:
				inTable++
			case strings.Contains(workflows, name):
				scheduled++
			case shortSkipExemptions[key+"|"+name] != "":
				exempt++
			default:
				problems = append(problems, fmt.Sprintf("%s：这条在 -short 下会跳过，既没进判据表也没有 CI 作业点名——它现在没有任何执行证据", key+"|"+name))
			}
		}
	}
	// 防空转③：一个候选都没扫到，说明归因逻辑没落到文件上。
	if candidates < 3 {
		t.Fatalf("只扫到 %d 个 -short 跳过的用例（实测 6：codegen 2 + 示例 4），归因逻辑失效，本闸门已空转", candidates)
	}
	// 豁免必须是活的：用例改名/重新排进 CI 后，留在名单里的豁免等于把判据悄悄关掉。
	var stale []string
	for k, reason := range shortSkipExemptions {
		if !seen[k] {
			stale = append(stale, fmt.Sprintf("%s（理由：%s）", k, reason))
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("shortSkipExemptions 里有 %d 条已经不再是要豁免的用例，请删掉：\n  %s", len(stale), strings.Join(stale, "\n  "))
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Errorf("-short 跳过的用例落在两条口径之间（表覆盖包=%d，候选=%d，进表=%d，CI点名=%d，豁免=%d）：\n  %s",
			len(pkgDirs), candidates, inTable, scheduled, exempt, strings.Join(problems, "\n  "))
	}
	t.Logf("判据腿=%d 覆盖包=%d 候选=%d 进表=%d CI点名=%d 豁免=%d", len(legs), len(pkgDirs), candidates, inTable, scheduled, exempt)
}

// ---- §7.31：全局 MeterProvider 写入方必须与闸门表同条数 ----

// otelGlobalImport 是进程全局 MeterProvider 的写入口所在包。
const otelGlobalImport = "go.opentelemetry.io/otel"

// globalMeterGateKeys 从 pkg/appkit/provider.go 解析 globalMeterSegments 的字面量键。
// 读源码而不是 import：appkit 与两个适配器分属不同 module，import 即成模块环，
// 而这个变量是非导出的——计划期互斥闸门的「表」因此只能按文本派生。
func globalMeterGateKeys(t *testing.T, root string) map[string]bool {
	t.Helper()
	path := filepath.Join(root, "pkg", "appkit", "provider.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	keys := map[string]bool{}
	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "globalMeterSegments" || len(spec.Values) != 1 {
			return true
		}
		lit, ok := spec.Values[0].(*ast.CompositeLit)
		if !ok {
			t.Errorf("%s: globalMeterSegments 不是复合字面量——本闸门的派生姿势需跟着改", path)
			return false
		}
		found = true
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				t.Errorf("%s: globalMeterSegments 里有非键值元素", path)
				continue
			}
			lit, ok := kv.Key.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: globalMeterSegments 的键不是字符串字面量", path)
				continue
			}
			key, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Errorf("%s: 键 %s 解引用失败: %v", path, lit.Value, err)
				continue
			}
			if keys[key] {
				t.Errorf("%s: globalMeterSegments 里 %s 出现了两次", path, key)
			}
			keys[key] = true
		}
		return false
	})
	if !found {
		t.Fatalf("%s 里派生不到 globalMeterSegments——闸门表被改名或删掉时，本条判据必须响而不是空转", path)
	}
	if len(keys) == 0 {
		t.Fatalf("globalMeterSegments 为空——§7.31 的互斥闸门已失效")
	}
	return keys
}

// globalSymbolWriterDirs 派生「在进程全局位上动手」的包目录：非测试源码里以指向
// importPath 的包名（含 import alias）引用 symbol 的文件，按其所在包目录去重。
// wantCall=true 只认调用（`otel.SetMeterProvider(...)`），false 认任何取值
// （`prometheus.DefaultRegisterer` 是变量，传给 Register 才是那条路径）。
// 判 AST 而不是文本命中：仓里有三处注释也写着 SetMeterProvider，按 grep 计数会把
// 注释算成写入方（§7.28 那两条「不要按名字/目录猜」的判据卫生问题在这里同样成立）。
func globalSymbolWriterDirs(t *testing.T, root, importPath, symbol string, wantCall bool) map[string]bool {
	t.Helper()
	mods := derivedModules(root)
	if len(mods) < 50 {
		t.Fatalf("派生到 %d 个 module，明显低于仓库实际数量——谓词失效，本闸门已空转", len(mods))
	}
	all := make(map[string]bool, len(mods))
	for _, m := range mods {
		all[m] = true
	}
	fset := token.NewFileSet()
	dirs := map[string]bool{}
	for _, m := range mods {
		files, err := moduleGoFiles(root, m, all)
		if err != nil {
			t.Fatalf("scan %s: %v", m, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(f)), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", f, err)
			}
			// 该文件里指向 importPath 的包名（无 alias 时按 Go 规则取路径末段）。
			pkgNames := map[string]bool{}
			for _, imp := range file.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil || path != importPath {
					continue
				}
				if imp.Name != nil {
					pkgNames[imp.Name.Name] = true
					continue
				}
				pkgNames[filepath.Base(path)] = true
			}
			if len(pkgNames) == 0 {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				var expr *ast.SelectorExpr
				switch {
				case wantCall:
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					expr, ok = call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
				default:
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					expr = sel
				}
				if expr.Sel.Name != symbol {
					return true
				}
				pkgIdent, ok := expr.X.(*ast.Ident)
				if !ok || !pkgNames[pkgIdent.Name] {
					return true
				}
				dirs[filepath.ToSlash(filepath.Dir(f))] = true
				return true
			})
		}
	}
	return dirs
}

// prometheusClientImport 是 client_golang 的包路径，进程默认注册表位在这个包上。
const prometheusClientImport = "github.com/prometheus/client_golang/prometheus"

// globalMeterWriterDirs 派生「把进程全局 OTel MeterProvider 换成自己那一个」的包目录。
func globalMeterWriterDirs(t *testing.T, root string) map[string]bool {
	t.Helper()
	return globalSymbolWriterDirs(t, root, otelGlobalImport, "SetMeterProvider", true)
}

// TestGlobalMeterWritersAreInGateTable 是 §7.31 残余①「表按段路径点名，新写入方
// 不会自动进表」的闸门。
//
// 判的是条数而不是身份映射，这是如实的边界：段路径（metrics、instrumentation.otlp）
// 与包目录（metrics/otel、contrib/observability-otlp/metrics）之间没有可派生的联系，
// 所以「第三个写入方出现但没进表」和「进了表但配错段」这两种情形里，前者必红、后者判不出。
// 加它的理由是形状而非概率：§7.27 的契约化让「谁写全局」从业务侧手工调用变成了配置能
// 点亮的分支，而这个表是手写的——没有闸门时，它只会在新写入方**恰好被人想起来**时才更新。
func TestGlobalMeterWritersAreInGateTable(t *testing.T) {
	root := repoRoot(t)
	keys := globalMeterGateKeys(t, root)
	dirs := globalMeterWriterDirs(t, root)
	// 防空转：写入方一条都没派生到，说明遍历或调用识别没落到文件上，
	// 此时「条数相等」会因为 0==0 而恒绿。
	if len(dirs) < 2 {
		t.Fatalf("只派生到 %d 个全局 MeterProvider 写入方（实测 2 个）——谓词失效，本闸门已空转", len(dirs))
	}
	var writerList, keyList []string
	for d := range dirs {
		writerList = append(writerList, d)
	}
	for k := range keys {
		keyList = append(keyList, k)
	}
	sort.Strings(writerList)
	sort.Strings(keyList)
	if len(dirs) != len(keys) {
		t.Errorf("全局 MeterProvider 写入方有 %d 个 %v，但 pkg/appkit 的互斥闸门表只点名 %d 个段 %v——条数不等意味着新写入方没进表，两侧同时被契约装配点亮时仍然按顺序谁赢，§7.31 的缺陷形状回来了",
			len(dirs), writerList, len(keys), keyList)
	}
	t.Logf("写入方=%d %v，闸门表=%d %v", len(dirs), writerList, len(keys), keyList)
}

// TestDefaultRegistryWriterCountIsOne 把 §7.31 残余②「prometheus 默认注册表没有对偶闸门」
// 从一句话变成实测判据：仓内引用 `prometheus.DefaultRegisterer` 的非测试包**恰好一个**
// （现值 `metrics/prometheus`，由 `use_default_registry=true` 点亮）。
// 之所以不建互斥闸门，是因为根本没有第二个写入方可撞——这句话原来是断言，这条判据让它
// 变成会被机器复跑的断言。计数为 0 也判红而不是放行：那意味着这条能力被删了或 import
// 路径漂移，两者都要求同步登记册 §7.31 与 `metrics/prometheus` 的文档，而不是让它静默失效。
func TestDefaultRegistryWriterCountIsOne(t *testing.T) {
	root := repoRoot(t)
	dirs := globalSymbolWriterDirs(t, root, prometheusClientImport, "DefaultRegisterer", false)
	if len(dirs) != 1 {
		var list []string
		for d := range dirs {
			list = append(list, d)
		}
		sort.Strings(list)
		t.Fatalf("进程默认 Prometheus 注册表的仓内写入方数到 %d 个 %v（实测现值 1：`metrics/prometheus`）——为 0 说明这条能力或 import 路径已漂移，需要同步 §7.31 口径；≥2 说明出现了真正的第二个写入方，此时必须像 `globalMeterSegments` 那样给它建按段互斥闸门，而不是继续判红", len(dirs), list)
	}
	for d := range dirs {
		t.Logf("默认注册表写入方=%s（仓内唯一，无可撞对偶 ⇒ 不需要互斥闸门）", d)
	}
}
