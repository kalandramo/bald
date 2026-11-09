package release

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// driveRelease 用注入的 buffer 流执行 `bald release` 树的一个子命令，返回
// stdout/stderr 与错误。每次新建命令树避免 cobra flag 状态跨调用残留。
func driveRelease(args ...string) (out, errOut string, err error) {
	var ob, eb bytes.Buffer
	root := NewCommand(IOStreams{Out: &ob, ErrOut: &eb})
	root.SetArgs(args)
	err = root.Execute()
	return ob.String(), eb.String(), err
}

// TestVersionOf 取纯版本号：根模块 tag 即版本，子模块剥掉 "<dir>/" 前缀。
func TestVersionOf(t *testing.T) {
	if got := versionOf(Change{Dir: "."}, "v1.2.3"); got != "v1.2.3" {
		t.Errorf("root: got %q, want v1.2.3", got)
	}
	if got := versionOf(Change{Dir: "sub"}, "sub/v0.2.0"); got != "v0.2.0" {
		t.Errorf("sub: got %q, want v0.2.0", got)
	}
}

// TestPrintPlan 覆盖三条形状：空计划早退、有 changes 时 versionOf 的当前列（含
// 未发布过取 "-"）、依赖归位与消费者归位两段都非空。
func TestPrintPlan(t *testing.T) {
	// 空计划：只有早退那句。
	var empty bytes.Buffer
	if err := PrintPlan(&empty, &Plan{Head: "abcdef0123456789"}, "/repo/bald"); err != nil {
		t.Fatalf("empty plan: %v", err)
	}
	if s := empty.String(); !strings.Contains(s, "没有需要发版") || !strings.Contains(s, "abcdef01") {
		t.Errorf("empty plan output = %q", s)
	}

	plan := &Plan{
		Head: "deadbeefcafe",
		Changes: []Change{
			{Dir: ".", Module: "example.com/m", Current: "v0.1.0", Next: "v0.1.1", Bump: "patch",
				Commits:  []Commit{{Subject: "fix: a"}},
				Requires: []RequireUpdate{{Module: "example.com/m/sub", From: "v0.1.0", To: "v0.2.0"}}},
			// 未发布过的模块 Current 为空 → 表里当前列应显示 "-"。
			{Dir: "sub", Module: "example.com/m/sub", Current: "", Next: "v0.1.0", Bump: "initial",
				Commits: []Commit{{Subject: "feat: b"}}},
		},
		Consumers: []ConsumerUpdate{
			{Dir: "_example/demo", Module: "example.com/m/example/demo",
				Requires: []RequireUpdate{{Module: "example.com/m", From: "v0.1.0", To: "v0.1.1"}}},
		},
	}
	var out bytes.Buffer
	if err := PrintPlan(&out, plan, "/repo/bald"); err != nil {
		t.Fatalf("full plan: %v", err)
	}
	s := out.String()
	for _, want := range []string{"发布计划", "bald", "v0.1.0", "-", "v0.2.0",
		"依赖归位", "消费者与非发版模块归位", "_example/demo/go.mod"} {
		if !strings.Contains(s, want) {
			t.Errorf("PrintPlan output missing %q:\n%s", want, s)
		}
	}
}

// TestNewDefaultIOStreams 生产路径绑定的进程标准流非空。
func TestNewDefaultIOStreams(t *testing.T) {
	s := NewDefaultIOStreams()
	if s.In == nil || s.Out == nil || s.ErrOut == nil {
		t.Errorf("NewDefaultIOStreams returned nil stream(s): %#v", s)
	}
}

// TestNewCommand 根命令装配 plan/sync/changelog/tag 四个子命令。
func TestNewCommand(t *testing.T) {
	root := NewCommand(NewDefaultIOStreams())
	if root.Use != "release [command]" {
		t.Fatalf("root Use = %q", root.Use)
	}
	names := map[string]bool{}
	for _, c := range root.Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"plan", "sync", "changelog", "tag"} {
		if !names[want] {
			t.Errorf("missing subcommand %q", want)
		}
	}
}

// TestOpenRepo 在夹具仓库目录内以 git toplevel 打开：Discover 到根 + sub 两个模块。
// t.Chdir 仅在本测试内改变进程 cwd，结束自动还原。
func TestOpenRepo(t *testing.T) {
	root := newRepo(t)
	t.Chdir(root)
	repo, err := OpenRepo()
	if err != nil {
		t.Fatalf("OpenRepo: %v", err)
	}
	dirs := map[string]bool{}
	for _, m := range repo.Modules {
		dirs[m.Dir] = true
	}
	if !dirs["."] || !dirs["sub"] {
		t.Errorf("OpenRepo modules = %v, want . and sub", dirs)
	}
}

// TestLoadPlan OpenRepo 成功后读计划：命中返回 repo+plan，路径缺失经 ReadPlan 报错。
func TestLoadPlan(t *testing.T) {
	root := newRepo(t)
	t.Chdir(root)
	writeFile(t, root, "root.go", "package m\n\n// fix\n")
	commit(t, root, "fix: root bug", "")

	path := filepath.Join(t.TempDir(), "plan.json")
	if err := WritePlan(path, planFor(t, root)); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	repo, plan, err := loadPlan(path)
	if err != nil {
		t.Fatalf("loadPlan: %v", err)
	}
	if repo == nil || len(plan.Changes) == 0 {
		t.Fatalf("loadPlan got repo=%v plan changes=%d", repo != nil, len(plan.Changes))
	}
	if _, _, err := loadPlan(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Errorf("loadPlan(missing) should error")
	}
}

// TestCommands_RoundTrip 端到端驱动四个子命令的 RunE：plan（text+json 两分支）
// 写计划，sync/changelog/tag 用同一份计划做 dry-run（默认不 --write/--apply，
// 只打印），全程在夹具临时仓库内、不改真实仓库。
func TestCommands_RoundTrip(t *testing.T) {
	root := newRepo(t)
	t.Chdir(root)
	writeFile(t, root, "root.go", "package m\n\n// fix\n")
	commit(t, root, "fix: root bug", "")
	writeFile(t, root, "sub/sub.go", "package sub\n\n// feature\n")
	commit(t, root, "feat: sub feature", "")

	planPath := filepath.Join(t.TempDir(), "plan.json")

	// plan 默认 text（走 PrintPlan 分支）。
	out, errOut, err := driveRelease("plan", "--out", planPath)
	if err != nil {
		t.Fatalf("plan text: %v", err)
	}
	if !strings.Contains(out, "发布计划") || !strings.Contains(errOut, "plan:") {
		t.Errorf("plan text output = %q errout = %q", out, errOut)
	}

	// plan --json（走 JSON 打印分支）。
	out, _, err = driveRelease("plan", "--out", planPath, "--json")
	if err != nil {
		t.Fatalf("plan json: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("plan json output not JSON: %q", out)
	}

	// sync / changelog / tag 均 dry-run，复用同一份（HEAD 未动）计划。
	if _, _, err := driveRelease("sync", "--plan", planPath); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, _, err := driveRelease("changelog", "--plan", planPath); err != nil {
		t.Fatalf("changelog: %v", err)
	}
	out, _, err = driveRelease("tag", "--plan", planPath)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	if !strings.Contains(out, "would tag:") {
		t.Errorf("tag dry-run output = %q, want would tag", out)
	}
}
