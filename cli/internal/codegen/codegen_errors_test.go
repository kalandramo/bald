package codegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件补齐 iostreams.go writeGuarded 与 manifest.go 各函数的错误/边界半边，
// 全部用临时目录夹具离线可跑，复用 guard_test.go 的 testStreams。结构性不可达
// 分支（fs 权限、已知类型 json.Marshal）见文件尾注释。

// --- writeGuarded（iostreams.go）---

// TestWriteGuarded_UnknownOwnership 非法归属值走 default 拒绝（iostreams.go:78）。
func TestWriteGuarded_UnknownOwnership(t *testing.T) {
	streams, _ := testStreams()
	err := writeGuarded(filepath.Join(t.TempDir(), "x"), []byte("x"), ownership(99), false, streams)
	if err == nil || !strings.Contains(err.Error(), "unknown ownership") {
		t.Fatalf("unknown ownership must error, got %v", err)
	}
}

// TestWriteGuarded_MkdirError 父目录被同名文件挡住 ⇒ MkdirAll 失败（iostreams.go:81）。
// force=true 跳过 stat 守卫直达落盘通道。
func TestWriteGuarded_MkdirError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	streams, _ := testStreams()
	err := writeGuarded(filepath.Join(blocker, "sub", "x.go"), []byte("x"), owned, true, streams)
	if err == nil {
		t.Fatal("writeGuarded under a file-blocked dir must error on MkdirAll")
	}
}

// TestWriteGuarded_WriteError 目标是目录 ⇒ MkdirAll(父) 成功但 WriteFile 失败
// （iostreams.go:84）。
func TestWriteGuarded_WriteError(t *testing.T) {
	dirPath := filepath.Join(t.TempDir(), "iamdir")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	streams, _ := testStreams()
	if err := writeGuarded(dirPath, []byte("x"), owned, true, streams); err == nil {
		t.Fatal("writeGuarded onto a directory must error on WriteFile")
	}
}

// --- buildManifest（manifest.go）---

// TestBuildManifest_SpecReadError 声明文件缺失 ⇒ read spec 报错（manifest.go:78）。
func TestBuildManifest_SpecReadError(t *testing.T) {
	_, _, err := buildManifest("test", filepath.Join(t.TempDir(), "missing.json"), nil)
	if err == nil || !strings.Contains(err.Error(), "read spec") {
		t.Fatalf("missing spec must error, got %v", err)
	}
}

// TestBuildManifest_UserFileSkipped 用户文件不进清单（manifest.go:83）。
func TestBuildManifest_UserFileSkipped(t *testing.T) {
	spec := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(spec, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, raw, err := buildManifest("test", spec, []genFile{{Path: "biz.go", Own: User, Raw: []byte("x")}})
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}
	if len(m.Files) != 0 {
		t.Errorf("user file must not enter manifest, got %+v", m.Files)
	}
	if len(raw) == 0 {
		t.Error("manifest raw must be non-empty")
	}
}

// TestBuildManifest_PrepareContentError owned .go 源非法 ⇒ prepareContent 的
// gofmt 失败上抛（manifest.go:95）。
func TestBuildManifest_PrepareContentError(t *testing.T) {
	spec := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(spec, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := buildManifest("test", spec,
		[]genFile{{Path: "bad.go", Own: owned, GoSrc: true, Raw: []byte("func ( {not go")}})
	if err == nil || !strings.Contains(err.Error(), "gofmt") {
		t.Fatalf("invalid go source must fail prepareContent, got %v", err)
	}
}

// --- checkTree（manifest.go）---

// TestCheckTree_UserFileSkipped 用户文件不参与比对（manifest.go:115），清单一致
// 即判干净。
func TestCheckTree_UserFileSkipped(t *testing.T) {
	root := t.TempDir()
	files := []genFile{{Path: "biz.go", Own: User, Raw: []byte("x")}}
	wantRaw, err := json.Marshal(&genManifest{Version: manifestVersion, Tool: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, domainManifestName), wantRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	streams, out := testStreams()
	if err := checkTree(root, domainManifestName, files, wantRaw, streams); err != nil {
		t.Fatalf("checkTree with only user file must be clean, got %v", err)
	}
	if !strings.Contains(out.String(), "clean:") {
		t.Errorf("want clean report, got %q", out.String())
	}
}

// TestCheckTree_PrepareContentError 磁盘文件存在（ReadFile 成功）但计划里的 .go
// 源非法 ⇒ prepareContent 失败上抛（manifest.go:135）。
func TestCheckTree_PrepareContentError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bad.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := []genFile{{Path: "bad.go", Own: owned, GoSrc: true, Raw: []byte("func ( {not go")}}
	streams, _ := testStreams()
	err := checkTree(root, domainManifestName, files, nil, streams)
	if err == nil || !strings.Contains(err.Error(), "gofmt") {
		t.Fatalf("invalid planned go source must fail prepareContent, got %v", err)
	}
}

// --- pruneStaleOwned（manifest.go）---

// TestPruneStaleOwned_CorruptManifest 清单损坏 ⇒ 无法判定归属，整体跳过返回 nil
// （manifest.go:200）。
func TestPruneStaleOwned_CorruptManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, domainManifestName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	streams, _ := testStreams()
	if err := pruneStaleOwned(root, domainManifestName, nil, streams); err != nil {
		t.Fatalf("corrupt manifest must skip pruning (nil), got %v", err)
	}
}

// TestPruneStaleOwned_MissingFileSkipped 清单记录的 owned 文件不在磁盘 ⇒ Remove
// 返回 IsNotExist ⇒ continue 不报错（manifest.go:215）。
func TestPruneStaleOwned_MissingFileSkipped(t *testing.T) {
	root := t.TempDir()
	raw, err := json.Marshal(&genManifest{Version: manifestVersion, Tool: "test",
		Files: []manifestEntry{{Path: "ghost.go", Ownership: "owned"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, domainManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	streams, _ := testStreams()
	if err := pruneStaleOwned(root, domainManifestName, nil, streams); err != nil {
		t.Fatalf("missing stale file must be skipped, got %v", err)
	}
}

// TestPruneStaleOwned_RemoveError 清单记为 owned 的路径是非空目录 ⇒ os.Remove
// 失败（非 IsNotExist）⇒ 报错上抛（manifest.go:216/218）。
func TestPruneStaleOwned_RemoveError(t *testing.T) {
	root := t.TempDir()
	staleDir := filepath.Join(root, "staleDir")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "inner.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(&genManifest{Version: manifestVersion, Tool: "test",
		Files: []manifestEntry{{Path: "staleDir", Ownership: "owned"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, domainManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	streams, _ := testStreams()
	err = pruneStaleOwned(root, domainManifestName, nil, streams)
	if err == nil || !strings.Contains(err.Error(), "remove stale generated file") {
		t.Fatalf("non-empty dir removal must error, got %v", err)
	}
}

// --- writeManifest（manifest.go）---

// TestWriteManifest_MkdirError root 是文件 ⇒ MkdirAll 失败（manifest.go:230）。
func TestWriteManifest_MkdirError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	streams, _ := testStreams()
	if err := writeManifest(blocker, appManifestName, []byte("{}"), streams); err == nil {
		t.Fatal("writeManifest under a file root must error on MkdirAll")
	}
}

// TestWriteManifest_WriteError 清单路径预置为目录 ⇒ WriteFile 失败（manifest.go:233）。
func TestWriteManifest_WriteError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, appManifestName), 0o755); err != nil {
		t.Fatal(err)
	}
	streams, _ := testStreams()
	err := writeManifest(root, appManifestName, []byte("{}"), streams)
	if err == nil || !strings.Contains(err.Error(), "write manifest") {
		t.Fatalf("writeManifest onto a directory must error, got %v", err)
	}
}

// 结构性不可达分支（如实留空，不为绿造假）：
//   - iostreams.go:67/74（Seeded/owned 的 os.Stat 返回非 IsNotExist 错误）：依赖
//     文件系统权限，root 身份夹具下不可稳定触发。
//   - manifest.go:103（buildManifest 的 json.MarshalIndent）：genManifest 是已知
//     类型，序列化恒成功。
//   - manifest.go:128/147/196（checkTree/pruneStaleOwned 的 ReadFile 非 IsNotExist
//     错误）：同 fs 权限夹具问题。
