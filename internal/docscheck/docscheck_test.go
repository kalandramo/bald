// Package docscheck 守卫文档里相对链接的目标必须存在：文档漂移（引用了已
// 删除的文件/错误的相对路径）在 `go test ./...` 里直接暴露，而不是等读者
// 点开才发现 404。
package docscheck

import (
	"bytes"
	"fmt"
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
