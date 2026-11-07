package convention

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// TestLoadEmbeddedCards 校验内嵌卡库的整体形状：六张首版卡、规则 ID 前缀与
// 唯一性（Load 内建校验）、codegen 卡声明 gen-app-drift。
func TestLoadEmbeddedCards(t *testing.T) {
	cards, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cards) != 16 {
		t.Fatalf("expected 16 cards, got %d: %v", len(cards), cardIDs(cards))
	}
	byID := map[string]Card{}
	for _, c := range cards {
		byID[c.ID] = c
		for _, r := range c.Rules {
			if !strings.HasPrefix(r.ID, c.ID+"-r") {
				t.Errorf("card %s: rule %s lacks card prefix", c.ID, r.ID)
			}
			if r.Line <= 0 {
				t.Errorf("card %s: rule %s has no line number", c.ID, r.ID)
			}
		}
	}
	j := byID["judgments"]
	if len(j.Rules) != 9 {
		t.Errorf("judgments: expected 9 rules (J1–J9), got %d", len(j.Rules))
	}
	for _, r := range j.Rules {
		if r.Level != LevelMandatory {
			t.Errorf("judgments: rule %s level = %s, want 强制", r.ID, r.Level)
		}
	}
	cg := byID["codegen"]
	if len(cg.Checks) != 1 || cg.Checks[0] != "gen-app-drift" {
		t.Errorf("codegen: checks = %v, want [gen-app-drift]", cg.Checks)
	}
	if byID["catalog"].Level != LevelRecommended {
		t.Errorf("catalog: level = %s, want 建议", byID["catalog"].Level)
	}
}

// TestLoadFSRejectsBrokenCards 用合成卡逐项校验解析器的拒绝路径：front-matter
// 缺失/未闭合、id 与文件名不符、级别非法、规则 ID 前缀不匹配、正文无规则。
func TestLoadFSRejectsBrokenCards(t *testing.T) {
	good := "---\nid: demo\ntitle: 演示\nlevel: 强制\ncategory: core\nsources: []\nchecks: []\n---\n- [demo-r01] [强制] 规则一条。\n"
	cases := map[string]string{
		"no front-matter":   "没有围栏的普通 markdown",
		"unclosed fm":       "---\nid: demo\nlevel: 强制\ncategory: core\n",
		"id mismatch":       "---\nid: other\ntitle: 演示\nlevel: 强制\ncategory: core\n---\n- [other-r01] [强制] 规则。\n",
		"bad level":         "---\nid: demo\ntitle: 演示\nlevel: 必须\ncategory: core\n---\n- [demo-r01] [强制] 规则。\n",
		"bad category":      "---\nid: demo\ntitle: 演示\nlevel: 强制\ncategory: misc\n---\n- [demo-r01] [强制] 规则。\n",
		"rule prefix wrong": "---\nid: demo\ntitle: 演示\nlevel: 强制\ncategory: core\n---\n- [other-r01] [强制] 规则。\n",
		"no rules":          "---\nid: demo\ntitle: 演示\nlevel: 强制\ncategory: core\n---\n正文没有规则行。\n",
	}
	for name, content := range cases {
		fsys := fstest.MapFS{"cards/demo.md": &fstest.MapFile{Data: []byte(content)}}
		if _, err := LoadFS(fsys); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
	// 正例兜底：同一张合法卡能过（sources/checks 空列表头不炸解析器）。
	fsys := fstest.MapFS{"cards/demo.md": &fstest.MapFile{Data: []byte(good)}}
	if _, err := LoadFS(fsys); err != nil {
		t.Errorf("good card rejected: %v", err)
	}
}

// TestRenderOverview 总览必须包含全部卡 id、取用方式与级别语义（AI 的入口契约）。
func TestRenderOverview(t *testing.T) {
	cards, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := renderOverview(cards)
	for _, c := range cards {
		if !strings.Contains(out, c.ID) {
			t.Errorf("overview missing card %s", c.ID)
		}
	}
	for _, want := range []string{"bald_convention_get", "bald convention get", "写代码前先查规范", "[强制]", "代码评审不可替代"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q", want)
		}
	}
}

// TestGetCard 未命中时错误信息必须带候选清单（AI 自纠的依据）。
func TestGetCard(t *testing.T) {
	cards, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := getCard(cards, "nope"); err == nil || !strings.Contains(err.Error(), "judgments") {
		t.Errorf("getCard(nope) error = %v, want candidates in message", err)
	}
}

// TestRunCheckFilters 校验 runCheck 的两个过滤维度：--rules 只放行绑定的
// checker；--min-level 的非法值报错。测试 cwd（cli/internal/convention）没有
// appspec.json，gen-app-drift 走 skip 分支——不依赖仓库布局。
func TestRunCheckFilters(t *testing.T) {
	res, err := runCheck(checkOptions{Dir: ".", MinLevel: "mandatory", Rules: []string{"judgments-r01"}})
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if len(res.OK) != 0 || len(res.Found) != 0 {
		t.Errorf("rules filter: ok=%v found=%v, want empty", res.OK, res.Found)
	}
	if len(res.Skipped) == 0 {
		t.Errorf("rules filter: expected gen-app-drift skipped")
	}
	if _, err := runCheck(checkOptions{Dir: ".", MinLevel: "bogus"}); err == nil {
		t.Errorf("bogus min-level: expected error")
	}
	// 全量跑一次（无 appspec → skip）：完整性通过、零 finding。
	res, err = runCheck(checkOptions{Dir: ".", MinLevel: "reference"})
	if err != nil {
		t.Fatalf("runCheck full: %v", err)
	}
	if len(res.Found) != 0 {
		t.Errorf("full run: findings = %v, want none", res.Found)
	}
}

// TestPrintCheckFormats text 模式 finding 走 file:line: [rule] 前缀；json 模式
// stdout 是纯 findings 数组、状态摘要进 ErrOut。
func TestPrintCheckFormats(t *testing.T) {
	res := &checkResult{
		Cards: 6, Rules: 45, OK: []string{"gen-app-drift"},
		Found: []Finding{{RuleID: "codegen-r03", Level: LevelMandatory, File: "appspec.json", Line: 0,
			Message: "漂移", Suggestion: "重新生成"}},
	}
	var out, errOut bytes.Buffer
	if fail := printCheck(res, checkOptions{Format: "json"}, Streams{Out: &out, ErrOut: &errOut}); fail {
		t.Errorf("json without FailOnFindings should not fail")
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "[]") && !strings.Contains(out.String(), `"rule_id"`) {
		t.Errorf("json stdout should be a findings array, got %q", out.String())
	}
	if !strings.Contains(errOut.String(), "findings: 1") {
		t.Errorf("json stderr should carry summary, got %q", errOut.String())
	}

	out.Reset()
	if fail := printCheck(res, checkOptions{Format: "text", FailOnFindings: true}, Streams{Out: &out, ErrOut: &errOut}); !fail {
		t.Errorf("text with FailOnFindings and 1 finding should fail")
	}
	if !strings.Contains(out.String(), "appspec.json:0: [codegen-r03] 漂移") {
		t.Errorf("text finding line malformed: %q", out.String())
	}
}

// TestLoadAllProjectCards 项目级卡：.bald/conventions/ 下的合法卡并入集合；
// 卡 id 或规则 id 与基线冲突报错（补充不是覆盖）；目录缺失静默退化为基线。
func TestLoadAllProjectCards(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, ProjectCardsDir)
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	valid := "---\nid: team\ntitle: 团队约定\nlevel: 强制\ncategory: core\nsources: []\nchecks: []\n---\n- [team-r01] [强制] handler 里不允许直接使用 time.Now，必须注入时钟。\n"
	if err := os.WriteFile(filepath.Join(local, "team.md"), []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	cards, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	got := map[string]bool{}
	for _, c := range cards {
		got[c.ID] = true
	}
	if len(cards) != 17 || !got["team"] || !got["judgments"] {
		t.Errorf("LoadAll: got %v (%d cards), want 16 baseline + team", cardIDs(cards), len(cards))
	}

	// 卡 id 冲突（id 必须与文件名 stem 一致，冲突文件名取 reuse.md 才能走到
	// LoadAll 的冲突检查而非 parseCard 的 stem 校验；规则前缀也要同步换）。
	dup := strings.Replace(valid, "id: team", "id: reuse", 1)
	dup = strings.Replace(dup, "[team-r01]", "[reuse-r01]", 1)
	if err := os.WriteFile(filepath.Join(local, "reuse.md"), []byte(dup), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAll(dir); err == nil || !strings.Contains(err.Error(), "supplement, never shadow") {
		t.Errorf("duplicate card id: err = %v, want shadow error", err)
	}
	if err := os.Remove(filepath.Join(local, "reuse.md")); err != nil {
		t.Fatal(err)
	}

	// 规则 id 冲突经公开 API 不可达：parseCard 强制规则前缀 = 卡 id，卡 id 又
	// 全库唯一 ⇒ 规则 id 天然唯一。LoadAll 里的规则查重是纵深防御，不写测试。

	// 目录缺失：退化基线。
	if cards, err := LoadAll(filepath.Join(dir, "nonexistent")); err != nil || len(cards) != 16 {
		t.Errorf("missing project dir: cards=%d err=%v, want 16 baseline", len(cards), err)
	}
}

func cardIDs(cards []Card) []string {
	ids := make([]string, 0, len(cards))
	for _, c := range cards {
		ids = append(ids, c.ID)
	}
	return ids
}
