package contract

import (
	"context"
	"path/filepath"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
)

// Provider 把契约 charm 段映射为终端后端；level/format/output_path 与 slog 段同形状。
func TestProvider_Defaults(t *testing.T) {
	cfg := &bootstrapv1.Logger{Type: "charm", Charm: &bootstrapv1.Logger_Charm{}}
	l, cleanup, err := Provider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if l == nil {
		t.Fatal("nil logger")
	}
	defer cleanup()

	// 默认 info：debug 不输出，info 输出（Enabled 语义）。
	if l.Enabled(log.LevelDebug) {
		t.Fatal("debug should be disabled at default level")
	}
	if !l.Enabled(log.LevelInfo) {
		t.Fatal("info should be enabled at default level")
	}
}

func TestProvider_LevelAndFileOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "charm.log")
	cfg := &bootstrapv1.Logger{
		Type: "charm",
		Charm: &bootstrapv1.Logger_Charm{
			Level:      "debug",
			Format:     "text",
			OutputPath: out,
		},
	}
	l, cleanup, err := Provider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	defer cleanup()

	// level=debug 生效。
	if !l.Enabled(log.LevelDebug) {
		t.Fatal("debug should be enabled after level=debug")
	}
}

func TestProvider_NilSegment(t *testing.T) {
	cfg := &bootstrapv1.Logger{Type: "charm"}
	if _, _, err := Provider(context.Background(), cfg); err == nil {
		t.Fatal("expected fail-fast on nil charm segment")
	}
}
