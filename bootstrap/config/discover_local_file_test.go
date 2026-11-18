package config

import (
	"os"
	"testing"
)

// TestDiscoverLocalFile_EnvFallsBackToBase 复现 AR-3：Env 非空时此前只试
// Name-Env 文件——预装载（按 Name 探针）找到的文件，正式装载会找不到，
// 运行期配置为空且不受 watch。修复后查找序 Name-Env > Name（注释承诺的
// 原语义），两遍装载同源。
func TestDiscoverLocalFile_EnvFallsBackToBase(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("mybiz.yaml", []byte("app:\n  name: mybiz\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := DiscoverLocalFile(Options{Name: "mybiz", Env: "prod"})
	if got != "mybiz.yaml" {
		t.Fatalf("Env=prod 且只有基础文件时应回退到 Name.yaml，got %q", got)
	}

	// 环境专属存在时优先（注释承诺的优先序不变）。
	if err := os.WriteFile("mybiz-prod.yaml", []byte("app:\n  name: mybiz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DiscoverLocalFile(Options{Name: "mybiz", Env: "prod"}); got != "mybiz-prod.yaml" {
		t.Fatalf("环境专属文件存在时应优先，got %q", got)
	}
}

// TestStore_LocalFileAccessor 复现 AR-3 的同源机制：Load 后 Store 必须暴露
// 发现的文件路径，预装载把它作为正式装载的显式 ConfigFile 传入，两遍读同一
// 个文件。空路径（纯远程/纯 flag）原样返回空。
func TestStore_LocalFileAccessor(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("mybiz.yaml", []byte("app:\n  name: mybiz\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(Options{Name: "mybiz"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = s.Close() }()
	if got := s.LocalFile(); got != "mybiz.yaml" {
		t.Fatalf("LocalFile = %q, want mybiz.yaml", got)
	}

	// 显式 ConfigFile 原样透传。
	s2, err := Load(Options{Name: "mybiz", ConfigFile: "mybiz.yaml"})
	if err != nil {
		t.Fatalf("Load explicit: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if got := s2.LocalFile(); got != "mybiz.yaml" {
		t.Fatalf("explicit LocalFile = %q, want mybiz.yaml", got)
	}
}
