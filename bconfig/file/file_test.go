package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kalandramo/bald/bconfig"
)

func TestNew_EmptyPath(t *testing.T) {
	_, err := New()
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := []byte("name: test\nport: 8080\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := New(WithPath(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	data, err := s.Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Errorf("expected %q, got %q", content, data)
	}
}

func TestLoad_KeyOverride(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.yaml")
	path2 := filepath.Join(dir, "b.yaml")

	os.WriteFile(path1, []byte("a"), 0o644)
	os.WriteFile(path2, []byte("b"), 0o644)

	s, err := New(WithPath(path1))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	// Default key
	data, _ := s.Load(context.Background(), "")
	if string(data) != "a" {
		t.Errorf("expected 'a', got %q", data)
	}

	// Override key
	data, _ = s.Load(context.Background(), path2)
	if string(data) != "b" {
		t.Errorf("expected 'b', got %q", data)
	}
}

func TestWatchValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watch.yaml")

	os.WriteFile(path, []byte("v1"), 0o644)

	s, err := New(WithPath(path), WithWatch(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	// Modify the file after a short delay.
	go func() {
		time.Sleep(200 * time.Millisecond)
		os.WriteFile(path, []byte("v2"), 0o644)
	}()

	select {
	case data := <-ch:
		if string(data) != "v2" {
			t.Errorf("expected 'v2', got %q", data)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for watch event")
	}
}

func TestClose_NilWatcher(t *testing.T) {
	s := &source{options: &options{path: "/tmp/test"}}
	if err := s.Close(); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestInterfaceCompliance(t *testing.T) {
	var _ bconfig.Reader = (*source)(nil)
	var _ bconfig.ValueWatcher = (*source)(nil)
	var _ bconfig.Closer = (*source)(nil)
}
