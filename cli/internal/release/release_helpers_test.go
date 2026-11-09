package release

import (
	"strings"
	"testing"
)

// 纯函数行级补全：变更日志文本折算与版本分级是确定性口径，表驱动锁住每条
// 分支，无需 git 夹具。

func TestHelpers_PlanDate(t *testing.T) {
	cases := map[string]string{
		"2026-11-09T12:00:00Z": "2026-11-09",
		"short":                "short",
		"":                     "",
	}
	for in, want := range cases {
		if got := planDate(&Plan{GeneratedAt: in}); got != want {
			t.Errorf("planDate(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestHelpers_SplitDateSection(t *testing.T) {
	cases := []struct{ in, heading, body string }{
		{"## 2026-11-09\n\n### x v1\n", "## 2026-11-09", "### x v1"},
		{"plain text", "", "plain text"},
		{"## only", "", "## only"},
		{"", "", ""},
	}
	for _, c := range cases {
		h, b := splitDateSection(c.in)
		if h != c.heading || b != c.body {
			t.Errorf("splitDateSection(%q)=(%q,%q), want (%q,%q)", c.in, h, b, c.heading, c.body)
		}
	}
}

func TestHelpers_DateHeadingIndex(t *testing.T) {
	cases := []struct {
		existing, heading string
		want              int
	}{
		{"anything", "", -1},
		{"## d\nrest", "## d", 0},
		{"intro\n## d\nrest", "## d", 6},
		{"no match here", "## d", -1},
	}
	for _, c := range cases {
		if got := dateHeadingIndex(c.existing, c.heading); got != c.want {
			t.Errorf("dateHeadingIndex(%q,%q)=%d, want %d", c.existing, c.heading, got, c.want)
		}
	}
}

func TestHelpers_InsertSections(t *testing.T) {
	sections := "## 2026-11-09\n\n### cli v1.0.0\n\n- `abcd1234` feat: x\n"

	// 空文件 → 头部说明 + 新节。
	if got := insertSections("", sections); !strings.HasPrefix(got, changelogHeader) || !strings.Contains(got, "### cli v1.0.0") {
		t.Errorf("empty existing: %q", got)
	}
	// 同日合并（标题在开头，其后无内容 → rest 为空）。
	if got := insertSections("## 2026-11-09\n", sections); strings.Count(got, "## 2026-11-09") != 1 {
		t.Errorf("same-day merge (empty rest) must keep one heading: %q", got)
	}
	// 同日合并（标题后有几余内容 → rest 非空，接在新节之后）。
	got := insertSections("## 2026-11-09\n\n### old v0.9.0\n", sections)
	if strings.Count(got, "## 2026-11-09") != 1 || !strings.Contains(got, "### old v0.9.0") {
		t.Errorf("same-day merge (rest) must keep heading and old section: %q", got)
	}
	// 新日期 → 插在首个历史日期节之前。
	got = insertSections("# 变更日志\n\n## 2026-01-01\n\n### old v0.1.0\n", sections)
	if !strings.Contains(got, "## 2026-01-01") || strings.Index(got, "## 2026-11-09") > strings.Index(got, "## 2026-01-01") {
		t.Errorf("new date must prepend before history: %q", got)
	}
	// 无任何日期标题 → 追加到末尾。
	got = insertSections("intro without heading\n", sections)
	if !strings.HasSuffix(got, strings.TrimRight(sections, "\n")+"\n") || !strings.Contains(got, "intro without heading") {
		t.Errorf("no heading must append: %q", got)
	}
}

func TestHelpers_RenderSections(t *testing.T) {
	plan := &Plan{
		GeneratedAt: "2026-11-09T00:00:00Z",
		Changes: []Change{{
			Dir: "cli", Next: "v1.0.0",
			Commits: []Commit{
				{Hash: "abcdefgh1234", Subject: "feat: add", Breaking: true},
				{Hash: "short", Subject: "fix: bug"},
			},
		}},
	}
	got := renderSections("/repo", plan)
	if !strings.Contains(got, "## 2026-11-09") || !strings.Contains(got, "### cli v1.0.0") {
		t.Errorf("headings missing: %q", got)
	}
	if !strings.Contains(got, "- `abcdefgh` feat: add **(breaking)**") {
		t.Errorf("breaking marker + short hash missing: %q", got)
	}
	if !strings.Contains(got, "- `short` fix: bug\n") || strings.Contains(got, "fix: bug **(breaking)**") {
		t.Errorf("non-breaking commit must not carry marker: %q", got)
	}
}

func TestHelpers_ShortHash(t *testing.T) {
	cases := map[string]string{
		"abcdefgh1234": "abcdefgh",
		"12345678":     "12345678",
		"short":        "short",
		"":             "",
	}
	for in, want := range cases {
		if got := shortHash(in); got != want {
			t.Errorf("shortHash(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestHelpers_ChangelogLabel(t *testing.T) {
	if got := changelogLabel("/x/myrepo", Change{Dir: "."}); got != "myrepo" {
		t.Errorf("root label=%q, want myrepo", got)
	}
	if got := changelogLabel("/x/myrepo", Change{Dir: "cli"}); got != "cli" {
		t.Errorf("subdir label=%q, want cli", got)
	}
}

func TestHelpers_Classify(t *testing.T) {
	cases := []struct {
		name    string
		commits []Commit
		current string
		want    string
	}{
		{"major breaking", []Commit{{Breaking: true}}, "v1.2.3", "major"},
		{"0.x breaking is minor", []Commit{{Breaking: true}}, "v0.2.3", "minor"},
		{"feat", []Commit{{Type: "feat"}}, "v1.0.0", "minor"},
		{"non-conventional", []Commit{{Type: ""}}, "v1.0.0", "patch"},
		{"docs only", []Commit{{Type: "docs"}}, "v1.0.0", ""},
		{"fix default", []Commit{{Type: "fix"}}, "v1.0.0", "patch"},
		{"highest wins", []Commit{{Type: "docs"}, {Type: "feat"}}, "v1.0.0", "minor"},
		{"empty", nil, "v1.0.0", ""},
	}
	for _, c := range cases {
		if got := classify(c.commits, c.current); got != c.want {
			t.Errorf("%s: classify=%q, want %q", c.name, got, c.want)
		}
	}
}

func TestHelpers_NextVersion(t *testing.T) {
	// 未发布 + 无提交 → 不发。
	if bump, next := (&Module{Latest: ""}).nextVersion(nil); bump != "" || next != "" {
		t.Errorf("unpublished no commits: (%q,%q), want empty", bump, next)
	}
	// 未发布 + 有提交 → initial v0.1.0。
	if bump, next := (&Module{Latest: ""}).nextVersion([]Commit{{Type: "feat"}}); bump != "initial" || next != "v0.1.0" {
		t.Errorf("unpublished with commits: (%q,%q), want (initial,v0.1.0)", bump, next)
	}
	// 已发布 + 纯 docs → 不发。
	if bump, next := (&Module{Latest: "v1.0.0"}).nextVersion([]Commit{{Type: "docs"}}); bump != "" || next != "" {
		t.Errorf("docs only: (%q,%q), want empty", bump, next)
	}
	// 已发布 + feat → minor。
	if bump, next := (&Module{Latest: "v1.2.3"}).nextVersion([]Commit{{Type: "feat"}}); bump != "minor" || next != "v1.3.0" {
		t.Errorf("feat bump: (%q,%q), want (minor,v1.3.0)", bump, next)
	}
}

func TestHelpers_ZipPathElemOK(t *testing.T) {
	cases := map[string]bool{
		"":            false,
		"abc123":      true,
		"my-pkg_v1.0": true,
		"a b":         true, // 空格在 zipFilePunct 白名单内
		"a/b":         false,
		"a:b":         false,
		"café":        true,  // 非 ASCII 字母放行
		"（":           false, // 全角括号不是字母
		"123":         true,
	}
	for in, want := range cases {
		if got := zipPathElemOK(in); got != want {
			t.Errorf("zipPathElemOK(%q)=%v, want %v", in, got, want)
		}
	}
}
