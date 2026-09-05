package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sleepMs 轮询等待的步进休眠。
func sleepMs(n int) { time.Sleep(time.Duration(n) * time.Millisecond) }

// --- 契约源层（Layer）机制测试 ---

// stubLayerReader 最小整文档源：固定数据 + 可推送变更（ValueWatcher）。
type stubLayerReader struct {
	data []byte
	ch   chan []byte
}

func newStubLayerReader(doc string) *stubLayerReader {
	return &stubLayerReader{data: []byte(doc), ch: make(chan []byte, 1)}
}

func (s *stubLayerReader) Load(_ context.Context, _ string) ([]byte, error) {
	return s.data, nil
}

func (s *stubLayerReader) WatchValue(_ context.Context, _ string) (<-chan []byte, error) {
	return s.ch, nil
}

func (s *stubLayerReader) push(doc string) {
	select {
	case s.ch <- []byte(doc):
	default:
	}
}

// waitFor 轮询等待条件成立（最多 1s），避免热更新时序竞态。
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		sleepMs(5)
	}
	t.Fatal(msg)
}

// TestLoad_Layers_MergePriority：列表首元素优先级最高 + 字段级合并。
// first 层覆盖 second 层的重叠键；second 独有键保留——这是 FallbackReader
// 回退语义做不到的字段级合并，是层模型的直接证据。
func TestLoad_Layers_MergePriority(t *testing.T) {
	first := newStubLayerReader("log:\n  level: debug\nreplicas: 1")
	second := newStubLayerReader("log:\n  level: info\n  addr: \":9090\"")

	s, err := Load(Options{
		Name: "bald-demo",
		Layers: []Layer{
			{Name: "first", Reader: first},
			{Name: "second", Reader: second},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()

	// 重叠键：first（列表首）胜出。
	if got := s.GetString("log.level"); got != "debug" {
		t.Fatalf("log.level = %q, want debug (first layer wins)", got)
	}
	// second 独有键保留（字段级合并，非整文档替换）。
	if got := s.GetString("log.addr"); got != ":9090" {
		t.Fatalf("log.addr = %q, want :9090 (field-level merge)", got)
	}
	if got := s.GetString("replicas"); got != "1" {
		t.Fatalf("replicas = %q, want 1", got)
	}
}

// TestLoad_Layers_HotUpdate：层变更触发重合并；上层覆盖与本地覆盖保留。
func TestLoad_Layers_HotUpdate(t *testing.T) {
	basis := newStubLayerReader("http:\n  addr: \":8080\"\n  timeout: 30")
	localPath := writeTemp(t, "config.yaml", "http:\n  addr: \":9090\"")

	changed := make(chan struct{}, 4)
	s, err := Load(Options{
		Name:           "bald-demo",
		ConfigFile:     localPath,
		WatchLocalFile: true,
		Layers: []Layer{
			{Name: "basis", Reader: basis, Watch: true},
		},
		OnChange: func(map[string]any) { changed <- struct{}{} },
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()

	// 初始：本地覆盖 basis 的 addr，timeout 从 basis 继承。
	if got := s.GetString("http.addr"); got != ":9090" {
		t.Fatalf("http.addr = %q, want :9090 (local overrides layer)", got)
	}
	if got := s.GetString("http.timeout"); got != "30" {
		t.Fatalf("http.timeout = %q, want 30", got)
	}

	// 层热更新：timeout 变更；本地 addr 覆盖不被冲掉。
	basis.push("http:\n  addr: \":7000\"\n  timeout: 60")
	<-changed
	if got := s.GetString("http.timeout"); got != "60" {
		t.Fatalf("after push http.timeout = %q, want 60 (layer hot update)", got)
	}
	if got := s.GetString("http.addr"); got != ":9090" {
		t.Fatalf("after push http.addr = %q, want :9090 (local override preserved)", got)
	}
}

// TestLoad_Layers_FormatFallback：Format 为空且无 formatAware 时回退 yaml
// （yaml.v3 是 JSON 超集，JSON 文档天然兼容）。
func TestLoad_Layers_FormatFallback(t *testing.T) {
	r := newStubLayerReader(`{"log":{"level":"debug"}}`)
	s, err := Load(Options{
		Name:   "bald-demo",
		Layers: []Layer{{Name: "json-doc", Reader: r}}, // Format 未声明
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()
	if got := s.GetString("log.level"); got != "debug" {
		t.Fatalf("log.level = %q, want debug (json via yaml fallback)", got)
	}
}

// staticReader 仅实现 Reader（无 WatchValue），用于 Watch/ValueWatcher 校验测试。
type staticReader struct{ data []byte }

func (s *staticReader) Load(_ context.Context, _ string) ([]byte, error) {
	return s.data, nil
}

// TestLoad_Layers_WatchMismatch：Watch=true 但 Reader 未实现 ValueWatcher → fail-fast。
func TestLoad_Layers_WatchMismatch(t *testing.T) {
	r := &staticReader{data: []byte("log:\n  level: debug")}
	_, err := Load(Options{
		Name:     "bald-demo",
		Layers:   []Layer{{Name: "static", Reader: r, Watch: true}},
		OnChange: func(map[string]any) {}, // 有回调才会进入 watch 装配
	})
	if err == nil {
		t.Fatal("Load() = nil error, want Watch/ValueWatcher mismatch error")
	}
}

// TestLoad_Layers_RequireReader：Reader 为 nil 报错。
func TestLoad_Layers_RequireReader(t *testing.T) {
	_, err := Load(Options{
		Name:   "bald-demo",
		Layers: []Layer{{Name: "empty"}},
	})
	if err == nil {
		t.Fatal("Load() = nil error, want Reader-required error")
	}
}

// TestLoad_Layers_BelowEnv：契约层整体低于 env/flag 运维面。
func TestLoad_Layers_BelowEnv(t *testing.T) {
	t.Setenv("BALD_DEMO_LOG_LEVEL", "warn")
	r := newStubLayerReader("log:\n  level: debug")
	s, err := Load(Options{
		Name:   "bald-demo",
		Layers: []Layer{{Name: "basis", Reader: r}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()
	if got := s.GetString("log.level"); got != "warn" {
		t.Fatalf("log.level = %q, want warn (env above declared layers)", got)
	}
}

// TestLoad_Layers_EmptyDocument：层文档为空视为该层无配置（合法）。
func TestLoad_Layers_EmptyDocument(t *testing.T) {
	r := newStubLayerReader("")
	s, err := Load(Options{
		Name:   "bald-demo",
		Layers: []Layer{{Name: "empty-doc", Reader: r}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()
	if _, ok := s.Get("log"); ok {
		t.Fatal("log should be absent for empty layer document")
	}
}

// TestStore_Close_StopsWatch：Close 后层推送不再触发 OnChange（watch 生命周期归 Store）。
func TestStore_Close_StopsWatch(t *testing.T) {
	r := newStubLayerReader("log:\n  level: debug")
	fired := 0
	s, err := Load(Options{
		Name:     "bald-demo",
		Layers:   []Layer{{Name: "basis", Reader: r, Watch: true}},
		OnChange: func(map[string]any) { fired++ },
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r.push("log:\n  level: info")
	sleepMs(50)
	if fired != 0 {
		t.Fatalf("OnChange fired %d times after Close, want 0", fired)
	}
	// Close 幂等。
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestLoad_LocalFormatFallback：本地文件扩展名无法识别时回退 yaml 解析。
func TestLoad_LocalFormatFallback(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.conf")
	if err := os.WriteFile(p, []byte("log:\n  level: debug"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(Options{Name: "bald-demo", ConfigFile: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()
	if got := s.GetString("log.level"); got != "debug" {
		t.Fatalf("log.level = %q, want debug (yaml fallback)", got)
	}
}
