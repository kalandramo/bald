package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseVersion_StripsDirPrefix 复现 AR-10：子目录 module 的 tag 是
// "pkg/v1.2.3"——latestTag 返回完整 tag，parseVersion 此前只剥开头的 "v"，
// "pkg/v1" 段 Atoi 失败按 0 处理，feat 提交算出 v0.3.0 的降级版本（go get
// 永远选不到）。修复后剥目录前缀再解析。
func TestParseVersion_StripsDirPrefix(t *testing.T) {
	for _, c := range []struct {
		in                string
		major, minor, pat int
	}{
		{"v0.56.0", 0, 56, 0},
		{"pkg/v1.2.3", 1, 2, 3},
		{"internal/release/v10.20.30", 10, 20, 30},
		{"v1.2.3", 1, 2, 3},
	} {
		maj, min, pat := parseVersion(c.in)
		if maj != c.major || min != c.minor || pat != c.pat {
			t.Errorf("parseVersion(%q) = (%d,%d,%d), want (%d,%d,%d)", c.in, maj, min, pat, c.major, c.minor, c.pat)
		}
	}
}

// TestBumpVersion_SubDirFeatDoesNotDowngrade 端到端锁：子目录 module 的
// feat 提交必须从 pkg/v1.2.3 前进到 v1.3.0（修复前 parseVersion 把 pkg/v1
// 当 0，算出 v0.3.0 的降级版本；dir 前缀由 BuildPlan 拼，bumpVersion 返回裸版本）。
func TestBumpVersion_SubDirFeatDoesNotDowngrade(t *testing.T) {
	if got := bumpVersion("pkg/v1.2.3", "minor"); got != "v1.3.0" {
		t.Fatalf("bumpVersion(pkg/v1.2.3, minor) = %q, want v1.3.0（修复前是降级的 v0.3.0）", got)
	}
	if got := bumpVersion("pkg/v1.2.3", "major"); got != "v2.0.0" {
		t.Fatalf("major = %q, want v2.0.0", got)
	}
}

// TestCheckModulePathForMajor 复现 AR-10 第二半：root（或子目录）升 major
// 到 v2+ 时 go.mod 的 module 路径必须带 /vN 后缀（Go module 规范）——
// 路径不变打出的 v2.0.0 会被代理判非法。
func TestCheckModulePathForMajor(t *testing.T) {
	root := t.TempDir()
	mk := func(dir, module string) {
		t.Helper()
		p := filepath.Join(root, dir, "go.mod")
		if dir == "." {
			p = filepath.Join(root, "go.mod")
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("module "+module+"\n\ngo 1.27\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// root 升 major、路径未迁移 → 拒绝。
	mk(".", "example.com/bald")
	plan := &Plan{Changes: []Change{{Dir: ".", Tag: "v2.0.0"}}}
	if err := checkModulePathForMajor(root, plan); err == nil {
		t.Fatal("root 升 v2 但 module 路径未带 /v2 应拒绝")
	}

	// 路径已迁移 → 放行。
	mk(".", "example.com/bald/v2")
	if err := checkModulePathForMajor(root, plan); err != nil {
		t.Fatalf("已带 /v2 后缀应放行: %v", err)
	}

	// 子目录同判。
	mk("pkg", "example.com/bald/pkg")
	plan2 := &Plan{Changes: []Change{{Dir: "pkg", Tag: "pkg/v2.0.0"}}}
	if err := checkModulePathForMajor(root, plan2); err == nil {
		t.Fatal("子目录升 v2 未带后缀应拒绝")
	}
	mk("pkg", "example.com/bald/pkg/v2")
	if err := checkModulePathForMajor(root, plan2); err != nil {
		t.Fatalf("子目录带 /v2 应放行: %v", err)
	}

	// major < 2 不校验（v0/v1 路径无后缀要求）。
	plan3 := &Plan{Changes: []Change{{Dir: ".", Tag: "v1.0.0"}}}
	if err := checkModulePathForMajor(root, plan3); err != nil {
		t.Fatalf("v1 不该触发校验: %v", err)
	}
}

// TestParseVersion_RootMajorGuard 顺带锁 compare/bump 在 dir tag 上的序：
// pkg/v2.0.0 必须排在 pkg/v1.9.9 之后（修复前 pkg/v1.9.9 被解析成 1.9.9、
// pkg/v2.0.0 也是 1.9.9+2 → 序错乱）。
func TestParseVersion_RootMajorGuard(t *testing.T) {
	if compareVersion("pkg/v2.0.0", "pkg/v1.9.9") <= 0 {
		t.Fatal("pkg/v2.0.0 应排在 pkg/v1.9.9 之后")
	}
	if !strings.Contains(bumpVersion("pkg/v1.9.9", "minor"), "v1.10.0") {
		t.Fatalf("minor bump = %q", bumpVersion("pkg/v1.9.9", "minor"))
	}
}
