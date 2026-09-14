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
// 默认值：
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
// 配置源（bootstrap/config Layer 合并树、bconfig KV、测试桩）里未出现的字段
// 保留这里的默认值。
// 例外是 repeated 字段：配置中出现的列表整体替换默认列表、新元素不继承
// 默认项的任何字段（见 UnmarshalMap 的 clearPresentLists）——配置了
// logger.backends 就须写全该项，缺输出目标会被 Validate fail-fast。
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
//   - logger.backends 至少一项；逐项校验 type 非空与六种后端段的段级规则
//     （值域、必填连接目标、数值非负，详见 validateLogger）。
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

// validateLogger 校验日志契约段。段级规则只到形状可判定层（值域、必填
// 连接目标、数值非负）；后端是否已注册由装配层 bootstrap.LogRegistry
// 查表 fail-fast——契约层不硬编码后端清单。
//
// filter_keys 与 backends 两个 repeated 字段逐项校验：前者先做（脱敏对
// 全部后端生效），项非空；后者校验 type 非空与六种后端段的段级规则
//（slog/charm/loki/sentry/aliyun/tencent，见各自的 validate*Section）。
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
		prefix := fmt.Sprintf("backends[%d]", i)
		if s := b.GetSlog(); s != nil {
			if err := validateSlogSection(s, prefix+".slog"); err != nil {
				return err
			}
		}
		if s := b.GetCharm(); s != nil {
			if err := validateCharmSection(s, prefix+".charm"); err != nil {
				return err
			}
		}
		if s := b.GetLoki(); s != nil {
			if err := validateLokiSection(s, prefix+".loki"); err != nil {
				return err
			}
		}
		if s := b.GetSentry(); s != nil {
			if err := validateSentrySection(s, prefix+".sentry"); err != nil {
				return err
			}
		}
		if s := b.GetAliyun(); s != nil {
			if err := validateAliyunSection(s, prefix+".aliyun"); err != nil {
				return err
			}
		}
		if s := b.GetTencent(); s != nil {
			if err := validateTencentSection(s, prefix+".tencent"); err != nil {
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

// validateCharmSection 校验后端声明项的 charm 段规则：level/format 值域
//（空串放行，装配层回退默认，与 slog 段一致）。
//
// 值域校验的必要性：charm contract 对未知值静默回退默认——level "fatal"
// 落 info、format "console" 落 text，错配无任何报错；契约层把静默错配变
// 启动报错。output_path 空串放行：contract 明确空值与 "stderr" 等价
//（终端美化日志走 stderr 是 charmbracelet 惯例），不是漏配。
func validateCharmSection(s *bootstrapv1.Logger_Charm, prefix string) error {
	if v := s.GetLevel(); v != "" {
		switch v {
		case "debug", "info", "warn", "error":
		default:
			return fmt.Errorf("%s.level %q, want debug|info|warn|error", prefix, v)
		}
	}
	if v := s.GetFormat(); v != "" {
		switch v {
		case "json", "text":
		default:
			return fmt.Errorf("%s.format %q, want json|text", prefix, v)
		}
	}
	return nil
}

// validateLokiSection 校验后端声明项的 loki 段规则：endpoint 必填（contract
// 同款 required 前移到契约层，错误更早且带段级定位）；batch_size /
// flush_interval 非负（contract 仅 >0 生效，负数会静默用默认——配置错误
// 应显式暴露）。
func validateLokiSection(s *bootstrapv1.Logger_Loki, prefix string) error {
	if s.GetEndpoint() == "" {
		return fmt.Errorf("%s.endpoint must not be empty", prefix)
	}
	if s.GetBatchSize() < 0 {
		return fmt.Errorf("%s.batch_size must be >= 0", prefix)
	}
	if s.GetFlushInterval() < 0 {
		return fmt.Errorf("%s.flush_interval must be >= 0", prefix)
	}
	return nil
}

// validateSentrySection 校验后端声明项的 sentry 段规则：dsn 必填（contract
// 同款 required 前移）。dsn 格式不校——Sentry SDK 构造期的解析错误更专业，
// 留在装配层。
func validateSentrySection(s *bootstrapv1.Logger_Sentry, prefix string) error {
	if s.GetDsn() == "" {
		return fmt.Errorf("%s.dsn must not be empty", prefix)
	}
	return nil
}

// validateAliyunSection 校验后端声明项的 aliyun 段规则：连接目标必填，
// 凭证不校验。
//
// endpoint/project/logstore 空值会静默发往 options 层占位默认
// （"projectName"/"app"），且 SendLog 的错误被丢弃——静默丢日志，必须在
// 启动期挡下。access_key/access_secret/security_token 不校验非空：SDK
// 环境变量凭证链（ALIBABA_CLOUD_ACCESS_KEY_ID 等）是合法配置路径。
func validateAliyunSection(s *bootstrapv1.Logger_Aliyun, prefix string) error {
	if s.GetEndpoint() == "" {
		return fmt.Errorf("%s.endpoint must not be empty", prefix)
	}
	if s.GetProject() == "" {
		return fmt.Errorf("%s.project must not be empty", prefix)
	}
	if s.GetLogstore() == "" {
		return fmt.Errorf("%s.logstore must not be empty", prefix)
	}
	return nil
}

// validateTencentSection 校验后端声明项的 tencent 段规则：endpoint /
// topic_id 必填——无默认，空值 deferred 到 send 期且错误同样被丢弃；
// 凭证不校验（同 aliyun，SDK 环境变量凭证链是合法路径）。
func validateTencentSection(s *bootstrapv1.Logger_Tencent, prefix string) error {
	if s.GetEndpoint() == "" {
		return fmt.Errorf("%s.endpoint must not be empty", prefix)
	}
	if s.GetTopicId() == "" {
		return fmt.Errorf("%s.topic_id must not be empty", prefix)
	}
	return nil
}

// 编译期断言：契约消息实现 proto.Message。
var (
	_ proto.Message = (*bootstrapv1.BootstrapConfig)(nil)
	_ proto.Message = (*bootstrapv1.App)(nil)
)
