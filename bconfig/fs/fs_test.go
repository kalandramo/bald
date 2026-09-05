package fs

import (
	"context"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"config/app.yaml": &fstest.MapFile{Data: []byte("log:\n  level: debug")},
		"other.json":      &fstest.MapFile{Data: []byte(`{"a":1}`)},
	}
}

// TestNew_ParameterValidation fsys 必填。
func TestNew_ParameterValidation(t *testing.T) {
	if _, err := New(nil, WithPath("config/app.yaml")); err == nil {
		t.Error("New(nil) should fail")
	}
}

// TestLoad_DefaultPath 默认 path 读取。
func TestLoad_DefaultPath(t *testing.T) {
	src, err := New(testFS(), WithPath("config/app.yaml"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data, err := src.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "log:\n  level: debug" {
		t.Errorf("Load() = %q", data)
	}
}

// TestLoad_ExplicitKey key 覆盖默认 path。
func TestLoad_ExplicitKey(t *testing.T) {
	src, _ := New(testFS(), WithPath("config/app.yaml"))
	data, err := src.Load(context.Background(), "other.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != `{"a":1}` {
		t.Errorf("Load() = %q", data)
	}
}

// TestLoad_MissingPath 未给 path 报错、不存在文件报错。
func TestLoad_MissingPath(t *testing.T) {
	src, _ := New(testFS())
	if _, err := src.Load(context.Background(), ""); err == nil {
		t.Error("Load without path should fail")
	}
	if _, err := src.Load(context.Background(), "no/such.yaml"); err == nil {
		t.Error("Load missing file should fail")
	}
}
