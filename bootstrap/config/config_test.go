package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	kratosconfig "github.com/go-kratos/kratos/v3/config"
	"github.com/spf13/pflag"
)

// getStr 测试辅助：从快照 map 中按点路径读字符串。
func getStr(m map[string]any, key string) string {
	v, ok := getAtPath(m, key)
	if !ok {
		return ""
	}
	return toString(v)
}

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

	s, err := Load(Options{
		Name:       "bald-demo",
		ConfigFile: localPath,
		Remote:     remote,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 本地覆盖远程：http.addr 取本地值。
	if got := s.GetString("http.addr"); got != ":9090" {
		t.Fatalf("http.addr = %q, want :9090 (local overrides remote)", got)
	}
	// 远程独有 key 应保留（基准）。
	if got := s.GetString("name"); got != "remote" {
		t.Fatalf("name = %q, want remote (remote baseline preserved)", got)
	}
}

func TestLoad_NoLocalFileAllowed(t *testing.T) {
	// 远程有值、无本地文件时不应报错（onexstack 行为）。
	remote := newMemSource(`http:
  addr: ":7070"`, "yaml")
	s, err := Load(Options{Name: "bald-demo", Remote: remote})
	if err != nil {
		t.Fatalf("Load should not fail without local file: %v", err)
	}
	if got := s.GetString("http.addr"); got != ":7070" {
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
	s, err := Load(Options{Name: "bald-demo", Env: "prod"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.GetString("env"); got != "prod" {
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
		OnChange: func(m map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			changed++
			lastAddr = getStr(m, "http.addr")
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

// --- 优先级链测试 ---

// TestLoad_FlagHighestPriority：flag 应压过本地文件与远程（优先级 高→低：flag>env>本地>远程）。
func TestLoad_FlagHighestPriority(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"`, "yaml")
	localPath := writeTemp(t, "config.yaml", `http:
  addr: ":9090"`)

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("http.addr", ":7070", "")
	// 仅识别已 Parse 且有显式值的 flag（默认值不覆盖 config），模拟真实运行。
	if err := fs.Parse([]string{"--http.addr=:7000"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	s, err := Load(Options{
		Name:       "bald-demo",
		ConfigFile: localPath,
		Remote:     remote,
		Flags:      fs,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// flag 必须赢：:7000 > 本地 :9090 > 远程 :8080。
	if got := s.GetString("http.addr"); got != ":7000" {
		t.Fatalf("http.addr = %q, want :7000 (flag highest)", got)
	}
}

// TestLoad_FlagDefaultNotMerged：未显式传入的 flag（默认值）不参与合并，
// 不压过 env/文件/远程——零值 flag 反压是旧 BindPFlags 的著名坑。
func TestLoad_FlagDefaultNotMerged(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"`, "yaml")

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("http.addr", ":7070", "") // 默认值，未 Parse
	t.Setenv("BALD_DEMO_HTTP_ADDR", ":6060")

	s, err := Load(Options{Name: "bald-demo", Remote: remote, Flags: fs})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.GetString("http.addr"); got != ":6060" {
		t.Fatalf("http.addr = %q, want :6060 (unchanged flag must not shadow env)", got)
	}
}

// TestLoad_EnvOverridesRemote：NAME_ 前缀环境变量应覆盖远程基准。
func TestLoad_EnvOverridesRemote(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"`, "yaml")

	t.Setenv("BALD_DEMO_HTTP_ADDR", ":6060")
	s, err := Load(Options{Name: "bald-demo", Remote: remote})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.GetString("http.addr"); got != ":6060" {
		t.Fatalf("http.addr = %q, want :6060 (env overrides remote)", got)
	}
}

// TestLoad_EnvVsLocalFile：环境变量应压过本地文件（优先级 flag > env > 本地 > 远程）。
// 这是面向 K8s/容器部署的核心语义——运维通过 ConfigMap 注入的环境变量覆盖镜像内配置。
func TestLoad_EnvVsLocalFile(t *testing.T) {
	// 本地文件给出 :9090，env 给出 :6060，env 必须赢。
	localPath := writeTemp(t, "config.yaml", `http:
  addr: ":9090"`)

	t.Setenv("BALD_DEMO_HTTP_ADDR", ":6060")
	s, err := Load(Options{Name: "bald-demo", ConfigFile: localPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.GetString("http.addr"); got != ":6060" {
		t.Fatalf("http.addr = %q, want :6060 (env overrides local file)", got)
	}
}

// TestLoad_FullPriorityChain：单测覆盖完整优先级链 flag > env > 本地 > 远程，
// 同名 key 在四个来源分别给出不同值，断言最终取最高优先级的 flag。
func TestLoad_FullPriorityChain(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"`, "yaml") // 远程 :8080（最低）
	localPath := writeTemp(t, "config.yaml", `http:
  addr: ":9090"`) // 本地 :9090（压远程）

	t.Setenv("BALD_DEMO_HTTP_ADDR", ":6060") // env :6060（压本地）

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("http.addr", ":7070", "")
	if err := fs.Parse([]string{"--http.addr=:7000"}); err != nil { // flag :7000（最高）
		t.Fatalf("parse flags: %v", err)
	}

	s, err := Load(Options{
		Name:       "bald-demo",
		ConfigFile: localPath,
		Remote:     remote,
		Flags:      fs,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.GetString("http.addr"); got != ":7000" {
		t.Fatalf("http.addr = %q, want :7000 (flag highest in chain)", got)
	}
}

// --- 热更新测试 ---

// TestLoad_LocalFileWatchTriggersOnChange：本地文件变更应触发 OnChange，
// 且本地层刷新（本地新值覆盖远程基准）。
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
		OnChange: func(m map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			changed++
			lastAddr = getStr(m, "http.addr")
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

// TestLoad_LocalWatchPreservesRemoteBaseline：本地文件变更触发 watch 后，
// 远程基准不应被清掉（回归本地 watch bug：整体刷新底层会丢掉远程）。
func TestLoad_LocalWatchPreservesRemoteBaseline(t *testing.T) {
	remote := newMemSource(`name: remote
grpc:
  addr: ":9090"`, "yaml") // 远程独有 key：name / grpc.addr
	dir := t.TempDir()
	localPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(localPath, []byte("http:\n  addr: \":9090\""), 0o644); err != nil {
		t.Fatalf("write local: %v", err)
	}

	var mu sync.Mutex
	var changed int

	// 持有主 s 实例：回归点是本地 watch 后「同一实例」的合并树是否重建且保留远程基准。
	s, err := Load(Options{
		Name:           "bald-demo",
		ConfigFile:     localPath,
		Remote:         remote,
		WatchLocalFile: true,
		OnChange: func(m map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			changed++
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// 修改本地文件（只改 http.addr），触发 fsnotify。
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

	// 关键回归点：直接断言触发热更新的「主 s 实例」（而非重新 Load），
	// 验证本地 watch 重合并后仍保留远程基准。
	mu.Lock()
	defer mu.Unlock()
	if got := s.GetString("name"); got != "remote" {
		t.Fatalf("after local watch, name = %q, want remote (remote baseline preserved)", got)
	}
	if got := s.GetString("grpc.addr"); got != ":9090" {
		t.Fatalf("after local watch, grpc.addr = %q, want :9090 (remote baseline preserved)", got)
	}
	if got := s.GetString("http.addr"); got != ":9091" {
		t.Fatalf("after local watch, http.addr = %q, want :9091 (local new value)", got)
	}
}

// TestLoad_RemoteWatchKeepsLocalOverride：远程 watch 更新后，
// 本地/env 层不被污染（回归 Issue#1 核心防护）。
func TestLoad_RemoteWatchKeepsLocalOverride(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"`, "yaml")
	localPath := writeTemp(t, "config.yaml", `http:
  addr: ":9090"`)

	var mu sync.Mutex
	var changed int
	// 直接持有 Load 返回的主 s 实例，在 watch 后断言本地覆盖是否保留。
	s, err := Load(Options{
		Name:       "bald-demo",
		ConfigFile: localPath,
		Remote:     remote,
		OnChange: func(m map[string]any) {
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
	// 直接断言主 s 实例：远程更新重建远程层，但本地覆盖层应保留。
	if got := s.GetString("http.addr"); got != ":9090" {
		t.Fatalf("after remote watch, http.addr = %q, want :9090 (local override preserved)", got)
	}
}

// TestLoad_SettingsSnapshotIsolated：Settings() 返回深拷贝快照，
// 热更新后旧快照不受影响（发布语义：已取出的快照稳定）。
func TestLoad_SettingsSnapshotIsolated(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"`, "yaml")
	var mu sync.Mutex
	var changed int

	s, err := Load(Options{
		Name:   "bald-demo",
		Remote: remote,
		OnChange: func(m map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			changed++
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	before := s.Settings()

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

	// 旧快照必须仍是 :8080；新快照是 :9090。
	if got := getStr(before, "http.addr"); got != ":8080" {
		t.Fatalf("old snapshot http.addr = %q, want :8080 (snapshot must be isolated)", got)
	}
	if got := s.GetString("http.addr"); got != ":9090" {
		t.Fatalf("after update http.addr = %q, want :9090", got)
	}
}

// TestLoad_DeferWatchWhenNoCallback：无 OnChange 时不启动 watch（防空转 goroutine）。
func TestLoad_DeferWatchWhenNoCallback(t *testing.T) {
	remote := newMemSource(`http:
  addr: ":8080"`, "yaml")
	localPath := writeTemp(t, "config.yaml", `http:
  addr: ":9090"`)
	s, err := Load(Options{
		Name:           "bald-demo",
		ConfigFile:     localPath,
		Remote:         remote,
		WatchLocalFile: true, // 有 watch 意愿但无回调 → 不启动
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.watchCancel != nil {
		t.Fatalf("watch should not be armed without OnChange")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
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
	m, err := decodeDocument(data, format)
	if err != nil {
		t.Fatalf("decode merged: %v", err)
	}
	if got := getStr(m, "http.addr"); got != ":8080" {
		t.Fatalf("http.addr = %q, want :8080", got)
	}
	if got := getStr(m, "grpc.addr"); got != ":9090" {
		t.Fatalf("grpc.addr = %q, want :9090", got)
	}
}

// multiKVKratosSource 返回多个 KV，用于验证多 KV 合并。
type multiKVKratosSource struct{ kvs []*kratosconfig.KeyValue }

func (s *multiKVKratosSource) Load() ([]*kratosconfig.KeyValue, error) { return s.kvs, nil }
func (s *multiKVKratosSource) Watch() (kratosconfig.Watcher, error) {
	return &kratosMemWatcher{ch: make(chan []*kratosconfig.KeyValue, 1)}, nil
}

// --- merge.go 单元测试 ---

func TestDeepMerge_Semantics(t *testing.T) {
	base := map[string]any{
		"server":  map[string]any{"http": map[string]any{"addr": ":8080"}, "grpc": map[string]any{"addr": ":9090"}},
		"scalars": map[string]any{"n": 1},
	}
	over := map[string]any{
		"server":  map[string]any{"http": map[string]any{"addr": ":9999"}},
		"scalars": map[string]any{"n": 2},
		"new":     map[string]any{"k": "v"},
	}
	merged := deepMerge(base, over)

	// 覆盖生效（浅层 scalar 与深层嵌套）。
	if got := getStr(merged, "server.http.addr"); got != ":9999" {
		t.Fatalf("server.http.addr = %q, want :9999", got)
	}
	// 同级兄弟 key 保留（合并而非替换）。
	if got := getStr(merged, "server.grpc.addr"); got != ":9090" {
		t.Fatalf("server.grpc.addr = %q, want :9090 (sibling preserved)", got)
	}
	if got := getStr(merged, "new.k"); got != "v" {
		t.Fatalf("new.k = %q, want v", got)
	}
	// 标量覆盖。
	if v, _ := getAtPath(merged, "scalars.n"); v != 2 {
		t.Fatalf("scalars.n = %v, want 2", v)
	}
	// 输入 map 不被修改（copy-on-write）。
	if got := getStr(base, "server.http.addr"); got != ":8080" {
		t.Fatalf("base mutated: server.http.addr = %q, want :8080", got)
	}
}

func TestDeepMerge_NonMapOverridesMap(t *testing.T) {
	base := map[string]any{"a": map[string]any{"b": 1}}
	over := map[string]any{"a": "scalar"}
	merged := deepMerge(base, over)
	// 非 map 值整体替换嵌套 map（类型冲突以高层为准）。
	if v, _ := getAtPath(merged, "a"); v != "scalar" {
		t.Fatalf("a = %v, want scalar (non-map replaces map)", v)
	}
}

func TestFlattenFlags_ChangedOnly(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("http.addr", ":7070", "")
	fs.Bool("http.tls", false, "")
	fs.StringSlice("http.skip", []string{"/a", "/b"}, "")
	// 只显式传入 http.addr。
	if err := fs.Parse([]string{"--http.addr=:7000"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	m := flattenFlags(fs)
	if got := getStr(m, "http.addr"); got != ":7000" {
		t.Fatalf("http.addr = %q, want :7000", got)
	}
	if _, ok := getAtPath(m, "http.tls"); ok {
		t.Fatalf("http.tls should not be merged (unchanged flag)")
	}
	if _, ok := getAtPath(m, "http.skip"); ok {
		t.Fatalf("http.skip should not be merged (unchanged flag)")
	}
}

func TestEnvironMap_PrefixAndPath(t *testing.T) {
	t.Setenv("DEMO_HTTP_ADDR", ":6060")
	t.Setenv("OTHER_HTTP_ADDR", ":1111") // 不同前缀，忽略
	m := environMap("demo")
	if got := getStr(m, "http.addr"); got != ":6060" {
		t.Fatalf("http.addr = %q, want :6060", got)
	}
	if _, ok := getAtPath(m, "other"); ok {
		t.Fatalf("OTHER_ prefix leaked into demo env map")
	}
}

func TestDecodeDocument_JSON(t *testing.T) {
	m, err := decodeDocument([]byte(`{"http":{"addr":":8080"}}`), "json")
	if err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if got := getStr(m, "http.addr"); got != ":8080" {
		t.Fatalf("http.addr = %q, want :8080", got)
	}
}
