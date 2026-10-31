package codegen

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exampleModule 返回 _example/bald 消费者 module 的绝对路径——生成物编译/运行以它
// 作模块上下文（其 go.mod 含 cobra/gin/grpc，replace 指向核心当前工作树，闭环验证）。
// go test 的进程 cwd 恒为包目录（本包位于 <root>/internal/codegen），上溯两级即仓库根。
func exampleModule(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "_example", "bald")
}

// repoRoot 返回 bald 仓库根（go test 的进程 cwd 恒为包目录，上溯两级）。
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
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
		filepath.Join("internal", "apiserver", "domains.gen.go"),
		filepath.Join("configs", "cli-demo.yaml"),
		filepath.Join("api", "buf.yaml"),
		"taskfile.yml",
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("tree file %q not created: %v", want, err)
		}
	}
}
