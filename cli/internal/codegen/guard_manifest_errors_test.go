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

// TestWriteGuarded_StatErrors Seeded / owned 两态在 stat 失败（非 ENOENT）
// 时上报：父级是普通文件 → ENOTDIR，不能按「未存在」继续写。
func TestWriteGuarded_StatErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	target := filepath.Join(root, "blocker", "file.go")
	for _, own := range []ownership{Seeded, owned} {
		streams, _ := testStreams()
		err := writeGuarded(target, []byte("package x\n"), own, false, streams)
		if err == nil || !strings.Contains(err.Error(), "stat") {
			t.Fatalf("ownership %d: stat error must surface, got: %v", own, err)
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
