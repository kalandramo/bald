package config

import (
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// TestUnmarshal_FromFile 验证来自本地文件的配置（值类型已经是"正确"的）。
func TestUnmarshal_FromFile(t *testing.T) {
	settings := map[string]any{
		"server": map[string]any{
			"http": map[string]any{"addr": ":8080"},
			"grpc": map[string]any{"addr": ":9090"},
		},
		"logger": map[string]any{
			"type": "slog",
			"slog": map[string]any{"level": "debug", "format": "json", "output_path": "stdout"},
		},
	}

	cfg := &bootstrapv1.BootstrapConfig{}
	if err := Unmarshal(settings, cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got := cfg.GetServer().GetHttp().GetAddr(); got != ":8080" {
		t.Errorf("server.http.addr = %q, want :8080", got)
	}
	if got := cfg.GetServer().GetGrpc().GetAddr(); got != ":9090" {
		t.Errorf("server.grpc.addr = %q, want :9090", got)
	}
	if got := cfg.GetLogger().GetSlog().GetLevel(); got != "debug" {
		t.Errorf("logger.slog.level = %q, want debug", got)
	}
	if got := cfg.GetLogger().GetSlog().GetFormat(); got != "json" {
		t.Errorf("logger.slog.format = %q, want json", got)
	}
}

// TestUnmarshal_DurationFromFlag 验证 pflag.DurationVar 产生的 time.Duration
// 能被正确规范化为 protojson 可接受的 duration 串。
// 关键坑：map 里 Duration 可能是 time.Duration 或纳秒整数，
// protojson 只接受 "10s" 形式的字符串。
func TestUnmarshal_DurationFromFlag(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  time.Duration
	}{
		{"time.Duration", 10 * time.Second, 10 * time.Second},
		{"纳秒整数", int64(10 * time.Second), 10 * time.Second},
		{"字符串", "10s", 10 * time.Second},
		// 回归：time.Duration.String() 会给出 "1m30s"，protojson 不接受，
		// 必须格式化成 "90s"。
		{"跨分钟", 90 * time.Second, 90 * time.Second},
		{"毫秒", 1500 * time.Millisecond, 1500 * time.Millisecond},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := map[string]any{
				"app": map[string]any{"stop_timeout": tc.value},
			}
			cfg := &bootstrapv1.BootstrapConfig{}
			if err := Unmarshal(settings, cfg); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got := cfg.GetApp().GetStopTimeout().AsDuration(); got != tc.want {
				t.Errorf("app.stop_timeout = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUnmarshal_StringValuesFromEnv 验证 env / flag 带来的字符串能被转成
// proto 声明的类型（protojson 严格模式不会自动转 "true" → bool）。
func TestUnmarshal_StringValuesFromEnv(t *testing.T) {
	settings := map[string]any{
		"server": map[string]any{
			"grpc": map[string]any{"reflection": "true"}, // env: 一切皆字符串
		},
	}
	cfg := &bootstrapv1.BootstrapConfig{}
	if err := Unmarshal(settings, cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !cfg.GetServer().GetGrpc().GetReflection() {
		t.Errorf("server.grpc.reflection = false, want true")
	}
}

// TestUnmarshal_KeepsDefaults 验证未出现在配置中的字段保持调用方预设的默认值。
func TestUnmarshal_KeepsDefaults(t *testing.T) {
	cfg := &bootstrapv1.BootstrapConfig{
		Server: &bootstrapv1.Server{
			Http: &bootstrapv1.Server_Http{Addr: ":443"},
			Grpc: &bootstrapv1.Server_Grpc{Addr: ":9090"},
		},
	}
	settings := map[string]any{
		"server": map[string]any{"http": map[string]any{"addr": ":18080"}}, // 只覆盖 http.addr
	}
	if err := Unmarshal(settings, cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := cfg.GetServer().GetHttp().GetAddr(); got != ":18080" {
		t.Errorf("server.http.addr = %q, want :18080", got)
	}
	if got := cfg.GetServer().GetGrpc().GetAddr(); got != ":9090" {
		t.Errorf("server.grpc.addr = %v, want 保持默认 :9090", got)
	}
}

// TestUnmarshal_ListReplacesDefault 回归防护：repeated 字段必须是「替换」而非「追加」。
// proto.Merge 对 list 的语义是 append，若不清理会合并成两遍。
func TestUnmarshal_ListReplacesDefault(t *testing.T) {
	newCfg := func() *bootstrapv1.BootstrapConfig {
		return &bootstrapv1.BootstrapConfig{
			Server: &bootstrapv1.Server{
				Http: &bootstrapv1.Server_Http{
					Middleware: &bootstrapv1.Server_Http_Middleware{
						Logging: &bootstrapv1.Server_Http_Middleware_Logging{
							SkipPaths: []string{"/healthz"},
						},
					},
				},
			},
		}
	}

	// 配置给多目标时，整体替换而非追加。
	cfg2 := newCfg()
	settings2 := map[string]any{
		"server": map[string]any{"http": map[string]any{
			"middleware": map[string]any{"logging": map[string]any{"skip_paths": []any{"/a", "/b"}}},
		}},
	}
	if err := Unmarshal(settings2, cfg2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := cfg2.GetServer().GetHttp().GetMiddleware().GetLogging().GetSkipPaths(); len(got) != 2 {
		t.Fatalf("skip_paths = %v, want 2 items", got)
	}

	// 配置中未出现 list 字段时，默认值保留。
	cfg3 := newCfg()
	if err := Unmarshal(map[string]any{
		"server": map[string]any{"http": map[string]any{"addr": ":8080"}},
	}, cfg3); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := cfg3.GetServer().GetHttp().GetMiddleware().GetLogging().GetSkipPaths(); len(got) != 1 || got[0] != "/healthz" {
		t.Fatalf("skip_paths = %v, want 保留默认 [/healthz]", got)
	}
}

// TestUnmarshal_DiscardUnknown 验证配置文件中的业务配置段（proto 未声明）
// 不会导致解析失败——proto 只约束框架级配置。
func TestUnmarshal_DiscardUnknown(t *testing.T) {
	settings := map[string]any{
		"server":    map[string]any{"http": map[string]any{"addr": ":8080"}},
		"business":  map[string]any{"feature-flag": true},
		"databases": []any{map[string]any{"dsn": "mysql://..."}},
	}
	cfg := &bootstrapv1.BootstrapConfig{}
	if err := Unmarshal(settings, cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := cfg.GetServer().GetHttp().GetAddr(); got != ":8080" {
		t.Errorf("server.http.addr = %q, want :8080", got)
	}
}

// TestUnmarshal_Errors 验证非法取值被明确报错（而非静默落到零值）。
func TestUnmarshal_Errors(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]any
	}{
		{"非法 duration", map[string]any{"app": map[string]any{"stop_timeout": "abc"}}},
		{"非法 bool", map[string]any{"server": map[string]any{"grpc": map[string]any{"reflection": "yes"}}}},
		{"消息字段给标量", map[string]any{"server": "not-an-object"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &bootstrapv1.BootstrapConfig{}
			if err := Unmarshal(tc.settings, cfg); err == nil {
				t.Errorf("Unmarshal succeeded, want error")
			}
		})
	}
}

// TestUnmarshal_Nil 验证 nil 入参语义：nil map 等价空配置（msg 保持默认值），
// nil msg 被明确拒绝。
func TestUnmarshal_Nil(t *testing.T) {
	cfg := &bootstrapv1.BootstrapConfig{
		Server: &bootstrapv1.Server{
			Http: &bootstrapv1.Server_Http{Addr: ":443"},
		},
	}
	if err := Unmarshal(nil, cfg); err != nil {
		t.Fatalf("Unmarshal(nil, cfg) failed: %v", err)
	}
	if got := cfg.GetServer().GetHttp().GetAddr(); got != ":443" {
		t.Errorf("server.http.addr = %q, want :443 (nil map is empty config, defaults kept)", got)
	}
	if err := Unmarshal(map[string]any{}, nil); err == nil {
		t.Error("Unmarshal(m, nil) succeeded, want error")
	}
}
