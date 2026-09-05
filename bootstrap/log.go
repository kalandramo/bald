package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
	slogadapter "github.com/kalandramo/bald/log/slog"
)

// LoggerProvider 是日志后端工厂：从契约的 Logger 配置构造一个 log.Logger。
//
// 返回值语义：
//   - 出错返回 error，BuildLogger 短路并包装错误；
//   - 返回 nil Logger 视为该 provider 无法处理此配置，BuildLogger 报错
//     （日志是必需品，与配置源「nil=跳过」的级联语义不同）；
//   - cleanup 释放后端资源（Sync/关连接等），可为 nil。
//
// provider 按 cfg.Type 字符串查表调用；注册名约定为小写（"slog"/"zap"/...）。
// 与配置源 Registry 的差异：日志是单选（type 指定一种后端），无级联序；
// 将来契约扩展多输出源（本地 + 远程并存）后，可在此层循环产出并用
// log.NewMultiLogger 合并——注册表形状已为此就绪。
type LoggerProvider func(ctx context.Context, cfg *bootstrapv1.Logger) (log.Logger, func(), error)

// LogRegistry 按名字注册日志后端工厂（显式注册，无 init() 副作用）。
type LogRegistry struct {
	mu        sync.RWMutex
	providers map[string]LoggerProvider
}

// NewLogRegistry 创建一个空的 [LogRegistry]。
func NewLogRegistry() *LogRegistry {
	return &LogRegistry{providers: make(map[string]LoggerProvider)}
}

// Register 注册一个日志后端工厂。重名报错、不覆盖（fail-fast）。
func (r *LogRegistry) Register(name string, p LoggerProvider) error {
	if name == "" {
		return fmt.Errorf("bootstrap: log provider name is empty")
	}
	if p == nil {
		return fmt.Errorf("bootstrap: log provider %q is nil", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[name]; ok {
		return fmt.Errorf("bootstrap: log provider %q already registered", name)
	}
	r.providers[name] = p
	return nil
}

// MustRegister 是 [LogRegistry.Register] 的 panic 版本，仅用于主程序 main() 内显式注册。
func (r *LogRegistry) MustRegister(name string, p LoggerProvider) {
	if err := r.Register(name, p); err != nil {
		panic(err)
	}
}

// BuildLogger 按契约的 Logger.Type 查表构造日志后端。
//
// 返回 (Logger, cleanup, error)。cfg 为 nil 或 type 为空视为配置错误（fail-fast，
// 与配置源 Build 的语义一致）；type 未注册时报错并列出可用项。
//
// 由 main 显式调用并经 log.SetLogger 注入全局表：
//
//	lr := bootstrap.NewLogRegistry()
//	lr.MustRegister("slog", bootstrap.SlogLoggerProvider())
//	logger, cleanup, err := lr.BuildLogger(ctx, cfg.GetLogger())
//	log.SetLogger(logger)
//	defer cleanup()
func (r *LogRegistry) BuildLogger(ctx context.Context, cfg *bootstrapv1.Logger) (log.Logger, func(), error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("bootstrap: logger config is nil")
	}
	name := cfg.GetType()
	if name == "" {
		return nil, nil, fmt.Errorf("bootstrap: logger type is empty")
	}

	r.mu.RLock()
	p, ok := r.providers[name]
	r.mu.RUnlock()
	if !ok {
		return nil, nil, fmt.Errorf("bootstrap: log provider %q not registered (registered: %v)", name, r.names())
	}

	l, cleanup, err := p(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: log provider %s: %w", name, err)
	}
	if l == nil {
		return nil, nil, fmt.Errorf("bootstrap: log provider %s produced nil logger", name)
	}
	// cleanup 契约为「永不为 nil」：调用方可直接 defer cleanup()。
	if cleanup == nil {
		cleanup = func() {}
	}
	return l, cleanup, nil
}

// names 返回已注册 provider 名的排序快照（错误信息用）。
func (r *LogRegistry) names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for k := range r.providers {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// SlogLoggerProvider 返回标准库 slog 后端的工厂（契约 Type="slog"）。
//
// 它知道「契约里 Logger.GetSlog() 返回什么字段」与「slogadapter.NewOptions 的形状」，
// 因此 slog 包无需 import bconf，保持适配器层零契约依赖。
//
// 映射规则：level/format/output_path 逐字段透传；契约 output_path 为单值，
// 契约装配暂不含轮转（Options 的 Rotate/多路径仅 CLI/Options 路径可用）——
// 差异已记录于设计文档 §3。Slog 段缺失时回退 Options 默认值（stdout + info）。
func SlogLoggerProvider() LoggerProvider {
	return func(_ context.Context, cfg *bootstrapv1.Logger) (log.Logger, func(), error) {
		return slogadapter.NewSlogLogger(LogOptions(cfg)), nil, nil
	}
}

// LogOptions 把契约的 Logger 配置转为 slogadapter.Options。
//
// 原属 pkg/conf（LogOptions，confv1 版），legacy 契约退役后迁入装配层：
// 这里已同时依赖 bconf（契约类型）与 log/slog（Options 形状），放此处零新增依赖。
// Slog 段缺失时回退 Options 默认值（stdout + info）。
func LogOptions(l *bootstrapv1.Logger) *slogadapter.Options {
	c := l.GetSlog()
	if c == nil {
		// type=slog 但未携带 Slog 段：使用默认配置，保证日志开箱即用。
		return slogadapter.NewOptions()
	}
	o := slogadapter.NewOptions()
	if c.GetLevel() != "" {
		o.Level = c.GetLevel()
	}
	if c.GetFormat() != "" {
		o.Format = c.GetFormat()
	}
	if p := c.GetOutputPath(); p != "" {
		o.OutputPaths = []string{p}
	}
	return o
}
