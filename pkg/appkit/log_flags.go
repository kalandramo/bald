package appkit

import (
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log/bslog"
	"github.com/spf13/pflag"
)

// logFlagNames 是本文件认识的 CLI 覆盖键（与 `bslog.Options.AddFlags` 注册的同名）。
// 覆盖只在 flag **真正出现过**（Changed）时发生：未出现的键一律不动契约，
// 否则 flag 的默认值会静默压过配置文件里的取值。
const (
	flagLogLevel          = "log.level"
	flagLogFormat         = "log.format"
	flagLogOutputPaths    = "log.output-paths"
	flagLogRotateEnabled  = "log.rotate.enabled"
	flagLogRotateMaxSize  = "log.rotate.max-size"
	flagLogRotateMaxBacks = "log.rotate.max-backups"
	flagLogRotateMaxAge   = "log.rotate.max-age"
	flagLogRotateCompress = "log.rotate.compress"
)

// LogFlagNames 派生本文件认识的 CLI 键名——直接取自 `bslog.Options.AddFlags` 的
// 真实注册面，不是手抄清单：bslog 新增一个 `log.*` 旋钮时这里自动跟着变，
// 装配层据此把这些键从合并树摘掉（§7.39），不会出现「注册了但没摘 ⇒ 严格契约判未知键」。
// 与下方逐个读取的常量的一致性由 TestLogFlagNamesMatchAppliedKeys 钉住。
func LogFlagNames() []string {
	fs := pflag.NewFlagSet("bald-log-flags", pflag.ContinueOnError)
	bslog.NewOptions().AddFlags(fs)
	var names []string
	fs.VisitAll(func(f *pflag.Flag) { names = append(names, f.Name) })
	return names
}

// ApplyLogFlagOverrides 把命令行上出现过的 `--log.*` 覆盖写进契约的 logger 段。
//
// 为什么走契约而不是配置树：`--log.*` 的键名（顶层 `log`）在契约里不存在——
// 日志旋钮的形状是 `repeated logger.backends[i].slog.*`，没有任何等价的标量键路径。
// 把它并进合并树会被严格契约校验判成未知键（这就是 §7.39 那个「一用就终止启动」的
// 缺陷本体），所以 flag 值不进树，改在**校验之后、计划编译之前**落到契约对象上：
// 契约仍是运行期唯一的取值载体（《设计判据》J9）——紧随其后的 compilePlan 得到含
// 终值的期望态摘要（reconcile/drift 的基线），Logger 重建与热更新协调读的都是它。
// 边界：`--plan` 在任何副作用之前打印且只投影段名/消费者，实测带不带 `--log.level`
// 输出逐行相同，所以 CLI 覆盖不出现在 `--plan` 里——那是 `--plan` 的既有语义（它在
// 契约装载前就退出），不是本覆盖引入的缺口。
//
// 作用范围：只覆盖 `type == "slog"` 的后端（CLI 是全局开关，不区分实例）。
// 契约里整段 `logger` 缺失时按 flag 值物化一个 slog 后端——那条分支本来就是
// 引导期默认 slog（见 `bootstrap.LogOptions(nil)`），flag 是它唯一的取值来源，静默丢弃会
// 命中 J7 那类「配了以为生效」。`logger` 段存在但没有 slog 后端时**跳过**：
// 追加后端会改变日志出口数量，那不是「覆盖一个值」而是「换一种部署」。
//
// 为什么住在本包而不是 `bootstrap`：这段逻辑只依赖 `bconf` 类型、`bslog` 注册器与
// `pflag`，全是 appkit 装配链已有的取值，而它的两个消费者（`Bind` 的摘出过滤、
// `syncBootstrap`/`hotReload` 的覆盖调用）都在本包。搬进 `bootstrap` 要给 bootstrap
// 加两个新导出符号，而 13 个适配器 module 是「本地 `pkg` 源码 + 已发布 `bootstrap`
// 产物」编译的（它们只 `replace` 了 pkg/bconf，没有 replace bootstrap）——新符号在
// 那些 module 里当场编译失败（本轮实测：`metrics/datadog` 等 13 个红在
// `undefined: baldbootstrap.ApplyLogFlagOverrides`）。不加对外 API 既避开这一类漂移，
// 也保留那些 module 作为「pkg 用了兄弟模块未发布 API」的探测器。
func ApplyLogFlagOverrides(cfg *bootstrapv1.BootstrapConfig, fs *pflag.FlagSet) {
	if cfg == nil || fs == nil || !hasAnyLogFlag(fs) {
		return
	}
	l := cfg.Logger
	if l == nil {
		l = &bootstrapv1.Logger{Backends: []*bootstrapv1.Logger_Backend{{Type: "slog"}}}
		cfg.Logger = l
	}
	for _, b := range l.GetBackends() {
		if b.GetType() != "slog" {
			continue
		}
		if b.Slog == nil {
			b.Slog = &bootstrapv1.Logger_Slog{}
		}
		applyToSlog(b.Slog, fs)
	}
}

// applyToSlog 逐键覆盖一个 slog 后端声明；只动 Changed 的键。
func applyToSlog(s *bootstrapv1.Logger_Slog, fs *pflag.FlagSet) {
	if fs.Changed(flagLogLevel) {
		if v, err := fs.GetString(flagLogLevel); err == nil {
			s.Level = v
		}
	}
	if fs.Changed(flagLogFormat) {
		if v, err := fs.GetString(flagLogFormat); err == nil {
			s.Format = v
		}
	}
	if fs.Changed(flagLogOutputPaths) {
		if v, err := fs.GetStringSlice(flagLogOutputPaths); err == nil && len(v) > 0 {
			s.OutputPaths = v
		}
	}
	if !anyChanged(fs, flagLogRotateEnabled, flagLogRotateMaxSize, flagLogRotateMaxBacks,
		flagLogRotateMaxAge, flagLogRotateCompress) {
		return
	}
	if s.Rotate == nil {
		s.Rotate = &bootstrapv1.Logger_Slog_Rotate{}
	}
	r := s.Rotate
	if fs.Changed(flagLogRotateEnabled) {
		if v, err := fs.GetBool(flagLogRotateEnabled); err == nil {
			r.Enabled = v
		}
	}
	if fs.Changed(flagLogRotateMaxSize) {
		if v, err := fs.GetInt(flagLogRotateMaxSize); err == nil {
			r.MaxSize = int32(v)
		}
	}
	if fs.Changed(flagLogRotateMaxBacks) {
		if v, err := fs.GetInt(flagLogRotateMaxBacks); err == nil {
			r.MaxBackups = int32(v)
		}
	}
	if fs.Changed(flagLogRotateMaxAge) {
		if v, err := fs.GetInt(flagLogRotateMaxAge); err == nil {
			r.MaxAge = int32(v)
		}
	}
	if fs.Changed(flagLogRotateCompress) {
		if v, err := fs.GetBool(flagLogRotateCompress); err == nil {
			r.Compress = v
		}
	}
}

func anyChanged(fs *pflag.FlagSet, names ...string) bool {
	for _, n := range names {
		if fs.Changed(n) {
			return true
		}
	}
	return false
}

// hasAnyLogFlag 判断命令行是否出现过任一 `--log.*`；一个都没有时整段覆盖是 no-op，
// 因此不必物化 logger 段。未注册的 log.* 键（业务自带同名 flag）不算。
func hasAnyLogFlag(fs *pflag.FlagSet) bool {
	return anyChanged(fs, flagLogLevel, flagLogFormat, flagLogOutputPaths,
		flagLogRotateEnabled, flagLogRotateMaxSize, flagLogRotateMaxBacks,
		flagLogRotateMaxAge, flagLogRotateCompress)
}
