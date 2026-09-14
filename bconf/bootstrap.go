package bconf

import (
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// NewBootstrap 返回带默认值的顶层配置契约（bootstrapv1.BootstrapConfig）。
//
// 默认值与 onexstack/pkg/app 的 AppInfo 风格对齐：
//   - App.Id 取主机名（实例唯一标识），Name/Version 给占位默认；
//   - Server.Http ":8080"、Server.Grpc ":9090"；
//   - Logger 选 slog 后端（console / stdout / info）。
//
// 用法：
//
//	cfg := bconf.NewBootstrap()
//	bconf.UnmarshalMap(settings, cfg) // 配置覆盖默认值（合并语义）
//	bconf.Validate(cfg)               // 启动前校验
//
// 配置源（viper 树、bconfig KV、测试桩）里未出现的字段保留这里的默认值。
func NewBootstrap() *bootstrapv1.BootstrapConfig {
	return &bootstrapv1.BootstrapConfig{
		App: &bootstrapv1.App{
			Id:          hostname(),
			Name:        "bald-app",
			Version:     "v0.0.0",
			StopTimeout: durationpb.New(30 * time.Second),
		},
		Server: &bootstrapv1.Server{
			Http: &bootstrapv1.Server_Http{Addr: ":8080"},
			Grpc: &bootstrapv1.Server_Grpc{Addr: ":9090"},
		},
		Logger: &bootstrapv1.Logger{
			Backends: []*bootstrapv1.Logger_Backend{{
				Type: "slog",
				Slog: &bootstrapv1.Logger_Slog{
					Level:      "info",
					Format:     "console",
					OutputPath: "stdout",
				},
			}},
		},
	}
}

// Validate 校验顶层配置的取值合法性，供进程入口在装配前 fail-fast。
//
// 校验范围（缺省字段视为未启用，跳过）：
//   - app.id / app.name 非空；
//   - server.http.addr / server.grpc.addr 为合法 :port 或 ip:port；
//   - logger.backends 至少一项；逐项校验 type 非空与 slog 项的段级规则
//     （level / format / 输出目标 output_paths 优先，回退 output_path，
//     与 rotate 数值非负）。
func Validate(cfg *bootstrapv1.BootstrapConfig) error {
	if cfg == nil {
		return fmt.Errorf("bconf: bootstrap config is nil")
	}
	if err := validateApp(cfg.GetApp()); err != nil {
		return fmt.Errorf("app: %w", err)
	}
	if s := cfg.GetServer(); s != nil {
		if h := s.GetHttp(); h != nil {
			if err := validateAddress(h.GetAddr()); err != nil {
				return fmt.Errorf("server.http.addr: %w", err)
			}
		}
		if g := s.GetGrpc(); g != nil {
			if err := validateAddress(g.GetAddr()); err != nil {
				return fmt.Errorf("server.grpc.addr: %w", err)
			}
		}
	}
	if err := validateLogger(cfg.GetLogger()); err != nil {
		return fmt.Errorf("logger: %w", err)
	}
	return nil
}

func validateApp(a *bootstrapv1.App) error {
	if a == nil {
		return nil
	}
	if a.GetId() == "" {
		return fmt.Errorf("id must not be empty")
	}
	if a.GetName() == "" {
		return fmt.Errorf("name must not be empty")
	}
	return nil
}

// validateLogger 校验日志契约段。当前仅 slog 后端已实现深校验，
// 其余 type 先放行（装配层 bootstrap.LogRegistry 查表时才会 fail-fast），
// 避免契约层硬编码后端清单。
//
// 契约只有 backends 一个多值配置项：逐项校验 type 非空与 slog 项的段级
// 规则。filter_keys 全局校验在逐项之前（脱敏对全部后端生效）。
func validateLogger(l *bootstrapv1.Logger) error {
	if l == nil {
		return nil
	}
	// 全局脱敏清单：空串项 fail-fast（对齐 output_paths 先例）——空 key
	// 永不命中任何属性，配置错误应显式暴露而非静默吞掉。
	for i, k := range l.GetFilterKeys() {
		if k == "" {
			return fmt.Errorf("filter_keys[%d] must not be empty", i)
		}
	}
	bs := l.GetBackends()
	if len(bs) == 0 {
		return fmt.Errorf("backends must not be empty")
	}
	for i, b := range bs {
		if b.GetType() == "" {
			return fmt.Errorf("backends[%d].type must not be empty", i)
		}
		if s := b.GetSlog(); s != nil {
			if err := validateSlogSection(s, fmt.Sprintf("backends[%d].slog", i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateSlogSection 校验后端声明项的 slog 段规则。
// prefix 用于错误定位（"backends[i].slog"）。
//
// level/format 空串放行（装配层 LogOptions 对空值回退默认 info/console——
// 多后端项是 repeated 新元素，不继承 NewBootstrap 默认值，字段缺省是
// 常态）；非空才校验取值。与 Validate 总则「缺省字段视为未启用，跳过」一致。
func validateSlogSection(s *bootstrapv1.Logger_Slog, prefix string) error {
	if v := s.GetLevel(); v != "" {
		switch v {
		case "debug", "info", "warn", "error":
		default:
			return fmt.Errorf("%s.level %q, want debug|info|warn|error", prefix, v)
		}
	}
	if v := s.GetFormat(); v != "" {
		switch v {
		case "console", "json":
		default:
			return fmt.Errorf("%s.format %q, want console|json", prefix, v)
		}
	}
	// 输出目标：output_paths 非空时逐项非空；为空时回退要求 output_path
	//（都空时装配层会默认 stdout，但显式给了 Slog 段却不给输出目标，
	// 更可能是漏配——fail-fast 暴露）。
	if ps := s.GetOutputPaths(); len(ps) > 0 {
		for i, p := range ps {
			if p == "" {
				return fmt.Errorf("%s.output_paths[%d] must not be empty", prefix, i)
			}
		}
	} else if s.GetOutputPath() == "" {
		return fmt.Errorf("%s.output_path must not be empty", prefix)
	}
	if r := s.GetRotate(); r != nil {
		if r.GetMaxSize() < 0 {
			return fmt.Errorf("%s.rotate.max_size must be >= 0", prefix)
		}
		if r.GetMaxBackups() < 0 {
			return fmt.Errorf("%s.rotate.max_backups must be >= 0", prefix)
		}
		if r.GetMaxAge() < 0 {
			return fmt.Errorf("%s.rotate.max_age must be >= 0", prefix)
		}
	}
	return nil
}

// 编译期断言：契约消息实现 proto.Message。
var (
	_ proto.Message = (*bootstrapv1.BootstrapConfig)(nil)
	_ proto.Message = (*bootstrapv1.App)(nil)
)
