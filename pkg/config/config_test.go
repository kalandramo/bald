package config

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	kratosconfig "github.com/go-kratos/kratos/v3/config"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// memSource 内存实现 RemoteSource，用于测试。
type memSource struct {
	mu      sync.Mutex
	data    []byte
	format  string
	watchCh chan struct{}
}

func newMemSource(data, format string) *memSource {
	return &memSource{data: []byte(data), format: format, watchCh: make(chan struct{}, 1)}
}

func (s *memSource) Read(_ context.Context) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data, s.format, nil
}

func (s *memSource) Watch(_ context.Context, onChange func([]byte, string)) error {
	go func() {
		for range s.watchCh {
			s.mu.Lock()
			d, f := s.data, s.format
			s.mu.Unlock()
			onChange(d, f)
		}
	}()
	return nil
}

func (s *memSource) update(data string) {
	s.mu.Lock()
	s.data = []byte(data)
	s.mu.Unlock()
	select {
	case s.watchCh <- struct{}{}:
	default:
	}
}

// kratosMemSource 实现 kratosconfig.Source，用于测试 FromKratosSource 桥接。
type kratosMemSource struct {
	data   []byte
	format string
}

func (s *kratosMemSource) Load() ([]*kratosconfig.KeyValue, error) {
	return []*kratosconfig.KeyValue{{Key: "remote", Value: s.data, Format: s.format}}, nil
}

func (s *kratosMemSource) Watch() (kratosconfig.Watcher, error) {
	return &kratosMemWatcher{ch: make(chan []*kratosconfig.KeyValue, 1)}, nil
}

type kratosMemWatcher struct{ ch chan []*kratosconfig.KeyValue }

func (w *kratosMemWatcher) Next() ([]*kratosconfig.KeyValue, error) {
	kv, ok := <-w.ch
	if !ok {
		return nil, context.Canceled
	}
	return kv, nil
}
func (w *kratosMemWatcher) Stop() error { return nil }

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

func TestLoad_RemoteBaselineThenLocalOverride(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"
name: remote`, "yaml")

	// 本地只覆盖 http.addr，不定义 name（name 为远程独有 key，应被保留）。
	localPath := writeTemp(t, "config.yaml", `http:
  addr: ":9090"`)

	v, err := Load(Options{
		Name:       "bald-demo",
		ConfigFile: localPath,
		Remote:     remote,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 本地覆盖远程：http.addr 取本地值。
	if got := v.GetString("http.addr"); got != ":9090" {
		t.Fatalf("http.addr = %q, want :9090 (local overrides remote)", got)
	}
	// 远程独有 key 应保留（基准）。
	if got := v.GetString("name"); got != "remote" {
		t.Fatalf("name = %q, want remote (remote baseline preserved)", got)
	}
}

func TestLoad_NoLocalFileAllowed(t *testing.T) {
	// 远程有值、无本地文件时不应报错（onexstack 行为）。
	remote := newMemSource(`http:
  addr: ":7070"`, "yaml")
	v, err := Load(Options{Name: "bald-demo", Remote: remote})
	if err != nil {
		t.Fatalf("Load should not fail without local file: %v", err)
	}
	if got := v.GetString("http.addr"); got != ":7070" {
		t.Fatalf("http.addr = %q, want :7070", got)
	}
}

func TestLoad_EnvSelectsFile(t *testing.T) {
	// 写入 Name-Env.yaml，验证 Env 非空时按 {Name}-{Env}.yaml 自动查找。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bald-demo-prod.yaml"), []byte("env: prod"), 0o644); err != nil {
		t.Fatalf("write prod file: %v", err)
	}
	old, _ := os.Getwd()
	defer os.Chdir(old) //nolint:errcheck
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	v, err := Load(Options{Name: "bald-demo", Env: "prod"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := v.GetString("env"); got != "prod" {
		t.Fatalf("env = %q, want prod", got)
	}
}

func TestLoad_RemoteWatchTriggersOnChange(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"`, "yaml")
	var mu sync.Mutex
	var changed int
	var lastAddr string

	_, err := Load(Options{
		Name:   "bald-demo",
		Remote: remote,
		OnChange: func(vv *viper.Viper) {
			mu.Lock()
			defer mu.Unlock()
			changed++
			lastAddr = vv.GetString("http.addr")
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	remote.update(`http:
  addr: ":9090"`)
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		c := changed
		mu.Unlock()
		if c > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("OnChange not triggered within timeout")
		case <-time.After(20 * time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if lastAddr != ":9090" {
		t.Fatalf("after watch, http.addr = %q, want :9090", lastAddr)
	}
}

func TestFromKratosSource(t *testing.T) {
	src := FromKratosSource(&kratosMemSource{data: []byte("k: v1"), format: "yaml"})
	data, format, err := src.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if format != "yaml" || string(data) != "k: v1" {
		t.Fatalf("Read = (%q,%q), want (k: v1,yaml)", string(data), format)
	}
	// Watch 应至少能从桥接器拿到一次回调（用注入的 watcher 验证）。
	kw := &kratosMemWatcher{ch: make(chan []*kratosconfig.KeyValue, 1)}
	adapter := &kratosSourceAdapter{src: &watchableKratosSource{watcher: kw}}
	got := make(chan string, 1)
	if err := adapter.Watch(context.Background(), func(d []byte, f string) {
		got <- string(d)
	}); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	kw.ch <- []*kratosconfig.KeyValue{{Key: "x", Value: []byte("k: v2"), Format: "yaml"}}
	select {
	case s := <-got:
		if s != "k: v2" {
			t.Fatalf("watch callback = %q, want k: v2", s)
		}
	case <-time.After(time.Second):
		t.Fatalf("watch callback not fired")
	}
}

// watchableKratosSource 用于测试 FromKratosSource.Watch 回调链路。
type watchableKratosSource struct {
	watcher kratosconfig.Watcher
}

func (s *watchableKratosSource) Load() ([]*kratosconfig.KeyValue, error) {
	return []*kratosconfig.KeyValue{{Key: "x", Value: []byte("k: v1"), Format: "yaml"}}, nil
}
func (s *watchableKratosSource) Watch() (kratosconfig.Watcher, error) { return s.watcher, nil }

// --- 以下为补充的单元测试场景 ---

// TestLoad_FlagHighestPriority：flag 应压过本地文件与远程（优先级 高→低：flag>本地>env>远程）。
func TestLoad_FlagHighestPriority(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"`, "yaml")
	localPath := writeTemp(t, "config.yaml", `http:
  addr: ":9090"`)

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("http.addr", ":7070", "")
	// viper 仅识别已 Parse 且有显式值的 flag（默认值不覆盖 config），模拟真实运行。
	if err := fs.Parse([]string{"--http.addr=:7000"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	v, err := Load(Options{
		Name:       "bald-demo",
		ConfigFile: localPath,
		Remote:     remote,
		Flags:      fs,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// flag 必须赢：:7000 > 本地 :9090 > 远程 :8080。
	if got := v.GetString("http.addr"); got != ":7000" {
		t.Fatalf("http.addr = %q, want :7000 (flag highest)", got)
	}
}

// TestLoad_EnvOverridesRemote：NAME_ 前缀环境变量应覆盖远程基准。
func TestLoad_EnvOverridesRemote(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"`, "yaml")

	t.Setenv("BALD_DEMO_HTTP_ADDR", ":6060")
	v, err := Load(Options{Name: "bald-demo", Remote: remote})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := v.GetString("http.addr"); got != ":6060" {
		t.Fatalf("http.addr = %q, want :6060 (env overrides remote)", got)
	}
}

// TestLoad_LocalFileWatchTriggersOnChange：本地文件变更应触发 OnChange，
// 且 override 层刷新（本地新值覆盖远程基准）。
func TestLoad_LocalFileWatchTriggersOnChange(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"`, "yaml")
	dir := t.TempDir()
	localPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(localPath, []byte("http:\n  addr: \":9090\""), 0o644); err != nil {
		t.Fatalf("write local: %v", err)
	}

	var mu sync.Mutex
	var changed int
	var lastAddr string

	_, err := Load(Options{
		Name:           "bald-demo",
		ConfigFile:     localPath,
		Remote:         remote,
		WatchLocalFile: true,
		OnChange: func(vv *viper.Viper) {
			mu.Lock()
			defer mu.Unlock()
			changed++
			lastAddr = vv.GetString("http.addr")
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// 修改本地文件，触发 fsnotify。
	if err := os.WriteFile(localPath, []byte("http:\n  addr: \":9091\""), 0o644); err != nil {
		t.Fatalf("rewrite local: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		c := changed
		mu.Unlock()
		if c > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("local file OnChange not triggered within timeout")
		case <-time.After(20 * time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if lastAddr != ":9091" {
		t.Fatalf("after local watch, http.addr = %q, want :9091", lastAddr)
	}
}

// TestLoad_RemoteWatchKeepsLocalOverride：远程 watch 更新后只 Reset 底层，
// 本地 override 层不被污染（回归 Issue#1 核心防护）。
func TestLoad_RemoteWatchKeepsLocalOverride(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"`, "yaml")
	localPath := writeTemp(t, "config.yaml", `http:
  addr: ":9090"`)

	var mu sync.Mutex
	var changed int
	// 直接持有 Load 返回的主 v 实例，在 watch 后断言本地覆盖是否保留。
	v, err := Load(Options{
		Name:       "bald-demo",
		ConfigFile: localPath,
		Remote:     remote,
		OnChange: func(vv *viper.Viper) {
			mu.Lock()
			defer mu.Unlock()
			changed++
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// 远程变更地址，但本地仍应压住远程。
	remote.update(`http:
  addr: ":7777"`)

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		c := changed
		mu.Unlock()
		if c > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("remote OnChange not triggered within timeout")
		case <-time.After(20 * time.Millisecond):
		}
	}
	// 直接断言主 v 实例：远程更新后重新注入了远程基准，但本地覆盖层应保留。
	if got := v.GetString("http.addr"); got != ":9090" {
		t.Fatalf("after remote watch, http.addr = %q, want :9090 (local override preserved)", got)
	}
}

// TestFromKratosSource_MultiKV_Merged：多 KV 的 kratos source 应合并为单文档，
// 不静默丢配置（防护 Issue#6）。
func TestFromKratosSource_MultiKV_Merged(t *testing.T) {
	ks := &multiKVKratosSource{
		kvs: []*kratosconfig.KeyValue{
			{Key: "a.yaml", Value: []byte("http:\n  addr: \":8080\""), Format: "yaml"},
			{Key: "b.yaml", Value: []byte("grpc:\n  addr: \":9090\""), Format: "yaml"},
		},
	}
	src := FromKratosSource(ks)
	data, format, err := src.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if format != "yaml" {
		t.Fatalf("format = %q, want yaml", format)
	}
	// 解析合并后的文档，两个 key 都应存在。
	v := viper.New()
	v.SetConfigType(format)
	if err := v.ReadConfig(bytes.NewReader(data)); err != nil {
		t.Fatalf("parse merged: %v", err)
	}
	if got := v.GetString("http.addr"); got != ":8080" {
		t.Fatalf("http.addr = %q, want :8080", got)
	}
	if got := v.GetString("grpc.addr"); got != ":9090" {
		t.Fatalf("grpc.addr = %q, want :9090", got)
	}
}

// multiKVKratosSource 返回多个 KV，用于验证多 KV 合并。
type multiKVKratosSource struct{ kvs []*kratosconfig.KeyValue }

func (s *multiKVKratosSource) Load() ([]*kratosconfig.KeyValue, error) { return s.kvs, nil }
func (s *multiKVKratosSource) Watch() (kratosconfig.Watcher, error) {
	return &kratosMemWatcher{ch: make(chan []*kratosconfig.KeyValue, 1)}, nil
}
