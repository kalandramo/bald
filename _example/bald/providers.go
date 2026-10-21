// providers.go —— 应用自持语义的段 Provider（范本：bald-admin
// cmd/bald-admin/app/providers.go 的 loggerProvider）。
//
// 装饰归 provider 自持：本应用的
// loggerProvider 覆盖 std 默认的 StandardLoggerProvider（段路径末者胜——
// 声明序在 std.Standard() 之后），把 Attr 层脱敏（bslog.WithFilter）与固定
// 结构化属性（service.name）带进阶段 A/B 两个构造点。
package main

import (
	"context"
	"fmt"
	"log/slog"

	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/log/bslog"
	"github.com/kalandramo/bald/pkg/appkit"
)

// newLoggerProvider 返回带 Attr 层脱敏与应用标识的 logger Provider（消费
// logger 段；沿用内置 slog 分派语义：阶段 A 引导 bslog——启动期日志仍带
// 脱敏；阶段 B/热更按 backends 逐项直构 + 多项合并 + filter_keys 外包）。
//
// 两套脱敏机制互补（bald-admin 实测验证）：契约 logger.filter_keys 包在
// MultiLogger 外层，覆盖位置参数对（"password", v）与 ctx 属性流；
// bslog.WithFilter 是 slog handler 层装饰器，覆盖 slog.Attr 形态
// （slog.String("password", v)）——契约版不处理 Attr，会被漏过。二者缺一即
// 有来源未被掩码，故并存。
func newLoggerProvider() appkit.Option {
	return loggerProvider{deco: []bslog.Option{
		bslog.WithFilter(bslog.FilterKey("password")),
		bslog.WithFilter(bslog.FilterKey("token")),
		bslog.WithAttrs(slog.String("service.name", "bald-demo")),
	}}
}

// loggerProvider 消费 logger 段（覆盖 std 默认——段路径末者胜）。
type loggerProvider struct{ deco []bslog.Option }

func (p loggerProvider) Segments() []string { return []string{"logger"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (p loggerProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Build 构造日志后端（产物 log.Logger，引擎 loggerSource 协议消费；cleanup
// 交 curCleanup 原子换槽——文件/轮转句柄的释放与热更重建不泄漏）。
func (p loggerProvider) Build(_ context.Context, s appkit.BuildScope) (any, appkit.Cleanup, error) {
	l := s.Config.GetLogger()
	if l == nil {
		// 阶段 A（契约装载前）：引导 bslog——启动期日志仍带脱敏。
		return bslog.New(baldbootstrap.LogOptions(nil), p.deco...), nil, nil
	}
	bs := l.GetBackends()
	if len(bs) == 0 {
		return nil, nil, fmt.Errorf("logger: logger.backends is empty (declare at least one backend, e.g. type \"slog\")")
	}
	loggers := make([]log.Logger, 0, len(bs))
	var cleanups []func()
	for i, b := range bs {
		if b.GetType() != "slog" {
			return nil, nil, fmt.Errorf("logger: logger.backends[%d] type %q: builtin path supports only \"slog\"; remote/custom backends need a logger provider", i, b.GetType())
		}
		lg, c := bslog.NewWithCleanup(baldbootstrap.LogOptions(b), p.deco...)
		loggers = append(loggers, lg)
		cleanups = append(cleanups, c)
	}
	var out log.Logger = loggers[0]
	if len(loggers) > 1 {
		out = log.NewMultiLogger(loggers...)
	}
	// cleanup 按构造序正放释放各 slog 项的文件/轮转句柄（恒非 nil）。
	merged := func() {
		for _, c := range cleanups {
			c()
		}
	}
	return log.NewFilterLogger(out, l.GetFilterKeys()...),
		appkit.Cleanup(func(context.Context) error { merged(); return nil }), nil
}
