package bconf

import (
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// defaultSlog 返回默认配置（NewBootstrap）首个后端的 slog 段。
func defaultSlog(c *bootstrapv1.BootstrapConfig) *bootstrapv1.Logger_Slog {
	return c.GetLogger().GetBackends()[0].GetSlog()
}

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
	bs := cfg.GetLogger().GetBackends()
	if len(bs) != 1 || bs[0].GetType() != "slog" {
		t.Fatalf("logger.backends default = %+v, want single slog", bs)
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
			// repeated 整体替换：默认 backends 项被此清单替换，
			// 项内未写字段不继承默认（装配层回退，字段缺省是常态）。
			"backends": []any{
				map[string]any{"type": "slog", "slog": map[string]any{"level": "debug", "output_path": "stdout"}},
			},
		},
	}
	if err := UnmarshalMap(m, cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.GetServer().GetHttp().GetAddr() != ":18080" {
		t.Fatalf("http addr = %q", cfg.GetServer().GetHttp().GetAddr())
	}
	// 合并语义：未覆盖的默认值保留。
	if cfg.GetServer().GetGrpc().GetAddr() != ":19090" {
		t.Fatalf("grpc addr = %q", cfg.GetServer().GetGrpc().GetAddr())
	}
	bs := cfg.GetLogger().GetBackends()
	if len(bs) != 1 || bs[0].GetSlog().GetLevel() != "debug" {
		t.Fatalf("backends[0] = %+v, want level=debug", bs[0])
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
			mutate:  func(c *bootstrapv1.BootstrapConfig) { defaultSlog(c).Level = "verbose" },
			wantSub: "backends[0].slog.level",
		},
		{
			name:    "bad slog format",
			mutate:  func(c *bootstrapv1.BootstrapConfig) { defaultSlog(c).Format = "xml" },
			wantSub: "backends[0].slog.format",
		},
		{
			name: "empty item in slog output_paths",
			mutate: func(c *bootstrapv1.BootstrapConfig) {
				defaultSlog(c).OutputPaths = []string{"stdout", ""}
			},
			wantSub: "slog.output_paths[1]",
		},
		{
			name: "negative slog rotate max_size",
			mutate: func(c *bootstrapv1.BootstrapConfig) {
				defaultSlog(c).Rotate = &bootstrapv1.Logger_Slog_Rotate{Enabled: true, MaxSize: -1}
			},
			wantSub: "slog.rotate.max_size",
		},
		{
			name: "empty backends",
			mutate: func(c *bootstrapv1.BootstrapConfig) {
				c.GetLogger().Backends = nil
			},
			wantSub: "backends must not be empty",
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
		{
			name: "filter_keys empty item",
			mutate: func(c *bootstrapv1.BootstrapConfig) {
				c.GetLogger().FilterKeys = []string{"password", ""}
			},
			wantSub: "filter_keys[1]",
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
	defaultSlog(cfg).OutputPath = "" // 单值让位，多值接管
	defaultSlog(cfg).OutputPaths = []string{"stdout", "/var/log/app.log"}
	if err := Validate(cfg); err != nil {
		t.Fatalf("output_paths 应满足输出目标要求: %v", err)
	}

	// UnmarshalMap 装载路径（配置文件/env/flag 四源同构）。
	m := map[string]any{
		"logger": map[string]any{
			"backends": []any{
				map[string]any{
					"type": "slog",
					"slog": map[string]any{
						"output_paths": []any{"stdout", "/var/log/x.log"},
						"rotate":       map[string]any{"enabled": true, "max_size": 50},
					},
				},
			},
		},
	}
	cfg2 := NewBootstrap()
	if err := UnmarshalMap(m, cfg2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	s := cfg2.GetLogger().GetBackends()[0].GetSlog()
	ps := s.GetOutputPaths()
	if len(ps) != 2 || ps[0] != "stdout" || ps[1] != "/var/log/x.log" {
		t.Fatalf("output_paths = %v", ps)
	}
	r := s.GetRotate()
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

// TestValidateLoggerFilterKeys 全局脱敏契约段：filter_keys 合法清单通过校验，
// UnmarshalMap 能装载 snake_case 字段（四源同构）。
func TestValidateLoggerFilterKeys(t *testing.T) {
	cfg := NewBootstrap()
	cfg.GetLogger().FilterKeys = []string{"password", "access_token"}
	if err := Validate(cfg); err != nil {
		t.Fatalf("filter_keys 合法清单应通过: %v", err)
	}

	m := map[string]any{
		"logger": map[string]any{
			"filter_keys": []any{"password", "id_card"},
		},
	}
	cfg2 := NewBootstrap()
	if err := UnmarshalMap(m, cfg2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := cfg2.GetLogger().GetFilterKeys()
	if len(got) != 2 || got[0] != "password" || got[1] != "id_card" {
		t.Fatalf("filter_keys 装载 = %v", got)
	}
}

// TestValidateLoggerBackendSections 五种非 slog 后端段的形状校验：合法配置
// 通过（凭证留空走 SDK env 凭证链是合法路径，不应被校验挡下），漏配连接
// 目标与越域取值 fail-fast 且错误带 backends[i].<seg> 定位。
func TestValidateLoggerBackendSections(t *testing.T) {
	// 合法：五后端各一项，凭证全部留空。
	cfg := NewBootstrap()
	cfg.GetLogger().Backends = []*bootstrapv1.Logger_Backend{
		{Type: "charm", Charm: &bootstrapv1.Logger_Charm{Level: "debug", Format: "json"}},
		{Type: "loki", Loki: &bootstrapv1.Logger_Loki{Endpoint: "http://loki:3100/loki/api/v1/push", BatchSize: 100, FlushInterval: 5000}},
		{Type: "sentry", Sentry: &bootstrapv1.Logger_Sentry{Dsn: "https://k@o.ingest.sentry.io/1"}},
		{Type: "aliyun", Aliyun: &bootstrapv1.Logger_Aliyun{Endpoint: "cn-hangzhou.log.aliyuncs.com", Project: "proj", Logstore: "store"}},
		{Type: "tencent", Tencent: &bootstrapv1.Logger_Tencent{Endpoint: "cls.ap-guangzhou.myqcloud.com", TopicId: "topic-1"}},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("五后端合法配置（凭证走 env 链）应通过: %v", err)
	}

	cases := []struct {
		name string
		b    *bootstrapv1.Logger_Backend
		want string
	}{
		{
			"charm level 越域（fatal 会静默落 info）",
			&bootstrapv1.Logger_Backend{Type: "charm", Charm: &bootstrapv1.Logger_Charm{Level: "fatal"}},
			"backends[0].charm.level",
		},
		{
			"charm format 越域（console 会静默落 text）",
			&bootstrapv1.Logger_Backend{Type: "charm", Charm: &bootstrapv1.Logger_Charm{Format: "console"}},
			"backends[0].charm.format",
		},
		{
			"loki endpoint 漏配",
			&bootstrapv1.Logger_Backend{Type: "loki", Loki: &bootstrapv1.Logger_Loki{}},
			"backends[0].loki.endpoint",
		},
		{
			"loki batch_size 负数（会静默用默认）",
			&bootstrapv1.Logger_Backend{Type: "loki", Loki: &bootstrapv1.Logger_Loki{Endpoint: "http://loki:3100", BatchSize: -1}},
			"backends[0].loki.batch_size",
		},
		{
			"sentry dsn 漏配",
			&bootstrapv1.Logger_Backend{Type: "sentry", Sentry: &bootstrapv1.Logger_Sentry{}},
			"backends[0].sentry.dsn",
		},
		{
			"aliyun project 漏配（会静默发往占位默认）",
			&bootstrapv1.Logger_Backend{Type: "aliyun", Aliyun: &bootstrapv1.Logger_Aliyun{Endpoint: "cn-hangzhou.log.aliyuncs.com"}},
			"backends[0].aliyun.project",
		},
		{
			"tencent topic_id 漏配",
			&bootstrapv1.Logger_Backend{Type: "tencent", Tencent: &bootstrapv1.Logger_Tencent{Endpoint: "cls.ap-guangzhou.myqcloud.com"}},
			"backends[0].tencent.topic_id",
		},
	}
	for _, tc := range cases {
		cfg := NewBootstrap()
		cfg.GetLogger().Backends = []*bootstrapv1.Logger_Backend{tc.b}
		err := Validate(cfg)
		if err == nil {
			t.Fatalf("%s: 期望 fail-fast", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: 错误 %v 应含 %q 定位", tc.name, err, tc.want)
		}
	}
}
