package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testStreams 返回捕获 Out 的 IOStreams——命令信息性输出可被断言（Options 三阶段
// 的可测试性落点：输出走 o.Out 而非 os.Stdout）。
func testStreams() (IOStreams, *bytes.Buffer) {
	var out bytes.Buffer
	return IOStreams{In: os.Stdin, Out: &out, ErrOut: os.Stderr}, &out
}

// TestWriteFileGuarded_RefusesOverwrite 防覆盖底线：目标已存在即拒绝，
// 原文件内容分毫不动，错误信息自带 --force 指引。
func TestWriteFileGuarded_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.proto")
	if err := os.WriteFile(path, []byte("hand-edited"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	streams, out := testStreams()

	err := writeFileGuarded(path, []byte("regenerated"), false, streams)
	if err == nil {
		t.Fatal("must refuse to overwrite an existing file")
	}
	for _, want := range []string{"already exists", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hand-edited" {
		t.Errorf("existing file was modified: %q", got)
	}
	if strings.Contains(out.String(), "generated:") {
		t.Error("no success message expected on refusal")
	}
}

// TestWriteFileGuarded_ForceOverwrites --force 显式放行覆盖，成功提示走 Out。
func TestWriteFileGuarded_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.proto")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	streams, out := testStreams()

	if err := writeFileGuarded(path, []byte("new"), true, streams); err != nil {
		t.Fatalf("force write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("force overwrite did not take effect: %q", got)
	}
	if !strings.Contains(out.String(), "generated:") {
		t.Errorf("success message must go to Out, got: %q", out.String())
	}
}

// TestWriteFileGuarded_WritesNew 新路径正常写入（含目录自动创建）。
func TestWriteFileGuarded_WritesNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "cmd", "demo", "main.go")
	streams, out := testStreams()

	if err := writeFileGuarded(path, []byte("package main"), false, streams); err != nil {
		t.Fatalf("write new: %v", err)
	}
	if !strings.Contains(out.String(), "generated:") {
		t.Errorf("success message must go to Out, got: %q", out.String())
	}
}

// TestGenProto_CommandGuard CLI 层端到端：gen proto 二次执行拒绝（不静默覆盖），
// --force 放行；信息性输出走 IOStreams.Out 而非内建 println（stderr）。
func TestGenProto_CommandGuard(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // 默认相对输出（api/proto/bald/<name>/v1）落在临时目录
	streams, out := testStreams()

	// 第一次：正常生成。
	cmd := genProtoCmd(streams)
	cmd.SetArgs([]string{"demo"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("first gen: %v", err)
	}
	want := filepath.Join("api", "proto", "bald", "demo", "v1", "demo.proto")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("generated file %q not found: %v", want, err)
	}
	if !strings.Contains(out.String(), "generated:") || !strings.Contains(out.String(), "next:") {
		t.Errorf("info output must go to Out (generated/next), got: %q", out.String())
	}

	// 第二次：目标已存在，必须拒绝且不改动原文件。
	before, _ := os.ReadFile(want)
	cmd2 := genProtoCmd(streams)
	cmd2.SetArgs([]string{"demo"})
	if err := cmd2.Execute(); err == nil {
		t.Fatal("second gen must fail (file exists, no --force)")
	} else if !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal error must mention --force, got: %v", err)
	}
	after, _ := os.ReadFile(want)
	if string(before) != string(after) {
		t.Error("refused run must not modify the existing file")
	}

	// 第三次：--force 显式放行。
	cmd3 := genProtoCmd(streams)
	cmd3.SetArgs([]string{"demo", "--force"})
	if err := cmd3.Execute(); err != nil {
		t.Fatalf("gen --force: %v", err)
	}
}

// TestGenApp_CommandGuardForce 模板模式同契约：gen app 二次执行拒绝、--force 放行。
func TestGenApp_CommandGuardForce(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	streams, _ := testStreams()

	run := func(force bool) error {
		cmd := genAppCmd(streams)
		args := []string{"demoapp"}
		if force {
			args = append(args, "--force")
		}
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	if err := run(false); err != nil {
		t.Fatalf("first gen app: %v", err)
	}
	if err := run(false); err == nil {
		t.Fatal("second gen app must fail (file exists, no --force)")
	}
	if err := run(true); err != nil {
		t.Fatalf("gen app --force: %v", err)
	}
}
