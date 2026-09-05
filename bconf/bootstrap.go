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
			Type: "slog",
			Slog: &bootstrapv1.Logger_Slog{
				Level:      "info",
				Format:     "console",
				OutputPath: "stdout",
			},
		},
	}
}

// Validate 校验顶层配置的取值合法性，供进程入口在装配前 fail-fast。
//
// 校验范围（缺省字段视为未启用，跳过）：
//   - app.id / app.name 非空；
//   - server.http.addr / server.grpc.addr 为合法 :port 或 ip:port；
//   - logger.type 非空；选 slog 时校验 level / format / output_path。
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

// validateLogger 校验日志契约段。当前仅 slog 后端已实现，
// 其余 type 先放行（装配层 bootstrap.LogRegistry 查表时才会 fail-fast），
// 避免契约层硬编码后端清单。
func validateLogger(l *bootstrapv1.Logger) error {
	if l == nil {
		return nil
	}
	if l.GetType() == "" {
		return fmt.Errorf("type must not be empty")
	}
	if s := l.GetSlog(); s != nil {
		switch s.GetLevel() {
		case "debug", "info", "warn", "error":
		default:
			return fmt.Errorf("slog.level %q, want debug|info|warn|error", s.GetLevel())
		}
		switch s.GetFormat() {
		case "console", "json":
		default:
			return fmt.Errorf("slog.format %q, want console|json", s.GetFormat())
		}
		if s.GetOutputPath() == "" {
			return fmt.Errorf("slog.output_path must not be empty")
		}
	}
	return nil
}

// 编译期断言：契约消息实现 proto.Message。
var (
	_ proto.Message = (*bootstrapv1.BootstrapConfig)(nil)
	_ proto.Message = (*bootstrapv1.App)(nil)
)
