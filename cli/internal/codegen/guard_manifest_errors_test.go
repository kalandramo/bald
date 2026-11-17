package codegen

// guard_manifest_errors_test.go 补 writeGuarded 与清单读写里的 I/O 失败分支：
// 目标路径的父级被普通文件占用（ENOTDIR）时，stat/read 必须报错而不是静默
// 当成「文件不存在」。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteGuarded_StatErrors Seeded / owned 两态在「父级是普通文件」时必须
// 失败且不得写穿：Unix 在 stat 即报 ENOTDIR；Windows 的 os.Stat 穿过普通文件
// 返回 ErrNotExist，失败点后移到 mkdir。不变量是任一阶段失败且目标未落盘。
func TestWriteGuarded_StatErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	target := filepath.Join(root, "blocker", "file.go")
	for _, own := range []ownership{Seeded, owned} {
		streams, _ := testStreams()
		err := writeGuarded(target, []byte("package x\n"), own, false, streams)
		if err == nil || (!strings.Contains(err.Error(), "stat") && !strings.Contains(err.Error(), "mkdir")) {
			t.Fatalf("ownership %d: must fail before writing through a regular file, got: %v", own, err)
		}
		// 不变量只是「目标没有落盘」：父级被普通文件占用时，Unix 的 Stat 报
		// ENOTDIR（IsNotExist 为 false——0b36b04d 曾在此用 IsNotExist 断言，
		// Windows 的 ErrNotExist 语义过、Linux 必红，即 CI #644 起 cli 判红
		// 的出处），Windows 报 ErrNotExist——stat 任一失败均满足不变量。
		if _, statErr := os.Stat(target); statErr == nil {
			t.Fatalf("ownership %d: 目标不得被写入（父级是普通文件）", own)
		}
	}
}

// TestCheckTree_ReadErrors 生成物路径或清单路径被目录占用时，读取失败必须
// 上报（不能当成 missing 继续比对）。
func TestCheckTree_ReadErrors(t *testing.T) {
	t.Run("generated file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "x.gen.go"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		streams, _ := testStreams()
		err := checkTree(root, "m.json", []genFile{{Path: "x.gen.go", Own: owned}}, []byte("{}"), streams)
		if err == nil || !strings.Contains(err.Error(), "read") {
			t.Fatalf("directory in place of a generated file must fail the read, got: %v", err)
		}
	})
	t.Run("manifest", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "m.json"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		streams, _ := testStreams()
		err := checkTree(root, "m.json", nil, []byte("{}"), streams)
		if err == nil || !strings.Contains(err.Error(), "read") {
			t.Fatalf("directory in place of the manifest must fail the read, got: %v", err)
		}
	})
}

// TestPruneStaleOwned_ManifestReadError 清理阶段读清单失败（非 ENOENT）时报错。
func TestPruneStaleOwned_ManifestReadError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "m.json"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	streams, _ := testStreams()
	if err := pruneStaleOwned(root, "m.json", nil, streams); err == nil || !strings.Contains(err.Error(), "read") {
		t.Fatalf("directory in place of the manifest must fail the prune, got: %v", err)
	}
}
