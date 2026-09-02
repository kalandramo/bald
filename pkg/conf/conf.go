// Package conf 是 bald 框架级配置的 Protobuf 契约层。
//
// 职责边界（重要）：
//   - pkg/config 是「加载器」：负责 flag / env / 本地文件 / 远程的多源合并与热更新；
//   - pkg/conf   是「契约」：定义配置长什么样（api/proto/bald/config/v1/*.proto），
//     提供默认值、校验、命令行 flag 绑定（BindFlags）以及 server 层消费的解析辅助。
//
// 二者通过 config.Unmarshal(v, msg) 衔接：
//
//	cfg := conf.NewBootstrap()             // 1. 默认值
//	_ = config.Unmarshal(app.Viper(), cfg) // 2. viper 合并结果 → proto
//	_ = conf.Validate(cfg)                  // 3. 校验
//	// 4. server 层直接消费 cfg.GetHttp() 等 proto 消息
//
// 在全栈 proto 驱动的架构下，proto 是全仓唯一契约与类型真相源，pkg/options 已废弃，
// server 层直接消费 confv1.* proto 消息（见 P1 阶段 2）。
//
// 覆盖范围：仅框架级配置（app / http / grpc / log）。业务配置不受 proto 约束，
// 仍可继续使用 viper 的动态 key 能力。
package conf

import (
	"fmt"
	"os"

	confv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/config/v1"
	"github.com/kalandramo/bald/pkg/log"
)

// 框架级默认值来源说明：
//   - 标量字段默认值（http.addr/timeout、grpc.network/addr/timeout、log.level/format）
//     已由 protoc-gen-defaults 依据 api/proto/bald/config/v1/*.proto 的
//     (defaults.value) 注解生成 Defaulter.Default() 注入，单一事实源在 proto 内。
//   - 无法用静态注解表达的默认值在此保留：
//     app.id（运行时 hostname）、app.name/version（占位）、
//     log.output_paths（repeated，插件不支持注解注入）。
const (
	defaultLogOutputPath = "stdout"
	defaultAppName       = "bald-app"
	defaultAppVersion    = "v0.0.0"
)

// NewBootstrap 返回填好默认值的 Bootstrap 配置。
//
// 框架级标量默认值（http.addr / http.timeout / grpc.network / grpc.addr 等）
// 由 protoc-gen-defaults 依据 proto 字段的 (defaults.value) 注解生成
// 的 Defaulter.Default() 方法注入（见 api/proto/bald/config/v1/*.proto）。
// 无法用静态注解表达的默认值（如运行时 hostname、repeated 字段）在此 post-process。
//
// 必须先调本函数（而非 &confv1.Bootstrap{}）再交给 config.Unmarshal：
// proto3 没有字段级默认值表达，未出现在配置文件中的段会保持零值，
// 而框架的语义是「未配置 → 用默认值」。config.Unmarshal 只覆盖配置中
// 显式出现的字段，因此 Default() 注入的默认值得以保留。
func NewBootstrap() *confv1.Bootstrap {
	cfg := &confv1.Bootstrap{}
	cfg.Default() // 注入 proto 注解声明的字段级默认值（零值字段）
	// post-process：无法用静态注解表达的默认值
	cfg.App = &confv1.App{
		Name:    defaultAppName,
		Version: defaultAppVersion,
		Id:      hostname(),
	}
	if len(cfg.Logger.GetOutputPaths()) == 0 {
		cfg.Logger.OutputPaths = []string{defaultLogOutputPath}
	}
	return cfg
}

// Validate 校验 Bootstrap 配置，一次返回所有问题（而非首个即止）。
func Validate(cfg *confv1.Bootstrap) error {
	if cfg == nil {
		return fmt.Errorf("conf: bootstrap config is nil")
	}
	var errs []error
	errs = append(errs, validateHTTP(cfg.GetHttp())...)
	errs = append(errs, validateGRPC(cfg.GetGrpc())...)
	errs = append(errs, validateLogger(cfg.GetLogger())...)
	if err := LogOptions(cfg.GetLogger()).Validate(); err != nil {
		errs = append(errs, err)
	}
	return joinErrors(errs)
}

// --- 各段校验 ---

func validateHTTP(http *confv1.Http) []error {
	if http == nil {
		return nil
	}
	var errs []error
	if http.GetAddr() != "" {
		if err := validateAddress(http.GetAddr()); err != nil {
			errs = append(errs, fmt.Errorf("http.addr: %w", err))
		}
	}
	if tls := http.GetTls(); tls.GetEnabled() {
		// Cert 与 Key 必须成对提供；仅配 CA 是合法的（如客户端 mTLS
		// 校验对端证书、或纯 CA 信任链）。错误仅发生在 cert/key 只配其一。
		hasCert := tls.GetCert() != ""
		hasKey := tls.GetKey() != ""
		if hasCert != hasKey {
			errs = append(errs, fmt.Errorf("http.tls: cert and key must be provided together (got cert=%v key=%v)", hasCert, hasKey))
		}
	}
	return errs
}

func validateGRPC(grpc *confv1.Grpc) []error {
	if grpc == nil {
		return nil
	}
	var errs []error
	if grpc.GetAddr() != "" {
		if err := validateAddress(grpc.GetAddr()); err != nil {
			errs = append(errs, fmt.Errorf("grpc.addr: %w", err))
		}
	}
	if n := grpc.GetNetwork(); n != "" && n != "tcp" && n != "unix" {
		errs = append(errs, fmt.Errorf("grpc.network: %q is not supported, want tcp or unix", n))
	}
	return errs
}

func validateLogger(l *confv1.Logger) []error {
	if l == nil {
		return nil
	}
	var errs []error
	if level := l.GetLevel(); level != "" {
		switch level {
		case "debug", "info", "warn", "error":
		default:
			errs = append(errs, fmt.Errorf("log.level: invalid value %q, want debug|info|warn|error", level))
		}
	}
	if format := l.GetFormat(); format != "" {
		switch format {
		case "console", "json":
		default:
			errs = append(errs, fmt.Errorf("log.format: invalid value %q, want console|json", format))
		}
	}
	if len(l.GetOutputPaths()) == 0 {
		errs = append(errs, fmt.Errorf("log.output-paths: must not be empty"))
	}
	return errs
}

// LogOptions 把 proto 的 Logger 配置转为 pkg/log.Options（供日志系统重建使用）。
func LogOptions(l *confv1.Logger) *log.Options {
	o := log.NewOptions()
	if l == nil {
		return o
	}
	if l.GetLevel() != "" {
		o.Level = l.GetLevel()
	}
	if l.GetFormat() != "" {
		o.Format = l.GetFormat()
	}
	if len(l.GetOutputPaths()) > 0 {
		o.OutputPaths = l.GetOutputPaths()
	}
	if r := l.GetRotate(); r != nil {
		o.Rotate.Enabled = r.GetEnabled()
		// proto3 标量字段无 presence：配置未显式写 max_size/max_backups/max_age 时
		// 为零值，此处回退到 log.NewOptions() 的默认，避免触发 Rotate.MaxSize<=0 校验失败。
		if r.GetMaxSize() > 0 {
			o.Rotate.MaxSize = int(r.GetMaxSize())
		}
		if r.GetMaxBackups() > 0 {
			o.Rotate.MaxBackups = int(r.GetMaxBackups())
		}
		if r.GetMaxAge() > 0 {
			o.Rotate.MaxAge = int(r.GetMaxAge())
		}
		o.Rotate.Compress = r.GetCompress()
	}
	return o
}

// --- 工具 ---

// hostname 返回主机名，失败时返回 "unknown"。
func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// joinErrors 把多个错误合并为一个，保持与 options.Validate() 一致的可读格式。
func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	msg := "conf: invalid bootstrap config:"
	for _, e := range errs {
		msg += "\n  - " + e.Error()
	}
	return fmt.Errorf("%s", msg)
}
