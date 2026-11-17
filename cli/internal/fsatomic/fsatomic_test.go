package fsatomic

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFile_CreatesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "target.json")

	if err := WriteFile(p, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "v1" {
		t.Fatalf("read back = %q, want v1", got)
	}

	if err := WriteFile(p, []byte("v2-longer-content"), 0o644); err != nil {
		t.Fatalf("overwrite v2: %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "v2-longer-content" {
		t.Fatalf("read back = %q, want v2-longer-content", got)
	}

	// 覆盖写后权限保持（Windows 上 chmod 只影响只读位，差异断言仅 Unix 有意义，
	// 这里只断言「文件仍可写」这一跨平台共同面）。
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("target should remain writable: %v", err)
	}
	_ = f.Close()
}

func TestWriteFile_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows chmod 只影响只读位，0600/0644 不可区分")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "secret")
	if err := WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", fi.Mode().Perm())
	}
}

func TestWriteFile_EmptyPath(t *testing.T) {
	if err := WriteFile("", []byte("x"), 0o644); err == nil {
		t.Fatal("empty path 应报错")
	}
}

func TestWriteFile_MissingDirFails(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nope", "f.txt")
	if err := WriteFile(p, []byte("x"), 0o644); err == nil {
		t.Fatal("目录不存在应报错（不代建目录）")
	}
}

func TestWriteFile_NoLeftoverTmpOnRenameFailure(t *testing.T) {
	// rename 失败路径：目标是已存在的**目录**——CreateTemp 成功、Write 成功、
	// rename 撞目录失败，必须清理临时文件（不留 .tmp 孤儿）。
	dir := t.TempDir()
	target := filepath.Join(dir, "occupied")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := WriteFile(target, []byte("x"), 0o644); err == nil {
		t.Fatal("rename 到目录应报错")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bald-tmp-") {
			t.Fatalf("失败路径遗留临时文件: %s", e.Name())
		}
	}
}

// TestWriteFile_SymlinkWriteThrough 钉住穿透语义：目标是符号链接时写真实
// 目的地、链接保持为链接——漏掉 Lstat/EvalSymlinks 防护的话，rename 会把
// 链接静默替换成普通文件（TestRunPreset_WriteError 曾当场抓住这回归）。
func TestWriteFile_SymlinkWriteThrough(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	if err := os.WriteFile(real, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed real: %v", err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink 不可用（Windows 无特权环境）: %v", err)
	}

	if err := WriteFile(link, []byte("new"), 0o644); err != nil {
		t.Fatalf("write through link: %v", err)
	}
	if got, _ := os.ReadFile(real); string(got) != "new" {
		t.Fatalf("real target = %q, want new（穿透写）", got)
	}
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("链接被替换成了普通文件: %v", err)
	}
}

// TestWriteFile_DanglingSymlinkFails 悬空链接 fail-closed：解析失败即报错，
// 不静默改写用户链接布局（与 os.WriteFile 的差异是刻意选择，见包注释）。
func TestWriteFile_DanglingSymlinkFails(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "dangling.json")
	if err := os.Symlink(filepath.Join(dir, "nope", "f.json"), link); err != nil {
		t.Skipf("symlink 不可用: %v", err)
	}
	if err := WriteFile(link, []byte("x"), 0o644); err == nil {
		t.Fatal("悬空符号链接应报错（fail-closed）")
	}
}

// TestWriteFile_TrailingSeparator 尾随分隔符显式报错（空 base 临时文件名
// 不可构造），而不是到 rename 才意外失败。
func TestWriteFile_TrailingSeparator(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFile(dir+string(os.PathSeparator), []byte("x"), 0o644); err == nil {
		t.Fatal("尾随分隔符（目标是目录本身）应报错")
	}
}
