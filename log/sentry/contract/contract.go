// Package contract 把契约 bootstrapv1.Logger 的 sentry 段映射为
// sentry 后端的构造选项，供 bootstrap.LogRegistry 显式注册：
//
//	reg := bootstrap.NewLogRegistry()
//	reg.MustRegister(contract.Type, contract.Provider)
//
// 本包是唯一 import 契约（bconf）的地方；根包（..）保持零契约依赖。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
	sentry "github.com/kalandramo/bald/log/sentry"
)

// Type 是契约 logger.type 的注册名（小写约定）。
const Type = "sentry"

// Provider 按契约 sentry 段构造 Sentry 错误追踪日志后端。
// 返回的 cleanup 冲刷事件队列（sentry.Flush）。
func Provider(ctx context.Context, cfg *bootstrapv1.Logger) (log.Logger, func(), error) {
	c := cfg.GetSentry()
	if c == nil {
		return nil, nil, fmt.Errorf("log/sentry/contract: bootstrap.logger.sentry is nil")
	}
	if c.GetDsn() == "" {
		return nil, nil, fmt.Errorf("log/sentry/contract: dsn is required")
	}

	var opts []sentry.Option
	opts = append(opts, sentry.WithDSN(c.GetDsn()))
	if v := c.GetEnvironment(); v != "" {
		opts = append(opts, sentry.WithEnvironment(v))
	}
	if v := c.GetRelease(); v != "" {
		opts = append(opts, sentry.WithRelease(v))
	}
	if v := c.GetServerName(); v != "" {
		opts = append(opts, sentry.WithServerName(v))
	}

	l, err := sentry.NewLogger(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("log/sentry/contract: %w", err)
	}
	cleanup := func() { _ = l.Close() }
	return l, cleanup, nil
}
