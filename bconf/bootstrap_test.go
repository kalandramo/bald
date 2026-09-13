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
			name: "empty item in slog output_paths",
			mutate: func(c *bootstrapv1.BootstrapConfig) {
				c.GetLogger().GetSlog().OutputPaths = []string{"stdout", ""}
			},
			wantSub: "slog.output_paths[1]",
		},
		{
			name: "negative slog rotate max_size",
			mutate: func(c *bootstrapv1.BootstrapConfig) {
				c.GetLogger().GetSlog().Rotate = &bootstrapv1.Logger_Slog_Rotate{Enabled: true, MaxSize: -1}
			},
			wantSub: "slog.rotate.max_size",
		},
		{
			name: "backends item without type",
			mutate: func(c *bootstrapv1.BootstrapConfig) {
				c.GetLogger().Backends = []*bootstrapv1.Logger_Backend{{Type: "slog", Slog: &bootstrapv1.Logger_Slog{Level: "info", Format: "console", OutputPath: "stdout"}}, {}}
			},
			wantSub: "backends[1].type",
		},
		{
			name: "backends item bad slog level",
			mutate: func(c *bootstrapv1.BootstrapConfig) {
				c.GetLogger().Backends = []*bootstrapv1.Logger_Backend{
					{Type: "slog", Slog: &bootstrapv1.Logger_Slog{Level: "verbose", Format: "console", OutputPath: "stdout"}},
				}
			},
			wantSub: "backends[0].slog.level",
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

// TestValidateSlogOutputPaths 多输出 + 轮转契约段：output_paths 提供时不再
// 要求单值 output_path；UnmarshalMap 能装载 snake_case 新字段。
func TestValidateSlogOutputPaths(t *testing.T) {
	cfg := NewBootstrap()
	cfg.GetLogger().GetSlog().OutputPath = "" // 单值让位，多值接管
	cfg.GetLogger().GetSlog().OutputPaths = []string{"stdout", "/var/log/app.log"}
	if err := Validate(cfg); err != nil {
		t.Fatalf("output_paths 应满足输出目标要求: %v", err)
	}

	// UnmarshalMap 装载路径（配置文件/env/flag 四源同构）。
	m := map[string]any{
		"logger": map[string]any{
			"slog": map[string]any{
				"output_paths": []any{"stdout", "/var/log/x.log"},
				"rotate":       map[string]any{"enabled": true, "max_size": 50},
			},
		},
	}
	cfg2 := NewBootstrap()
	if err := UnmarshalMap(m, cfg2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ps := cfg2.GetLogger().GetSlog().GetOutputPaths()
	if len(ps) != 2 || ps[0] != "stdout" || ps[1] != "/var/log/x.log" {
		t.Fatalf("output_paths = %v", ps)
	}
	r := cfg2.GetLogger().GetSlog().GetRotate()
	if r == nil || !r.GetEnabled() || r.GetMaxSize() != 50 {
		t.Fatalf("rotate = %+v", r)
	}
	if err := Validate(cfg2); err != nil {
		t.Fatalf("merged config should pass Validate: %v", err)
	}
}

// TestUnmarshalMapBackends 多后端声明经配置装载：列表项平移进 repeated Backends，
// 每项自带 type 与参数段。
func TestUnmarshalMapBackends(t *testing.T) {
	m := map[string]any{
		"logger": map[string]any{
			"backends": []any{
				map[string]any{"type": "slog", "slog": map[string]any{"level": "info", "output_path": "stdout"}},
				map[string]any{"type": "loki", "loki": map[string]any{"endpoint": "http://loki:3100/loki/api/v1/push"}},
			},
		},
	}
	cfg := NewBootstrap()
	if err := UnmarshalMap(m, cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	bs := cfg.GetLogger().GetBackends()
	if len(bs) != 2 {
		t.Fatalf("backends = %d, want 2", len(bs))
	}
	if bs[0].GetType() != "slog" || bs[0].GetSlog().GetLevel() != "info" {
		t.Fatalf("backends[0] = %+v", bs[0])
	}
	if bs[1].GetType() != "loki" || bs[1].GetLoki().GetEndpoint() == "" {
		t.Fatalf("backends[1] = %+v", bs[1])
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("backends config should pass Validate: %v", err)
	}
}
