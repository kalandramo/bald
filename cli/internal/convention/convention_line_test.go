package convention

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kalandramo/bald/cli/internal/codegen"
)

// readErrFS 让 ReadDir 成功但 ReadFile 失败，覆盖 LoadFS 的读卡错误分支。
type readErrFS struct{ fstest.MapFS }

func (readErrFS) ReadFile(string) ([]byte, error) { return nil, errors.New("read boom") }

// TestLoadFS_EdgeBranches 用合成卡库覆盖 LoadFS/parseCard/validate 的可达错误与
// 跳过分支：无 cards 目录、仅非 .md（0 卡）、ReadFile 失败、孤儿列表项、缺 id、
// 缺 title 报错；front-matter 空行走 continue 跳过（不报错）。
func TestLoadFS_EdgeBranches(t *testing.T) {
	// 无 cards 目录 → ReadDir 报错。
	if _, err := LoadFS(fstest.MapFS{}); err == nil || !strings.Contains(err.Error(), "read cards dir") {
		t.Errorf("no cards dir: err=%v, want read cards dir", err)
	}
	// 仅非 .md 文件 → 跳过 → 0 卡报错（同时覆盖 continue 与 len==0）。
	if _, err := LoadFS(fstest.MapFS{"cards/readme.txt": &fstest.MapFile{Data: []byte("x")}}); err == nil || !strings.Contains(err.Error(), "no cards found") {
		t.Errorf("only non-md: err=%v, want no cards found", err)
	}
	// ReadFile 失败。
	if _, err := LoadFS(readErrFS{fstest.MapFS{"cards/demo.md": &fstest.MapFile{Data: []byte("x")}}}); err == nil || !strings.Contains(err.Error(), "read card") {
		t.Errorf("readfile err: err=%v, want read card", err)
	}

	cases := map[string]string{
		"orphan list item": "---\nid: demo\ntitle: 演示\nlevel: 强制\ncategory: core\n  - 孤儿项\n---\n- [demo-r01] [强制] 规则。\n",
		"missing id":       "---\ntitle: 演示\nlevel: 强制\ncategory: core\n---\n- [demo-r01] [强制] 规则。\n",
		"missing title":    "---\nid: demo\nlevel: 强制\ncategory: core\n---\n- [demo-r01] [强制] 规则。\n",
	}
	for name, content := range cases {
		fsys := fstest.MapFS{"cards/demo.md": &fstest.MapFile{Data: []byte(content)}}
		if _, err := LoadFS(fsys); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}

	// front-matter 里的空行没有冒号 → parseCard 走 continue 跳过，卡仍合法。
	blank := "---\nid: demo\ntitle: 演示\nlevel: 强制\ncategory: core\n\n---\n- [demo-r01] [强制] 规则。\n"
	cards, err := LoadFS(fstest.MapFS{"cards/demo.md": &fstest.MapFile{Data: []byte(blank)}})
	if err != nil || len(cards) != 1 {
		t.Errorf("blank fm line should parse: cards=%d err=%v", len(cards), err)
	}
}

// writeProjectCard 在 dir/.bald/conventions 下写一个文件（用于 LoadAll 错误分支）。
func writeProjectCard(t *testing.T, dir, name, content string) {
	t.Helper()
	local := filepath.Join(dir, ProjectCardsDir)
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadAll_ReadDirError .bald/conventions 是普通文件 → ReadDir 非 notexist 错误。
func TestLoadAll_ReadDirError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".bald"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ProjectCardsDir), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAll(dir); err == nil || !strings.Contains(err.Error(), "read ") {
		t.Errorf("conventions-as-file: err=%v, want read error", err)
	}
}

// TestLoadAll_SkipNonMarkdown 目录里的非 .md 文件被跳过，集合退化为基线。
func TestLoadAll_SkipNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	writeProjectCard(t, dir, "notes.txt", "ignored")
	cards, err := LoadAll(dir)
	if err != nil || len(cards) != 25 {
		t.Errorf("non-md skip: cards=%d err=%v, want 25 baseline", len(cards), err)
	}
}

// TestLoadAll_ReadFileError .md 条目是个目录 → ReadFile 失败。
func TestLoadAll_ReadFileError(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, ProjectCardsDir)
	if err := os.MkdirAll(filepath.Join(local, "bad.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAll(dir); err == nil || !strings.Contains(err.Error(), "read project card") {
		t.Errorf("md-as-dir: err=%v, want read project card", err)
	}
}

// TestLoadAll_ParseError 损坏的项目卡 → parseCard 报错。
func TestLoadAll_ParseError(t *testing.T) {
	dir := t.TempDir()
	writeProjectCard(t, dir, "bad.md", "没有 front-matter 的坏卡")
	if _, err := LoadAll(dir); err == nil || !strings.Contains(err.Error(), "project card") {
		t.Errorf("corrupt card: err=%v, want project card error", err)
	}
}

// TestCommands_LoadAllError 项目卡损坏时 overview/list/get 的 RunE 经 LoadAll 报错。
func TestCommands_LoadAllError(t *testing.T) {
	dir := t.TempDir()
	writeProjectCard(t, dir, "bad.md", "坏卡")
	t.Chdir(dir)
	for _, sub := range []string{"overview", "list", "get"} {
		args := []string{sub}
		if sub == "get" {
			args = append(args, "judgments")
		}
		if _, _, err := drive(t, args...); err == nil {
			t.Errorf("%s with corrupt project card should error", sub)
		}
	}
}

// TestCheckCmd_ErrorAndRules check 的 runCheck 错误分支（非法 min-level）与
// PreRunE 的 --rules 切分分支。
func TestCheckCmd_ErrorAndRules(t *testing.T) {
	if _, _, err := drive(t, "check", "--min-level", "bogus"); err == nil {
		t.Errorf("check --min-level bogus should error")
	}
	if _, _, err := drive(t, "check", "--rules", "codegen-r03"); err != nil {
		t.Errorf("check --rules: %v", err)
	}
}

// genAppTree 在临时目录生成一套干净的 owned 应用树（经 codegen 公开命令），
// 返回该目录。供 checkGenAppDrift / runCheck 的干净与漂移分支使用。
func genAppTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	specPath := filepath.Join(dir, "appspec.json")
	spec := `{
  "meta": {"name": "conv-demo", "module": "example.com/conv-demo", "desc": "d"},
  "bootstrap": {"audit": {"backends": ["log"]}}
}`
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	streams := codegen.NewDefaultIOStreams()
	streams.Out, streams.ErrOut = io.Discard, io.Discard // 夹具生成不污染测试输出
	cmd := codegen.NewCommand(streams)
	cmd.SetArgs([]string{"app", "--spec", specPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("generate fixture tree: %v", err)
	}
	return dir
}

// TestCheckGenAppDrift_Branches stat 非 notexist 错误（ENOTDIR）、干净树（nil
// finding）、漂移树（删 owned 文件 → finding）三条分支。
func TestCheckGenAppDrift_Branches(t *testing.T) {
	// ENOTDIR：dir 是个普通文件 ⇒ dir/appspec.json stat 返回非 notexist 错误。
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := checkGenAppDrift(f); err == nil || !strings.Contains(err.Error(), "stat ") {
		t.Errorf("ENOTDIR stat: err=%v, want stat error", err)
	}

	// 干净树：零 finding、无 skip、无错误。
	clean := genAppTree(t)
	findings, skip, err := checkGenAppDrift(clean)
	if err != nil || skip != "" || len(findings) != 0 {
		t.Fatalf("clean tree: findings=%v skip=%q err=%v", findings, skip, err)
	}

	// 漂移树：删掉一个 owned 产物 ⇒ 一条绑定 codegen-r03 的 finding。
	if err := os.Remove(filepath.Join(clean, "cmd", "conv-demo", "main.go")); err != nil {
		t.Fatal(err)
	}
	findings, skip, err = checkGenAppDrift(clean)
	if err != nil || skip != "" || len(findings) != 1 {
		t.Fatalf("drifted tree: findings=%v skip=%q err=%v", findings, skip, err)
	}
	if findings[0].RuleID != "codegen-r03" || findings[0].File != "appspec.json" {
		t.Errorf("finding = %+v, want codegen-r03 / appspec.json", findings[0])
	}
}

// TestRunCheck_CheckerPaths 覆盖 runCheck 的 checker 错误传递、OK 与 Found 汇总、
// 以及 levelRank 的建议级（return 1）分支。
func TestRunCheck_CheckerPaths(t *testing.T) {
	// checker 错误（ENOTDIR Dir）→ runCheck 传递错误。
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCheck(checkOptions{Dir: f, MinLevel: "mandatory"}); err == nil || !strings.Contains(err.Error(), "checker gen-app-drift") {
		t.Errorf("checker error: err=%v, want checker gen-app-drift", err)
	}

	// 干净树 → OK 汇总；漂移树 → Found 汇总。
	clean := genAppTree(t)
	res, err := runCheck(checkOptions{Dir: clean, MinLevel: "mandatory"})
	if err != nil || len(res.OK) != 1 || len(res.Found) != 0 {
		t.Fatalf("clean runCheck: ok=%v found=%v err=%v", res.OK, res.Found, err)
	}
	if err := os.Remove(filepath.Join(clean, "cmd", "conv-demo", "main.go")); err != nil {
		t.Fatal(err)
	}
	res, err = runCheck(checkOptions{Dir: clean, MinLevel: "mandatory"})
	if err != nil || len(res.Found) != 1 {
		t.Fatalf("drifted runCheck: found=%v err=%v", res.Found, err)
	}

	// 建议级 min-level → levelRank 的 return 1 分支（cwd 无 appspec ⇒ skip）。
	if _, err := runCheck(checkOptions{Dir: ".", MinLevel: "recommended"}); err != nil {
		t.Errorf("recommended min-level: %v", err)
	}
}

// 结构性不可达分支（如实留空，不为绿造假）：
//   - runCheck 的 Load 错误 / unknown-checker / min-level-skip：内嵌卡集只有
//     codegen 一张卡声明 checker 且级别为强制，三条分支经公开 API 不可达。
//   - LoadAll 的 Load 错误、规则 id 冲突：内嵌卡恒合法、卡 id 全库唯一 ⇒ 规则 id
//     天然唯一（parseCard 强制前缀=卡 id）。
//   - getCmd 的 marshalCard 错误：json.Marshal 作用于已知类型恒成功。
// 这些与 os.Exit 半边不同（后者按 owner 决策走 re-exec 真测，见
// convention_reexec_test.go 的 check --fail-on-findings 子进程测试）。
