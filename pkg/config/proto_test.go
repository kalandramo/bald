package config

import (
	"testing"
	"time"

	"github.com/spf13/viper"
	"google.golang.org/protobuf/types/known/durationpb"

	confv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/config/v1"
)

// newSettingsViper 用给定 map 构造 viper，模拟 config.Load 的合并结果。
func newSettingsViper(t *testing.T, settings map[string]any) *viper.Viper {
	t.Helper()
	v := viper.New()
	if err := v.MergeConfigMap(settings); err != nil {
		t.Fatalf("MergeConfigMap: %v", err)
	}
	return v
}

// TestUnmarshal_FromFile 验证来自本地文件的配置（值类型已经是“正确”的）。
func TestUnmarshal_FromFile(t *testing.T) {
	v := newSettingsViper(t, map[string]any{
		"http": map[string]any{
			"addr":    ":8080",
			"timeout": "10s",
			"tls":     map[string]any{"enabled": false},
		},
		"grpc": map[string]any{
			"network": "tcp",
			"addr":    ":9090",
			"timeout": "30s",
		},
		"log": map[string]any{
			"level":        "debug",
			"format":       "json",
			"output-paths": []any{"stdout", "/var/log/bald.log"},
		},
	})

	cfg := &confv1.Bootstrap{}
	if err := Unmarshal(v, cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got := cfg.GetHttp().GetAddr(); got != ":8080" {
		t.Errorf("http.addr = %q, want :8080", got)
	}
	if got := cfg.GetHttp().GetTimeout().AsDuration(); got != 10*time.Second {
		t.Errorf("http.timeout = %v, want 10s", got)
	}
	if got := cfg.GetGrpc().GetAddr(); got != ":9090" {
		t.Errorf("grpc.addr = %q, want :9090", got)
	}
	if got := cfg.GetLogger().GetLevel(); got != "debug" {
		t.Errorf("log.level = %q, want debug", got)
	}
	if got := len(cfg.GetLogger().GetOutputPaths()); got != 2 {
		t.Errorf("len(log.output-paths) = %d, want 2", got)
	}
}

// TestUnmarshal_DurationFromFlag 验证 pflag.DurationVar 产生的 time.Duration
// 能被正确规范化为 protojson 可接受的 duration 串。
//
// 这是「加载器不动、proto 做契约」路线的关键坑：viper 里 Duration 可能是
// time.Duration 或纳秒整数，protojson 只接受 "10s" 形式的字符串。
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
			v := newSettingsViper(t, map[string]any{
				"http": map[string]any{"timeout": tc.value},
			})
			cfg := &confv1.Bootstrap{}
			if err := Unmarshal(v, cfg); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got := cfg.GetHttp().GetTimeout().AsDuration(); got != tc.want {
				t.Errorf("http.timeout = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUnmarshal_StringValuesFromEnv 验证 env / flag 带来的字符串能被转成
// proto 声明的类型（protojson 严格模式不会自动转 "true" → bool）。
func TestUnmarshal_StringValuesFromEnv(t *testing.T) {
	v := newSettingsViper(t, map[string]any{
		"http": map[string]any{
			"tls": map[string]any{"enabled": "true"}, // env: 一切皆字符串
		},
	})
	cfg := &confv1.Bootstrap{}
	if err := Unmarshal(v, cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !cfg.GetHttp().GetTls().GetEnabled() {
		t.Errorf("http.tls.enabled = false, want true")
	}
}

// TestUnmarshal_KeepsDefaults 验证未出现在配置中的字段保持调用方预设的默认值。
// 这是「先 NewBootstrap() 填默认值，再 Unmarshal」这一用法的基础。
func TestUnmarshal_KeepsDefaults(t *testing.T) {
	cfg := &confv1.Bootstrap{
		Http: &confv1.Http{Addr: ":443", Timeout: durationpb.New(10 * time.Second)},
	}
	v := newSettingsViper(t, map[string]any{
		"http": map[string]any{"addr": ":18080"}, // 只覆盖 addr
	})
	if err := Unmarshal(v, cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := cfg.GetHttp().GetAddr(); got != ":18080" {
		t.Errorf("http.addr = %q, want :18080", got)
	}
	if got := cfg.GetHttp().GetTimeout().AsDuration(); got != 10*time.Second {
		t.Errorf("http.timeout = %v, want 保持默认 10s", got)
	}
}

// TestUnmarshal_ListReplacesDefault 回归防护：repeated 字段必须是「替换」而非「追加」。
//
// proto.Merge 对 list 的语义是 append，若不做处理，
// 默认值 ["stdout"] 与配置值 ["stdout"] 会合并成 ["stdout", "stdout"]，
// 实际表现为日志向 stdout 写两遍。
func TestUnmarshal_ListReplacesDefault(t *testing.T) {
	cfg := &confv1.Bootstrap{Logger: &confv1.Logger{OutputPaths: []string{"stdout"}}}

	v := newSettingsViper(t, map[string]any{
		"log": map[string]any{"output-paths": []any{"stdout"}},
	})
	if err := Unmarshal(v, cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := cfg.GetLogger().GetOutputPaths(); len(got) != 1 || got[0] != "stdout" {
		t.Fatalf("log.output-paths = %v, want [stdout]", got)
	}

	// 配置给多个目标时，整体替换而非追加。
	cfg2 := &confv1.Bootstrap{Logger: &confv1.Logger{OutputPaths: []string{"stdout"}}}
	v2 := newSettingsViper(t, map[string]any{
		"log": map[string]any{"output-paths": []any{"stderr", "/var/log/bald.log"}},
	})
	if err := Unmarshal(v2, cfg2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := cfg2.GetLogger().GetOutputPaths(); len(got) != 2 {
		t.Fatalf("log.output-paths = %v, want 2 items (stderr + file)", got)
	}

	// 配置中未出现 list 字段时，默认值保留。
	cfg3 := &confv1.Bootstrap{Logger: &confv1.Logger{OutputPaths: []string{"stdout"}}}
	if err := Unmarshal(newSettingsViper(t, map[string]any{"log": map[string]any{"level": "debug"}}), cfg3); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := cfg3.GetLogger().GetOutputPaths(); len(got) != 1 || got[0] != "stdout" {
		t.Fatalf("log.output-paths = %v, want 保留默认 [stdout]", got)
	}
}

// TestUnmarshal_NestedListReplacesDefault 验证嵌套消息内的 list 字段同样被替换。
func TestUnmarshal_NestedListReplacesDefault(t *testing.T) {
	v := newSettingsViper(t, map[string]any{
		"http": map[string]any{"tls": map[string]any{"enabled": true}},
	})
	cfg := &confv1.Bootstrap{Http: &confv1.Http{Tls: &confv1.Tls{Enabled: true}}}
	if err := Unmarshal(v, cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !cfg.GetHttp().GetTls().GetEnabled() {
		t.Errorf("http.tls.enabled = false, want true")
	}
}

// TestUnmarshal_DiscardUnknown 验证配置文件中的业务配置段（proto 未声明）
// 不会导致解析失败——proto 只约束框架级配置。
func TestUnmarshal_DiscardUnknown(t *testing.T) {
	v := newSettingsViper(t, map[string]any{
		"http":      map[string]any{"addr": ":8080"},
		"business":  map[string]any{"feature-flag": true},
		"databases": []any{map[string]any{"dsn": "mysql://..."}},
	})
	cfg := &confv1.Bootstrap{}
	if err := Unmarshal(v, cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := cfg.GetHttp().GetAddr(); got != ":8080" {
		t.Errorf("http.addr = %q, want :8080", got)
	}
}

// TestUnmarshal_Errors 验证非法取值被明确报错（而非静默落到零值）。
func TestUnmarshal_Errors(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]any
	}{
		{"非法 duration", map[string]any{"http": map[string]any{"timeout": "abc"}}},
		{"非法 bool", map[string]any{"http": map[string]any{"tls": map[string]any{"enabled": "yes"}}}},
		{"消息字段给标量", map[string]any{"http": "not-an-object"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &confv1.Bootstrap{}
			if err := Unmarshal(newSettingsViper(t, tc.settings), cfg); err == nil {
				t.Errorf("Unmarshal succeeded, want error")
			}
		})
	}
}

// TestUnmarshal_Nil 验证 nil 入参被明确拒绝。
func TestUnmarshal_Nil(t *testing.T) {
	cfg := &confv1.Bootstrap{}
	if err := Unmarshal(nil, cfg); err == nil {
		t.Error("Unmarshal(nil, cfg) succeeded, want error")
	}
	if err := Unmarshal(viper.New(), nil); err == nil {
		t.Error("Unmarshal(v, nil) succeeded, want error")
	}
}

// TestCoerceDurationFormat 直接验证 duration 格式化：不能用 time.Duration.String()，
// 因为 "1m30s" 这类复合表示会被 protojson 拒绝。
func TestCoerceDurationFormat(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{10 * time.Second, "10s"},
		{90 * time.Second, "90s"}, // 不是 "1m30s"
		{1500 * time.Millisecond, "1.5s"},
		{0, "0s"},
	}
	for _, tc := range cases {
		if got := formatDuration(tc.in); got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
