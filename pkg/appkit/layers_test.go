package appkit

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/bootstrap/config"
)

// layerStubReader 最小 ValueWatcher 桩：固定文档 + 可推送变更。
type layerStubReader struct {
	data []byte
	ch   chan []byte
}

func newLayerStub(doc string) *layerStubReader {
	return &layerStubReader{data: []byte(doc), ch: make(chan []byte, 1)}
}

func (s *layerStubReader) Load(_ context.Context, _ string) ([]byte, error) {
	return s.data, nil
}
func (s *layerStubReader) WatchValue(_ context.Context, _ string) (<-chan []byte, error) {
	return s.ch, nil
}
func (s *layerStubReader) push(doc string) {
	select {
	case s.ch <- []byte(doc):
	default:
	}
}

// TestAppKit_ConfigLayers 链路验证：ConfigLayers 透传至 Store，
// 层值生效、低于本地文件、列表首层优先级最高、热更新触发回调。
func TestAppKit_ConfigLayers(t *testing.T) {
	first := newLayerStub("log:\n  level: debug\n  from: first")
	second := newLayerStub("log:\n  level: info\n  extra: \"1\"")
	cfgFile := writeCfg(t, "log:\n  level: \"warn\"\n")

	withArgs(t, []string{"--config=" + cfgFile})

	changed := make(chan struct{}, 4)
	a := New(
		Name("bald-test"),
		ConfigLayers(
			config.Layer{Name: "first", Reader: first, Watch: true},
			config.Layer{Name: "second", Reader: second},
		),
		OnConfigChange(func(map[string]any) { changed <- struct{}{} }),
	)
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	defer a.cfg.closeStore()

	// 本地文件压过全部契约层。
	if got := a.Config().GetString("log.level"); got != "warn" {
		t.Errorf("log.level = %q, want warn (local file above layers)", got)
	}
	// 列表首层胜出重叠键；后层独有键保留（字段级合并）。
	if got := a.Config().GetString("log.from"); got != "first" {
		t.Errorf("log.from = %q, want first", got)
	}
	if got := a.Config().GetString("log.extra"); got != "1" {
		t.Errorf("log.extra = %q, want 1 (field-level merge)", got)
	}

	// 契约层热更新：first 层变更触发 OnConfigChange，且新值生效。
	first.push("log:\n  level: debug\n  from: pushed")
	<-changed
	if got := a.Config().GetString("log.from"); got != "pushed" {
		t.Errorf("log.from = %q, want pushed (layer hot update via appkit)", got)
	}
	if got := a.Config().GetString("log.level"); got != "warn" {
		t.Errorf("log.level = %q, want warn (local still above layers)", got)
	}
}
