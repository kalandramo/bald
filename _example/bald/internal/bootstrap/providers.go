package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/log/bslog"
	"github.com/kalandramo/bald/pkg/appkit"
)

// newLoggerProvider 覆盖 std 的默认 logger Provider：Attr 层脱敏 + 服务标识。
func newLoggerProvider() appkit.Option {
	return loggerProvider{deco: []bslog.Option{
		bslog.WithFilter(bslog.FilterKey("password")),
		bslog.WithFilter(bslog.FilterKey("token")),
		bslog.WithAttrs(slog.String("service.name", "bald-demo")),
	}}
}

// loggerProvider 消费 logger 段（段路径末者胜）。
type loggerProvider struct{ deco []bslog.Option }

func (p loggerProvider) Segments() []string { return []string{"logger"} }

func (p loggerProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

func (p loggerProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:        "logger",
		Kind:       "logger",
		Segments:   []string{"logger"},
		Phase:      appkit.PhaseBootstrap,
		Priority:   appkit.PriorityLogger,
		Reload:     appkit.ReloadReconcile,
		EffectName: "appkit:bootstrap-logger",
	}}, nil
}

func (p loggerProvider) Build(_ context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	l := s.Config.GetLogger()
	if l == nil {
		// 阶段 A（契约装载前）：引导 bslog，启动期日志仍带脱敏。
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
	return log.NewFilterLogger(out, l.GetFilterKeys()...),
		appkit.Cleanup(func(context.Context) error {
			for _, c := range cleanups {
				c()
			}
			return nil
		}), nil
}
