package codegen

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// exampleModule 返回 _example/bald 消费者 module 的绝对路径——生成物编译/运行以它
// 作模块上下文（其 go.mod 含 cobra/gin/grpc，replace 指向核心当前工作树，闭环验证）。
func exampleModule(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "_example", "bald")
}

// rootModuleGoPath 是聚合根的 module 声明路径，仓库根以它唯一标识。
const rootModuleGoPath = "github.com/kalandramo/bald"

// repoRoot 返回 bald 仓库根（从 cwd 上溯到声明根 module 的 go.mod 所在目录）。
// 刻意不写死「上溯 N 级」：本包已从 <root>/internal/codegen 搬到
// <root>/cli/internal/codegen，层数假设每搬一次就会静默失效。
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root, ok := findRepoRoot(wd)
	if !ok {
		t.Fatalf("repo root (go.mod declaring %s) not found from %s", rootModuleGoPath, wd)
	}
	return root
}

// findRepoRoot 从 dir 起逐级上溯，返回声明根 module 的 go.mod 所在目录。
// 只认 module 声明行逐字相等——cli/go.mod 声明的是 <root>/bald/cli，不能按前缀匹配。
func findRepoRoot(dir string) (string, bool) {
	for {
		if raw, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				if f := strings.Fields(line); len(f) >= 2 && f[0] == "module" && f[1] == rootModuleGoPath {
					return dir, true
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// pkgSourceDir 返回本包源码目录。以源码路径（runtime.Caller）而非 cwd 定位：
// 测试里 t.Chdir 会把 cwd 切到临时产出根，而 testdata/ 始终跟着源码走。
func pkgSourceDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(thisFile)
}

// TestGenApp_RequiresSpec CLI 参数层：gen app 只有 AppSpec 一条路径——缺 --spec
// 直接拒绝并给出指引；位置参数不再接受（应用名以 AppSpec.meta.name 为准）。
func TestGenApp_RequiresSpec(t *testing.T) {
	cmd := genAppCmd(NewDefaultIOStreams())
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--spec") {
		t.Fatalf("missing --spec must fail with --spec guidance, got: %v", err)
	}

	cmd2 := genAppCmd(NewDefaultIOStreams())
	cmd2.SetErr(io.Discard)
	cmd2.SetArgs([]string{"demo", "--spec", "appspec.json"})
	if err := cmd2.Execute(); err == nil {
		t.Fatal("positional app name must be rejected")
	}
}

// TestGenAppSpec_GeneratesTree --spec 驱动整套目录树：<name> 不必给，应用名与
// 落盘路径均以 AppSpec 为准（meta.name/module 必填，Gate1 校验）。
func TestGenAppSpec_GeneratesTree(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // 默认产出根（layout.root="."）落在临时目录
	specPath := filepath.Join(dir, "appspec.json")
	spec := `{
  "meta": {"name": "cli-demo", "module": "example.com/cli-demo", "desc": "d"},
  "bootstrap": {"audit": {"backends": ["log"]}}
}`
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	cmd := genAppCmd(NewDefaultIOStreams())
	cmd.SetArgs([]string{"--spec", specPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gen app --spec should succeed: %v", err)
	}
	for _, want := range []string{
		filepath.Join("cmd", "cli-demo", "main.go"),
		filepath.Join("cmd", "cli-demo", "app", "command.go"),
		filepath.Join("internal", "bootstrap", "assemble.gen.go"),
		filepath.Join("internal", "bootstrap", "domains.gen.go"),
		filepath.Join("configs", "cli-demo.yaml"),
		filepath.Join("api", "buf.yaml"),
		"taskfile.yml",
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("tree file %q not created: %v", want, err)
		}
	}
}
