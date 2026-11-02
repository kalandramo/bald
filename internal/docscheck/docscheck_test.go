// Package docscheck 守卫文档里相对链接的目标必须存在：文档漂移（引用了已
// 删除的文件/错误的相对路径）在 `go test ./...` 里直接暴露，而不是等读者
// 点开才发现 404。
package docscheck

import (
	"bytes"
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

// TestPkgModuleIsLeaf 钉住 §3.1 收口的两个条件：① 聚合根只剩本仓工具包；
// ② `pkg` module 不 import 任何归根 module 的包。本地 `replace` 让违反②的代码
// 照样能编译，扇入复辟在 CI 上是静默的——只有发版时才炸，所以必须在此断言。
func TestPkgModuleIsLeaf(t *testing.T) {
	root := repoRoot(t)
	mods := derivedModules(root)
	if !slices.Contains(mods, "pkg") {
		t.Fatal("pkg 不是独立 module：派生清单里没有 pkg/")
	}

	// ① 根 module 的包集合。
	wantRootPkgs := []string{"cmd/bald", "internal/codegen", "internal/docscheck", "internal/release"}
	gotRootPkgs := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
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
		for _, m := range mods {
			if m != "." && (slash == m || strings.HasPrefix(slash, m+"/")) {
				return nil
			}
		}
		gotRootPkgs[slash] = true
		return nil
	})
	if err != nil {
		t.Fatalf("scan root packages: %v", err)
	}
	var extra, missing []string
	for p := range gotRootPkgs {
		if !slices.Contains(wantRootPkgs, p) {
			extra = append(extra, p)
		}
	}
	for _, p := range wantRootPkgs {
		if !gotRootPkgs[p] {
			missing = append(missing, p)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		t.Errorf("聚合根里出现了预期外的包 %v：根 module 应只剩本仓工具（risk-review.md §3.1）", extra)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("聚合根里预期的包不见了 %v：判据已过期，请核对 §3.1 后更新本清单", missing)
	}

	// ② pkg 不得 import 根 module 的包。
	hits := 0
	pkgDir := filepath.Join(root, "pkg")
	err = filepath.WalkDir(pkgDir, func(path string, d os.DirEntry, err error) error {
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
		{"github.com/kalandramo/bald/internal/codegen", "."},
		{"github.com/you/other", ""},
	}
	for _, mods := range [][]string{
		{".", "log", "pkg", "transport", "transport/grpc"},
		{"transport/grpc", ".", "transport", "pkg", "log"},
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
