package codegen

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testStreams 返回捕获 Out 的 IOStreams——命令信息性输出可被断言（Options 三阶段
// 的可测试性落点：输出走 o.Out 而非 os.Stdout）。ErrOut 弃流：gen 命令构造器统一
// 接 cmd.SetErr(streams.ErrOut)，cobra 的 "Error: ..." 随 ErrOut 走——负例测试
// 故意触发的错误若绑 os.Stderr 会漏进 CI 日志，读日志的人会误判为真实失败
// （run 39361419248 日志里的 Gate1/already exists 噪音即此）；错误本身仍经
// Execute 返回值逐字断言，不损失验证力度。
func testStreams() (IOStreams, *bytes.Buffer) {
	var out bytes.Buffer
	return IOStreams{In: os.Stdin, Out: &out, ErrOut: io.Discard}, &out
}

// ---- writeGuarded 三态归属（D2 §5.1 回归生成语义）----

// TestWriteGuarded_OwnedRefusesOverwrite Owned 防覆盖底线：目标已存在且无
// --force 即拒绝，原文件分毫不动，错误信息自带 --force 指引。
func TestWriteGuarded_OwnedRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.proto")
	if err := os.WriteFile(path, []byte("hand-edited"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	streams, out := testStreams()

	err := writeGuarded(path, []byte("regenerated"), Owned, false, streams)
	if err == nil {
		t.Fatal("must refuse to overwrite an existing owned file without --force")
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

// TestWriteGuarded_OwnedForceOverwrites Owned + --force 显式放行覆盖。
func TestWriteGuarded_OwnedForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.proto")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	streams, out := testStreams()

	if err := writeGuarded(path, []byte("new"), Owned, true, streams); err != nil {
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

// TestWriteGuarded_SeededKept Seeded 生成一次：不存在则写；已存在即跳过并
// 输出 kept:（不算错误，--force 也不覆盖——生成一次之后归用户维护）。
func TestWriteGuarded_SeededKept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seed", "seed.go")
	streams, out := testStreams()

	if err := writeGuarded(path, []byte("package seed\n\n// v1\n"), Seeded, false, streams); err != nil {
		t.Fatalf("first seed write: %v", err)
	}
	if !strings.Contains(out.String(), "generated:") {
		t.Errorf("first write must report generated:, got: %q", out.String())
	}

	// 用户手改后的内容 --force 也不许覆盖。
	streams2, out2 := testStreams()
	if err := writeGuarded(path, []byte("package seed\n\n// v2 (regenerated)\n"), Seeded, true, streams2); err != nil {
		t.Fatalf("seeded skip must not be an error: %v", err)
	}
	if !strings.Contains(out2.String(), "kept:") {
		t.Errorf("seeded skip must report kept:, got: %q", out2.String())
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "// v1") {
		t.Errorf("seeded file must be kept verbatim, got: %q", got)
	}
}

// TestWriteGuarded_UserUntouched User 永不落盘：只输出 skip:，连目录都不创建。
func TestWriteGuarded_UserUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user", "biz.go")
	streams, out := testStreams()

	if err := writeGuarded(path, []byte("package biz"), User, true, streams); err != nil {
		t.Fatalf("user file must never be an error: %v", err)
	}
	if !strings.Contains(out.String(), "skip:") {
		t.Errorf("user file must report skip:, got: %q", out.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("user file must not be created: %v", err)
	}
}

// TestWriteGuarded_WritesNew 新路径正常写入（含目录自动创建）。
func TestWriteGuarded_WritesNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "cmd", "demo", "main.go")
	streams, out := testStreams()

	if err := writeGuarded(path, []byte("package main"), Owned, false, streams); err != nil {
		t.Fatalf("write new: %v", err)
	}
	if !strings.Contains(out.String(), "generated:") {
		t.Errorf("success message must go to Out, got: %q", out.String())
	}
}

// ---- 越权直写回归锁（D2 §5.3）：gofmt 失败绝不触碰目标 ----

// TestWriteFileFormatted_GofmtFailureNeverTouchesTarget gofmt 失败时目标文件
// 字节不变、诊断原文写 <path>.rej、错误里附 .rej 路径。旧 renderAppSpec 在
// 此场景 os.WriteFile 目标路径（无 --force 也截断重写用户文件）——本测试钉死。
func TestWriteFileFormatted_GofmtFailureNeverTouchesTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	sentinel := "package main // user-edited, precious"
	if err := os.WriteFile(path, []byte(sentinel), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	streams, _ := testStreams()

	err := writeFileFormatted(path, []byte("func ( {not go"), Owned, false, streams)
	if err == nil {
		t.Fatal("gofmt failure must be an error")
	}
	if !strings.Contains(err.Error(), ".rej") {
		t.Errorf("error must mention the .rej path, got: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != sentinel {
		t.Errorf("target was touched on gofmt failure: %q", got)
	}
	rej, err := os.ReadFile(path + ".rej")
	if err != nil {
		t.Fatalf("diagnostic .rej not written: %v", err)
	}
	if string(rej) != "func ( {not go" {
		t.Errorf(".rej must carry the raw output, got: %q", rej)
	}
}

// TestWriteFileFormatted_SuccessGoesThroughGuard gofmt 成功后走 writeGuarded
// （Owned 无 --force 拒绝覆盖已存在文件）。
func TestWriteFileFormatted_SuccessGoesThroughGuard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	streams, _ := testStreams()
	if err := writeFileFormatted(path, []byte("package main\n"), Owned, false, streams); err == nil {
		t.Fatal("owned overwrite without --force must be refused")
	}
}

// ---- 命令级契约（gen proto 沿用 Owned 语义）----

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
	want := filepath.Join("api", "proto", "demo", "v1", "demo.proto")
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
