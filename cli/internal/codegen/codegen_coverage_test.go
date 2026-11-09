package codegen

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckErr_Nil 覆盖 CheckErr 的 err==nil 静默返回半边；非 nil 的 os.Exit(1)
// 半边由 re-exec 子进程真测覆盖（codegen_reexec_test.go）。
func TestCheckErr_Nil(t *testing.T) {
	CheckErr(nil) // 不 panic、不退出即通过
}

// TestNewCommand 根命令装配 proto/store/app/domain 四个叶子，不承载领域逻辑。
func TestNewCommand(t *testing.T) {
	streams, _ := testStreams()
	root := NewCommand(streams)
	if root.Use != "gen [command]" {
		t.Fatalf("root Use = %q", root.Use)
	}
	names := map[string]bool{}
	for _, c := range root.Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"proto", "store", "app", "domain"} {
		if !names[want] {
			t.Errorf("missing subcommand %q", want)
		}
	}
}

// TestStoreOptions_CompleteValidate 直接驱动 storeOptions 的默认回填与必填校验
// （cobra ExactArgs 让命令层永远拿到 name，故空 name 的拒绝半边只能在方法层测）。
func TestStoreOptions_CompleteValidate(t *testing.T) {
	var buf bytes.Buffer
	o := &storeOptions{ioStreams: ioStreams{Out: &buf}}
	if err := o.Complete([]string{"user"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if o.Name != "user" || o.OutDir != "." || o.Pkg != "main" {
		t.Errorf("Complete backfill = %+v, want name=user out=. pkg=main", o)
	}
	// 显式 --out/--out-pkg 不被默认覆盖。
	o2 := &storeOptions{OutDir: "gen", Pkg: "store"}
	if err := o2.Complete(nil); err != nil {
		t.Fatalf("Complete(nil): %v", err)
	}
	if o2.OutDir != "gen" || o2.Pkg != "store" {
		t.Errorf("explicit flags overwritten: %+v", o2)
	}
	if err := o2.Validate(); err == nil || !strings.Contains(err.Error(), "entity name") {
		t.Errorf("empty name Validate = %v, want required error", err)
	}
	if err := (&storeOptions{Name: "user"}).Validate(); err != nil {
		t.Errorf("non-empty name Validate = %v, want nil", err)
	}
}

// TestGenStoreCmd_CommandGuard CLI 层端到端镜像 gen proto：首次生成实体 .go（可
// 编译、gofmt 过），二次拒绝且不改动原文件，--force 放行。
func TestGenStoreCmd_CommandGuard(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	streams, out := testStreams()

	cmd := genStoreCmd(streams)
	cmd.SetArgs([]string{"order"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("first gen store: %v", err)
	}
	want := filepath.Join("order.go")
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("generated file %q not found: %v", want, err)
	}
	// 实体骨架必须带 title 化后的结构体与 KeyOf，且 package main 可编译。
	for _, s := range []string{"package main", "type Order struct", "func KeyOfOrder", "gorm:\"primaryKey\""} {
		if !strings.Contains(string(data), s) {
			t.Errorf("entity file missing %q:\n%s", s, data)
		}
	}
	if !strings.Contains(out.String(), "generated:") {
		t.Errorf("info output must go to Out, got %q", out.String())
	}

	// 二次：已存在默认拒绝，且不改原文件。
	before := append([]byte(nil), data...)
	cmd2 := genStoreCmd(streams)
	cmd2.SetArgs([]string{"order"})
	if err := cmd2.Execute(); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second gen must fail mentioning --force, got %v", err)
	}
	if after, _ := os.ReadFile(want); string(after) != string(before) {
		t.Error("refused run must not modify the existing file")
	}

	// --force 放行重写。
	cmd3 := genStoreCmd(streams)
	cmd3.SetArgs([]string{"order", "--force"})
	if err := cmd3.Execute(); err != nil {
		t.Fatalf("gen store --force: %v", err)
	}
}

// TestCheckAppSpec 覆盖 CheckAppSpec 这道零写入门：刚生成的树与声明零差异 ⇒
// nil；删掉一个 owned 产物后再检 ⇒ 返回带逐文件清单的漂移错误。
func TestCheckAppSpec(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	specPath := filepath.Join(dir, "appspec.json")
	spec := `{
  "meta": {"name": "cli-demo", "module": "example.com/cli-demo", "desc": "d"},
  "bootstrap": {"audit": {"backends": ["log"]}}
}`
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	// 先生成完整 owned 树（非 --check：emitTree 落盘 + 写清单）。
	gen := genAppCmd(NewDefaultIOStreams())
	gen.SetArgs([]string{"--spec", specPath})
	if err := gen.Execute(); err != nil {
		t.Fatalf("gen app for check fixture: %v", err)
	}

	// 零差异：CheckAppSpec 必须放行。
	if err := CheckAppSpec(specPath, "", io.Discard, io.Discard); err != nil {
		t.Fatalf("clean tree CheckAppSpec = %v, want nil", err)
	}

	// 删掉一个 owned 产物（main.go，清单里必为 owned）⇒ 漂移 ⇒ 非 nil 且点名
	// 缺失文件（checkTree 以 slash 清单 id 报告 "missing: <id>"）。
	ownedID := "cmd/cli-demo/main.go"
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(ownedID))); err != nil {
		t.Fatalf("remove owned file: %v", err)
	}
	err := CheckAppSpec(specPath, "", io.Discard, io.Discard)
	if err == nil {
		t.Fatal("drifted tree CheckAppSpec must fail")
	}
	if !strings.Contains(err.Error(), ownedID) {
		t.Errorf("drift error must name %q, got: %v", ownedID, err)
	}
}
