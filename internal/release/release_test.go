package release

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRepo 建一个最小多模块仓库：根模块 + sub 子模块，两者互相 require。
// 测试只依赖本机 git，不需要网络。
func newRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "test")
	runGit(t, root, "config", "commit.gpgsign", "false")
	writeFile(t, root, "go.mod", `module example.com/m

go 1.22

require example.com/m/sub v0.1.0
`)
	writeFile(t, root, "root.go", "package m\n")
	writeFile(t, root, "sub/go.mod", `module example.com/m/sub

go 1.22

require example.com/m v0.1.0
`)
	writeFile(t, root, "sub/sub.go", "package sub\n")
	commit(t, root, "feat: initial", "")
	runGit(t, root, "tag", "v0.1.0")
	runGit(t, root, "tag", "sub/v0.1.0")
	return root
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, root, subject, body string) {
	t.Helper()
	runGit(t, root, "add", "-A")
	args := []string{"commit", "-m", subject}
	if body != "" {
		args = append(args, "-m", body)
	}
	runGit(t, root, args...)
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

func planFor(t *testing.T, root string) *Plan {
	t.Helper()
	repo, err := Discover(root)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	plan, err := repo.BuildPlan(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	return plan
}

func changeByDir(t *testing.T, plan *Plan, dir string) *Change {
	t.Helper()
	for i := range plan.Changes {
		if plan.Changes[i].Dir == dir {
			return &plan.Changes[i]
		}
	}
	t.Fatalf("no change for dir %q in %+v", dir, plan.Changes)
	return nil
}

func subjects(commits []Commit) []string {
	out := make([]string, len(commits))
	for i, c := range commits {
		out[i] = c.Subject
	}
	return out
}

func TestBuildPlan_BumpOwnershipAndRequires(t *testing.T) {
	root := newRepo(t)

	writeFile(t, root, "root.go", "package m\n\n// root fix\n")
	commit(t, root, "fix: root bug", "")
	writeFile(t, root, "sub/sub.go", "package sub\n\n// feature\n")
	commit(t, root, "feat: sub feature", "")
	writeFile(t, root, "sub/README.md", "docs\n")
	commit(t, root, "docs: sub docs", "")

	plan := planFor(t, root)
	if len(plan.Changes) != 2 {
		t.Fatalf("changes = %d, want 2: %+v", len(plan.Changes), plan.Changes)
	}

	rootChange := changeByDir(t, plan, ".")
	if rootChange.Current != "v0.1.0" || rootChange.Next != "v0.1.1" || rootChange.Bump != "patch" {
		t.Errorf("root change = %+v, want v0.1.0 -> v0.1.1 patch", rootChange)
	}
	if got := subjects(rootChange.Commits); len(got) != 1 || got[0] != "fix: root bug" {
		t.Errorf("root commits = %v, want only root fix (submodule files must not leak)", got)
	}

	subChange := changeByDir(t, plan, "sub")
	if subChange.Current != "sub/v0.1.0" || subChange.Next != "v0.2.0" || subChange.Tag != "sub/v0.2.0" || subChange.Bump != "minor" {
		t.Errorf("sub change = %+v, want sub/v0.1.0 -> v0.2.0 minor", subChange)
	}
	if got := subjects(subChange.Commits); len(got) != 2 ||
		got[0] != "docs: sub docs" || got[1] != "feat: sub feature" {
		t.Errorf("sub commits = %v, want docs + feat", got)
	}

	// 两个模块都发版，互相的 require 都要归位到下一版本。
	if len(rootChange.Requires) != 1 || rootChange.Requires[0].Module != "example.com/m/sub" || rootChange.Requires[0].To != "v0.2.0" {
		t.Errorf("root requires = %+v", rootChange.Requires)
	}
	if len(subChange.Requires) != 1 || subChange.Requires[0].Module != "example.com/m" || subChange.Requires[0].To != "v0.1.1" {
		t.Errorf("sub requires = %+v", subChange.Requires)
	}
}

func TestBuildPlan_NoReleaseForDocsOnlyModule(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "sub/README.md", "docs only\n")
	commit(t, root, "docs: sub docs", "")
	writeFile(t, root, "root.go", "package m\n\n// fix\n")
	commit(t, root, "fix: root bug", "")

	plan := planFor(t, root)
	if len(plan.Changes) != 1 {
		t.Fatalf("changes = %d, want root only: %+v", len(plan.Changes), plan.Changes)
	}
	if plan.Changes[0].Dir != "." {
		t.Errorf("unexpected change %+v", plan.Changes[0])
	}
}

func TestBuildPlan_BreakingRules(t *testing.T) {
	root := newRepo(t)
	runGit(t, root, "tag", "-d", "v0.1.0")
	runGit(t, root, "tag", "v1.2.3") // 根模块按 1.x 规则升 major

	writeFile(t, root, "root.go", "package m\n\n// break\n")
	writeFile(t, root, "sub/sub.go", "package sub\n\n// break\n")
	commit(t, root, "refactor!: drop legacy api", "")

	plan := planFor(t, root)
	if got := changeByDir(t, plan, "."); got.Next != "v2.0.0" || got.Bump != "major" {
		t.Errorf("root = %s/%s, want v2.0.0 major", got.Next, got.Bump)
	}
	if got := changeByDir(t, plan, "sub"); got.Next != "v0.2.0" || got.Bump != "minor" {
		t.Errorf("sub = %s/%s, want v0.2.0 minor (0.x breaking)", got.Next, got.Bump)
	}
}

func TestCommitParsing(t *testing.T) {
	typ, breaking := parseSubject("feat(api)!: add endpoint")
	if typ != "feat" || !breaking {
		t.Errorf("parseSubject = %q/%v", typ, breaking)
	}
	if typ, breaking := parseSubject("not conventional"); typ != "" || breaking {
		t.Errorf("non-conventional = %q/%v", typ, breaking)
	}
	if !hasBreakingFooter("details\n\nBREAKING CHANGE: removed X") {
		t.Error("BREAKING CHANGE footer not detected")
	}
	if hasBreakingFooter("ordinary body") {
		t.Error("false positive breaking footer")
	}
}

func TestSetRequireVersion(t *testing.T) {
	block := "module example.com/m\n\nrequire (\n\texample.com/dep v0.1.0 // indirect\n\tother.example/x v1.0.0\n)\n"
	got, err := setRequireVersion(block, "example.com/dep", "v0.1.0", "v0.2.0")
	if err != nil {
		t.Fatalf("block update: %v", err)
	}
	if !strings.Contains(got, "example.com/dep v0.2.0 // indirect") {
		t.Errorf("block update result:\n%s", got)
	}

	single := "module example.com/m\n\nrequire example.com/dep v0.1.0\n"
	got, err = setRequireVersion(single, "example.com/dep", "v0.1.0", "v0.2.0")
	if err != nil {
		t.Fatalf("single-line update: %v", err)
	}
	if !strings.Contains(got, "require example.com/dep v0.2.0") {
		t.Errorf("single-line update result:\n%s", got)
	}

	if again, err := setRequireVersion(got, "example.com/dep", "v0.1.0", "v0.2.0"); err != nil || again != got {
		t.Errorf("already-at-target must be a no-op, got %v / %q", err, again)
	}
	if _, err := setRequireVersion(single, "example.com/dep", "v0.0.1", "v0.2.0"); err == nil {
		t.Error("stale version must fail instead of silently skipping")
	}
	if _, err := setRequireVersion(single, "example.com/missing", "v0.1.0", "v0.2.0"); err == nil {
		t.Error("missing require must fail")
	}
}

func TestSync_WritesRequiresAndIsIdempotent(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "root.go", "package m\n\n// fix\n")
	commit(t, root, "fix: root bug", "")
	writeFile(t, root, "sub/sub.go", "package sub\n\n// feature\n")
	commit(t, root, "feat: sub feature", "")

	plan := planFor(t, root)

	var out bytes.Buffer
	if err := Sync(root, plan, false, &out); err != nil {
		t.Fatalf("dry-run sync: %v", err)
	}
	if !strings.Contains(out.String(), "would update") {
		t.Errorf("dry-run output = %q", out.String())
	}
	if data := readFile(t, root, "sub/go.mod"); !strings.Contains(data, "example.com/m v0.1.0") {
		t.Errorf("dry-run must not modify go.mod:\n%s", data)
	}

	out.Reset()
	if err := Sync(root, plan, true, &out); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if data := readFile(t, root, "sub/go.mod"); !strings.Contains(data, "example.com/m v0.1.1") {
		t.Errorf("sub go.mod not synced:\n%s", data)
	}
	if data := readFile(t, root, "go.mod"); !strings.Contains(data, "example.com/m/sub v0.2.0") {
		t.Errorf("root go.mod not synced:\n%s", data)
	}

	out.Reset()
	if err := Sync(root, plan, true, &out); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if !strings.Contains(out.String(), "无需归位") {
		t.Errorf("second sync should be a no-op, output = %q", out.String())
	}
}

func TestDiscover_ExamplesAreNotReleasable(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "examples/demo/go.mod", "module example.com/m/examples/demo\n\ngo 1.22\n")
	writeFile(t, root, "examples/demo/main.go", "package main\n")
	commit(t, root, "feat: demo server", "")

	repo, err := Discover(root)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	var demo *Module
	for _, m := range repo.Modules {
		if m.Dir == "examples/demo" {
			demo = m
		}
	}
	if demo == nil || demo.Releasable {
		t.Fatalf("examples/demo = %+v, want discovered but non-releasable", demo)
	}

	// 演示 module 的改动不能算到根模块头上。
	plan, err := repo.BuildPlan(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if len(plan.Changes) != 0 {
		t.Errorf("plan = %+v, want no release", plan.Changes)
	}
}

func TestPlanFileRoundTrip(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "root.go", "package m\n\n// fix\n")
	commit(t, root, "fix: root bug", "")
	plan := planFor(t, root)

	path := filepath.Join(t.TempDir(), "plan.json")
	if err := WritePlan(path, plan); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	loaded, err := ReadPlan(path)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if loaded.Head != plan.Head || len(loaded.Changes) != len(plan.Changes) {
		t.Errorf("round trip mismatch: %+v vs %+v", loaded, plan)
	}
}

func TestChangelog_InsertsAndGuardsDuplicate(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "sub/sub.go", "package sub\n\n// feature\n")
	commit(t, root, "feat: sub feature", "")

	plan := planFor(t, root)
	var out bytes.Buffer
	if err := WriteChangelog(root, plan, true, &out); err != nil {
		t.Fatalf("write changelog: %v", err)
	}
	data := readFile(t, root, "CHANGELOG.md")
	for _, want := range []string{"# 变更日志", "## 2026-01-02", "### sub v0.2.0", "feat: sub feature"} {
		if !strings.Contains(data, want) {
			t.Errorf("CHANGELOG missing %q:\n%s", want, data)
		}
	}

	if err := WriteChangelog(root, plan, true, &out); err == nil || !strings.Contains(err.Error(), "已包含") {
		t.Fatalf("duplicate changelog write must fail, got %v", err)
	}
}

func TestChangelog_PrependsBeforeHistory(t *testing.T) {
	existing := "# 变更日志\n\n说明。\n\n## 2025-01-01\n\n### old v0.0.1\n\n- `abc` chore: old\n"
	got := insertSections(existing, "## 2026-01-02\n\n### sub v0.2.0\n\n- `def` feat: new\n")
	if strings.Index(got, "### sub v0.2.0") > strings.Index(got, "### old v0.0.1") {
		t.Errorf("new versions must be before old history:\n%s", got)
	}
	if !strings.Contains(got, "# 变更日志") || !strings.Contains(got, "### old v0.0.1") {
		t.Errorf("existing content lost:\n%s", got)
	}
}

func TestTag_ApplyAndGuards(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "root.go", "package m\n\n// fix\n")
	commit(t, root, "fix: root bug", "")
	writeFile(t, root, "sub/sub.go", "package sub\n\n// feature\n")
	commit(t, root, "feat: sub feature", "")

	plan := planFor(t, root)

	var out bytes.Buffer
	if err := Tag(root, plan, false, false, &out); err != nil {
		t.Fatalf("dry-run tag: %v", err)
	}
	if !strings.Contains(out.String(), "would tag: sub/v0.2.0") {
		t.Errorf("dry-run output = %q", out.String())
	}
	if got := strings.TrimSpace(runGit(t, root, "tag", "--list", "sub/v0.2.0")); got != "" {
		t.Fatalf("dry-run created tag %q", got)
	}

	if err := Tag(root, plan, false, true, &out); err == nil {
		t.Fatal("--push without --apply must fail")
	}

	if err := Tag(root, plan, true, false, &out); err != nil {
		t.Fatalf("apply tag: %v", err)
	}
	for _, tag := range []string{"v0.1.1", "sub/v0.2.0"} {
		if got := strings.TrimSpace(runGit(t, root, "tag", "--list", tag)); got != tag {
			t.Errorf("tag %s missing after apply", tag)
		}
	}

	if err := Tag(root, plan, true, false, &out); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("second apply must fail on existing tags, got %v", err)
	}
}

func TestTag_RejectsUnrelatedHeadMovement(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "root.go", "package m\n\n// fix\n")
	commit(t, root, "fix: root bug", "")
	plan := planFor(t, root)

	writeFile(t, root, "notes.txt", "unrelated\n")
	commit(t, root, "docs: notes", "")

	var out bytes.Buffer
	err := Tag(root, plan, true, false, &out)
	if err == nil || !strings.Contains(err.Error(), "非发布改动") {
		t.Fatalf("expected unrelated-head guard, got %v", err)
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
