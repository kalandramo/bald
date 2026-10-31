package config

// env_twopass_test.go 钉住两遍合并的两条红线：
//
//  1. env 前缀由第一遍（文件层）解析出的配置名派生——现有用户的 NAME_ 前缀
//     env 必须原样生效（覆盖文件值）；
//  2. 延迟挂载 env 层后，热更新（层 watch → 全量重合并）仍叠加 env 层，
//     watch 语义不回归。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// TestMountEnv_TwoPassDerivedPrefix 第一遍（DeferEnv）只装载文件层 → Unmarshal
// 解析 app.name；第二遍 MountEnv 按派生名挂载 env 层 → env 覆盖文件值。
func TestMountEnv_TwoPassDerivedPrefix(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "demo-app.yaml")
	yml := "app:\n  name: \"demo-app\"\nserver:\n  http: { addr: \":18080\" }\n"
	if err := os.WriteFile(path, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	// env 名不变红线：前缀来自 app.name（demo-app），文件值被 env 压过。
	t.Setenv("DEMO_APP_SERVER_HTTP_ADDR", ":19090")

	s, err := Load(Options{Name: "demo-app", ConfigFile: path, DeferEnv: true})
	if err != nil {
		t.Fatalf("Load (pass 1): %v", err)
	}
	defer s.Close()

	var probe bootstrapv1.BootstrapConfig
	if err := s.Unmarshal(&probe); err != nil {
		t.Fatalf("Unmarshal (pass 1): %v", err)
	}
	if got := probe.GetApp().GetName(); got != "demo-app" {
		t.Fatalf("pass 1 app.name = %q, want demo-app", got)
	}
	// 第一遍无 env 层：文件值原样。
	if got := probe.GetServer().GetHttp().GetAddr(); got != ":18080" {
		t.Fatalf("pass 1 addr = %q, want :18080 (env must be deferred)", got)
	}

	s.MountEnv("demo-app")

	var final bootstrapv1.BootstrapConfig
	if err := s.Unmarshal(&final); err != nil {
		t.Fatalf("Unmarshal (pass 2): %v", err)
	}
	if got := final.GetApp().GetName(); got != "demo-app" {
		t.Fatalf("pass 2 app.name = %q, want demo-app", got)
	}
	if got := final.GetServer().GetHttp().GetAddr(); got != ":19090" {
		t.Fatalf("pass 2 addr = %q, want :19090 (env override must win)", got)
	}
}

// TestMountEnv_WatchKeepsEnvLayer 延迟挂载 env 后，本地文件热更新触发的
// 全量重合并仍保留 env 覆盖（watch 语义不回归）。
func TestMountEnv_WatchKeepsEnvLayer(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "demo-app.yaml")
	if err := os.WriteFile(path, []byte("log:\n  level: \"info\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEMO_APP_SERVER_HTTP_ADDR", ":19090")

	changed := make(chan map[string]any, 4)
	s, err := Load(Options{
		Name:           "demo-app",
		ConfigFile:     path,
		DeferEnv:       true,
		WatchLocalFile: true,
		OnChange:       func(m map[string]any) { changed <- m },
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()
	s.MountEnv("demo-app")

	if err := os.WriteFile(path, []byte("log:\n  level: \"debug\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case m := <-changed:
			lvl, _ := At(m, "log.level")
			addr, _ := At(m, "server.http.addr")
			if String(lvl) == "debug" {
				if String(addr) != ":19090" {
					t.Fatalf("hot-reload snapshot lost env layer: server.http.addr = %q, want :19090", addr)
				}
				return
			}
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("hot-reload OnChange not triggered within timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
