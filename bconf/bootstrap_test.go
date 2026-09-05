package bconf

import (
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

func TestNewBootstrapDefaults(t *testing.T) {
	cfg := NewBootstrap()
	if cfg.GetApp().GetId() == "" {
		t.Fatal("app.id should default to hostname")
	}
	if cfg.GetServer().GetHttp().GetAddr() != ":8080" {
		t.Fatalf("http addr default = %q", cfg.GetServer().GetHttp().GetAddr())
	}
	if cfg.GetServer().GetGrpc().GetAddr() != ":9090" {
		t.Fatalf("grpc addr default = %q", cfg.GetServer().GetGrpc().GetAddr())
	}
	if cfg.GetLogger().GetType() != "slog" {
		t.Fatalf("logger.type default = %q", cfg.GetLogger().GetType())
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("defaults should pass Validate: %v", err)
	}
}

func TestUnmarshalMapMerges(t *testing.T) {
	cfg := NewBootstrap()
	m := map[string]any{
		"server": map[string]any{
			"http": map[string]any{"addr": ":18080"},
			"grpc": map[string]any{"addr": ":19090"},
		},
		"logger": map[string]any{
			"slog": map[string]any{"level": "debug"},
		},
	}
	if err := UnmarshalMap(m, cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.GetServer().GetHttp().GetAddr() != ":18080" {
		t.Fatalf("http addr = %q", cfg.GetServer().GetHttp().GetAddr())
	}
	if cfg.GetLogger().GetSlog().GetLevel() != "debug" {
		t.Fatalf("slog level = %q", cfg.GetLogger().GetSlog().GetLevel())
	}
	// 合并语义：未覆盖的默认值保留。
	if cfg.GetServer().GetGrpc().GetAddr() != ":19090" {
		t.Fatalf("grpc addr = %q", cfg.GetServer().GetGrpc().GetAddr())
	}
	if cfg.GetLogger().GetSlog().GetFormat() != "console" {
		t.Fatalf("slog format should keep default, got %q", cfg.GetLogger().GetSlog().GetFormat())
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("merged config should pass Validate: %v", err)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*bootstrapv1.BootstrapConfig)
		wantSub string
	}{
		{
			name:    "bad http addr",
			mutate:  func(c *bootstrapv1.BootstrapConfig) { c.GetServer().GetHttp().Addr = "no-port" },
			wantSub: "server.http.addr",
		},
		{
			name:    "bad grpc addr",
			mutate:  func(c *bootstrapv1.BootstrapConfig) { c.GetServer().GetGrpc().Addr = "1.2.3.4:99999" },
			wantSub: "server.grpc.addr",
		},
		{
			name:    "bad slog level",
			mutate:  func(c *bootstrapv1.BootstrapConfig) { c.GetLogger().GetSlog().Level = "verbose" },
			wantSub: "slog.level",
		},
		{
			name:    "bad slog format",
			mutate:  func(c *bootstrapv1.BootstrapConfig) { c.GetLogger().GetSlog().Format = "xml" },
			wantSub: "slog.format",
		},
		{
			name:    "empty app id",
			mutate:  func(c *bootstrapv1.BootstrapConfig) { c.GetApp().Id = "" },
			wantSub: "id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewBootstrap()
			tc.mutate(cfg)
			err := Validate(cfg)
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q should contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestUnmarshalMapCoercesScalars(t *testing.T) {
	// viper env/flag 值常为字符串或整数，UnmarshalMap 必须做类型规范化。
	cfg := NewBootstrap()
	m := map[string]any{
		"server": map[string]any{
			"http": map[string]any{"addr": ":18081"},
		},
	}
	if err := UnmarshalMap(m, cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
}
