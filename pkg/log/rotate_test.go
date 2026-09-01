package log

import (
	"io"
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
	if c, ok := w.(io.Closer); ok {
		if err := c.Close(); err != nil {
			t.Fatalf("close failed: %v", err)
		}
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

	var w io.Writer = openWriter(o)
	if _, ok := w.(*lumberjack.Logger); !ok {
		t.Fatalf("expected *lumberjack.Logger for file path with rotation enabled, got %T", w)
	}
}

// TestOpenWriterDirectFileWhenRotationDisabled 验证关闭轮转时文件路径仍直写 os.File。
func TestOpenWriterDirectFileWhenRotationDisabled(t *testing.T) {
	dir := t.TempDir()
	o := NewOptions()
	o.OutputPaths = []string{filepath.Join(dir, "plain.log")}
	// Rotate 默认 Enabled=false。

	w := openWriter(o)
	f, ok := w.(*os.File)
	if !ok {
		t.Fatalf("expected *os.File for file path without rotation, got %T", w)
	}
	defer f.Close()
}
