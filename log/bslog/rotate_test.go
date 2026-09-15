package bslog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// TestNewRotateWriterWritesToFile 验证轮转 writer 能正常写入文件并落盘。
func TestNewRotateWriterWritesToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bald.log")

	w := newRotateWriter(path, &RotateOptions{
		Enabled:    true,
		MaxSize:    1,
		MaxBackups: 1,
		MaxAge:     1,
		Compress:   false,
	})
	if w == nil {
		t.Fatal("newRotateWriter returned nil")
	}
	msg := "hello rotation\n"
	if _, err := w.Write([]byte(msg)); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	// lumberjack 缓冲需 Close 触发落盘。
	if err := w.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log file not created: %v", err)
	}
	if !strings.Contains(string(data), "hello rotation") {
		t.Fatalf("unexpected file content: %q", string(data))
	}
}

// TestOpenWriterUsesRotateForFilePath 验证开启轮转时文件路径返回 lumberjack writer。
func TestOpenWriterUsesRotateForFilePath(t *testing.T) {
	dir := t.TempDir()
	o := NewOptions()
	o.OutputPaths = []string{filepath.Join(dir, "rot.log")}
	o.Rotate.Enabled = true

	w, closers := openWriter(o)
	if _, ok := w.(*lumberjack.Logger); !ok {
		t.Fatalf("expected *lumberjack.Logger for file path with rotation enabled, got %T", w)
	}
	if len(closers) != 1 {
		t.Fatalf("rotation writer should be collected for cleanup, got %d closers", len(closers))
	}
	_ = closers[0].Close()
}

// TestOpenWriterDirectFileWhenRotationDisabled 验证关闭轮转时文件路径仍直写 os.File。
func TestOpenWriterDirectFileWhenRotationDisabled(t *testing.T) {
	dir := t.TempDir()
	o := NewOptions()
	o.OutputPaths = []string{filepath.Join(dir, "plain.log")}
	// Rotate 默认 Enabled=false。

	w, closers := openWriter(o)
	if _, ok := w.(*os.File); !ok {
		t.Fatalf("expected *os.File for file path without rotation, got %T", w)
	}
	if len(closers) != 1 {
		t.Fatalf("direct file handle should be collected for cleanup, got %d closers", len(closers))
	}
	_ = closers[0].Close()
}

// TestOpenWriterCreatesMissingParentDir 验证直写路径自动创建缺失的嵌套父目录
// （对齐 lumberjack 轮转路径的首写 MkdirAll 行为——否则目录缺失时静默回退
// stdout，配置错误被掩盖）。
func TestOpenWriterCreatesMissingParentDir(t *testing.T) {
	dir := t.TempDir()
	// 嵌套两级目录均不存在。
	nested := filepath.Join(dir, "var", "log", "app")
	path := filepath.Join(nested, "app.log")

	o := NewOptions()
	o.OutputPaths = []string{path}

	w, closers := openWriter(o)
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	for _, c := range closers {
		_ = c.Close()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log file should be created under missing parent dir: %v", err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("unexpected content: %q", data)
	}
}

// TestNewWithCleanupReleasesFileHandles cleanup 关闭自有文件句柄——
// Windows 上未关句柄会让 TempDir/RemoveAll 删除失败（生产影响：lumberjack
// 缓冲不落盘、热更新重建后端泄漏句柄）。
func TestNewWithCleanupReleasesFileHandles(t *testing.T) {
	dir := t.TempDir()
	o := NewOptions()
	o.OutputPaths = []string{filepath.Join(dir, "app.log")}
	o.Rotate.Enabled = true

	l, cleanup := NewWithCleanup(o)
	if l == nil || cleanup == nil {
		t.Fatal("NewWithCleanup() returned nil logger or nil cleanup")
	}
	l.Info(context.Background(), "flush-on-close", "k", "v")
	cleanup()

	data, err := os.ReadFile(filepath.Join(dir, "app.log"))
	if err != nil || !strings.Contains(string(data), "flush-on-close") {
		t.Fatalf("cleanup should flush buffered writes: (%s, %v)", data, err)
	}
	// 句柄已关：目录可删（Windows 上未关句柄导致 RemoveAll 失败）。
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll after cleanup should succeed: %v", err)
	}
}
