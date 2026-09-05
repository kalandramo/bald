package bootstrap

import (
	"context"
	"errors"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconfig"
	"github.com/kalandramo/bald/bootstrap/config"
)

// stubReader 是最小 Reader 实现，固定返回预置数据。
type stubReader struct{ data []byte }

func (s *stubReader) Load(_ context.Context, _ string) ([]byte, error) {
	return s.data, nil
}

// stubLayerProvider 返回一个固定行为的 Provider：返回预置 layer / cleanup / err。
func stubLayerProvider(l *config.Layer, cleanup func(), err error) Provider {
	return func(context.Context, *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error) {
		return l, cleanup, err
	}
}

// stubLayerOf 快捷构造：仅带 Reader 的层（Build 负责回填名称）。
func stubLayerOf(rd bconfig.Reader) *config.Layer {
	return &config.Layer{Reader: rd}
}

func TestRegister(t *testing.T) {
	r := NewRegistry()

	if err := r.Register("env", stubLayerProvider(stubLayerOf(&stubReader{}), nil, nil)); err != nil {
		t.Fatalf("Register(first) = %v, want nil", err)
	}
	if err := r.Register("file", stubLayerProvider(stubLayerOf(&stubReader{}), nil, nil)); err != nil {
		t.Fatalf("Register(second) = %v, want nil", err)
	}

	// 重名报错（fail-fast，不覆盖）。
	if err := r.Register("env", stubLayerProvider(stubLayerOf(&stubReader{}), nil, nil)); err == nil {
		t.Fatal("Register(duplicate) = nil, want error")
	}

	// 空名 / nil provider 报错。
	if err := r.Register("", stubLayerProvider(stubLayerOf(&stubReader{}), nil, nil)); err == nil {
		t.Fatal("Register(empty name) = nil, want error")
	}
	if err := r.Register("nilp", nil); err == nil {
		t.Fatal("Register(nil provider) = nil, want error")
	}
}

func TestMustRegister_PanicsOnDuplicate(t *testing.T) {
	r := NewRegistry()
	r.MustRegister("env", stubLayerProvider(stubLayerOf(&stubReader{}), nil, nil))

	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister(duplicate) did not panic")
		}
	}()
	r.MustRegister("env", stubLayerProvider(stubLayerOf(&stubReader{}), nil, nil))
}

// TestBuild_OrderIsPriority 验证注册序 = 层优先级：先注册的源排在列表首位（最高）。
func TestBuild_OrderIsPriority(t *testing.T) {
	r := NewRegistry()
	r.MustRegister("first", stubLayerProvider(stubLayerOf(&stubReader{data: []byte("from-first")}), nil, nil))
	r.MustRegister("second", stubLayerProvider(stubLayerOf(&stubReader{data: []byte("from-second")}), nil, nil))

	layers, cleanup, err := r.Build(context.Background(), &bootstrapv1.BootstrapConfig{})
	if err != nil {
		t.Fatalf("Build() = %v, want nil", err)
	}
	defer cleanup()

	if len(layers) != 2 {
		t.Fatalf("len(layers) = %d, want 2", len(layers))
	}
	if layers[0].Name != "first" {
		t.Fatalf("layers[0].Name = %q, want first (registration order = priority)", layers[0].Name)
	}
	data, err := layers[0].Reader.Load(context.Background(), "")
	if err != nil || string(data) != "from-first" {
		t.Fatalf("Load() = (%q, %v), want (%q, nil)", data, err, "from-first")
	}
}

// TestBuild_SkipsUnconfiguredSources 验证返回 nil layer 的 provider 被跳过（非错误）。
func TestBuild_SkipsUnconfiguredSources(t *testing.T) {
	r := NewRegistry()
	r.MustRegister("unconfigured", stubLayerProvider(nil, nil, nil))
	r.MustRegister("configured", stubLayerProvider(stubLayerOf(&stubReader{data: []byte("ok")}), nil, nil))

	layers, cleanup, err := r.Build(context.Background(), &bootstrapv1.BootstrapConfig{})
	if err != nil {
		t.Fatalf("Build() = %v, want nil", err)
	}
	defer cleanup()

	if len(layers) != 1 || layers[0].Name != "configured" {
		t.Fatalf("layers = %v, want only [configured]", layerNames(layers))
	}
}

// TestBuild_NameFallback：provider 未填层名时由 Build 回填注册名。
func TestBuild_NameFallback(t *testing.T) {
	r := NewRegistry()
	r.MustRegister("basis", stubLayerProvider(stubLayerOf(&stubReader{data: []byte("ok")}), nil, nil))

	layers, cleanup, err := r.Build(context.Background(), &bootstrapv1.BootstrapConfig{})
	if err != nil {
		t.Fatalf("Build() = %v, want nil", err)
	}
	defer cleanup()
	if layers[0].Name != "basis" {
		t.Fatalf("layers[0].Name = %q, want basis (Build fills registered name)", layers[0].Name)
	}
}

// TestBuild_NilReaderRejected：层 Reader 为 nil 是装配错误（fail-fast 回滚）。
func TestBuild_NilReaderRejected(t *testing.T) {
	r := NewRegistry()
	r.MustRegister("bad", stubLayerProvider(&config.Layer{}, func() {}, nil))

	if _, cleanup, err := r.Build(context.Background(), &bootstrapv1.BootstrapConfig{}); err == nil {
		t.Fatal("Build(nil Reader) = nil error, want error")
	} else if cleanup != nil {
		t.Fatal("Build cleanup != nil on failure, want nil (already rolled back)")
	}
}

// TestBuild_WatchMismatchRejected：Watch=true 但 Reader 非 ValueWatcher 报错。
func TestBuild_WatchMismatchRejected(t *testing.T) {
	r := NewRegistry()
	r.MustRegister("bad", stubLayerProvider(&config.Layer{Reader: &stubReader{}, Watch: true}, nil, nil))

	if _, _, err := r.Build(context.Background(), &bootstrapv1.BootstrapConfig{}); err == nil {
		t.Fatal("Build(Watch mismatch) = nil error, want error")
	}
}

// TestBuild_RollbackOnError 验证失败短路并回滚已构造源（逆序执行 closer）。
func TestBuild_RollbackOnError(t *testing.T) {
	var order []string
	mkCloser := func(name string) func() {
		return func() { order = append(order, name) }
	}

	r := NewRegistry()
	r.MustRegister("ok", stubLayerProvider(stubLayerOf(&stubReader{}), mkCloser("ok"), nil))
	r.MustRegister("ok2", stubLayerProvider(stubLayerOf(&stubReader{}), mkCloser("ok2"), nil))
	r.MustRegister("boom", stubLayerProvider(nil, nil, errors.New("boom")))

	_, cleanup, err := r.Build(context.Background(), &bootstrapv1.BootstrapConfig{})
	if err == nil {
		t.Fatal("Build() = nil error, want boom error")
	}
	if cleanup != nil {
		t.Fatal("Build() cleanup != nil on failure, want nil (already rolled back)")
	}
	if len(order) != 2 || order[0] != "ok2" || order[1] != "ok" {
		t.Fatalf("rollback order = %v, want [ok2 ok] (reverse)", order)
	}
}

// TestBuild_NoSourceConfigured 验证所有源都未配置时报错而非空层列表。
func TestBuild_NoSourceConfigured(t *testing.T) {
	r := NewRegistry()
	r.MustRegister("unconfigured", stubLayerProvider(nil, nil, nil))

	_, _, err := r.Build(context.Background(), &bootstrapv1.BootstrapConfig{})
	if err == nil {
		t.Fatal("Build() = nil error, want no-config-source error")
	}
}

func TestBuild_NilConfig(t *testing.T) {
	r := NewRegistry()
	r.MustRegister("env", stubLayerProvider(stubLayerOf(&stubReader{}), nil, nil))

	if _, _, err := r.Build(context.Background(), nil); err == nil {
		t.Fatal("Build(nil cfg) = nil error, want error")
	}
}

func layerNames(layers []config.Layer) []string {
	names := make([]string, 0, len(layers))
	for _, l := range layers {
		names = append(names, l.Name)
	}
	return names
}
