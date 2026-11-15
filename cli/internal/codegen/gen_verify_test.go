package codegen

// gen_verify_test.go 是 gen verify 的验收测试：一次调用覆盖两条腿（app 树 +
// 域产物），零写入，失败时报出是哪条腿、哪份文件。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runGenVerify 在 dir 执行 gen verify（cwd 切到 dir，--out 缺省语义与生产一致）。
func runGenVerify(t *testing.T, dir, specPath string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(dir)
	streams, out := testStreams()
	cmd := genVerifyCmd(streams)
	cmd.SetArgs(append([]string{"--spec", specPath}, args...))
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return out.String(), err
}

// TestGenVerify_MatchesGeneratedTree 全绿路径：刚生成完的树通过两条腿的校验，
// 输出按段推进（app 树 → 域产物 → 汇总）。
func TestGenVerify_MatchesGeneratedTree(t *testing.T) {
	dir, specPath := runGenAppInTemp(t, guardFixture())
	out, err := runGenVerify(t, dir, specPath)
	if err != nil {
		t.Fatalf("verify on a freshly generated tree: %v\n%s", err, out)
	}
	for _, want := range []string{"verify: app tree", "verify: domain artifacts", "verified:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestGenVerify_FlagsAppTreeDrift app 侧产物被改 → 前缀点名 app tree（不是
// 笼统的「不一致」：两段校验失败时的修法完全不同）。
func TestGenVerify_FlagsAppTreeDrift(t *testing.T) {
	dir, specPath := runGenAppInTemp(t, guardFixture())
	mainPath := filepath.Join(dir, "cmd", "demoapp", "main.go")
	if err := os.WriteFile(mainPath, []byte("package main\n\n// hand-edited\n"), 0o644); err != nil {
		t.Fatalf("tamper main.go: %v", err)
	}
	_, err := runGenVerify(t, dir, specPath)
	if err == nil {
		t.Fatal("verify must fail on app-tree drift")
	}
	if !strings.Contains(err.Error(), "app tree:") {
		t.Errorf("error must be attributed to the app tree, got: %v", err)
	}
}

// TestGenVerify_FlagsDomainDrift 域产物被改 → 前缀点名 domain artifacts。
func TestGenVerify_FlagsDomainDrift(t *testing.T) {
	dir, specPath := runGenAppInTemp(t, guardFixture())
	convPath := filepath.Join(dir, "internal", "apiserver", "handler", "convert", "greet.gen.go")
	if _, err := os.Stat(convPath); err != nil {
		t.Fatalf("fixture convert projection missing: %v", err)
	}
	if err := os.WriteFile(convPath, []byte("package convert\n\n// hand-edited\n"), 0o644); err != nil {
		t.Fatalf("tamper convert: %v", err)
	}
	_, err := runGenVerify(t, dir, specPath)
	if err == nil {
		t.Fatal("verify must fail on domain-artifact drift")
	}
	if !strings.Contains(err.Error(), "domain artifacts:") {
		t.Errorf("error must be attributed to domain artifacts, got: %v", err)
	}
}

// TestGenVerify_WritesNothing 零写入：校验前后逐文件字节不变（含清单）。
func TestGenVerify_WritesNothing(t *testing.T) {
	dir, specPath := runGenAppInTemp(t, guardFixture())
	before := snapshotTree(t, dir)
	if _, err := runGenVerify(t, dir, specPath); err != nil {
		t.Fatalf("verify: %v", err)
	}
	after := snapshotTree(t, dir)
	if len(before) != len(after) {
		t.Fatalf("verify changed the file set: %d → %d files", len(before), len(after))
	}
	for path, want := range before {
		if got := after[path]; string(got) != string(want) {
			t.Errorf("%s changed during verify:\nbefore=%q\nafter=%q", path, want, got)
		}
	}
}

// TestGenVerify_RequiresSpec 没有 --spec 且 cwd 无 appspec.json 时点名修法。
func TestGenVerify_RequiresSpec(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	streams, out := testStreams()
	cmd := genVerifyCmd(streams)
	cmd.SetArgs(nil)
	cmd.SilenceErrors = true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("verify without --spec and without appspec.json must fail")
	}
	if !strings.Contains(err.Error(), "appspec.json") {
		t.Errorf("error must point at appspec.json, got: %v", err)
	}
	_ = out
}
