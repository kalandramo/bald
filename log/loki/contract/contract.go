// Package contract 把契约 bootstrapv1.Logger 的 loki 段映射为
// loki 后端的构造选项，供 bootstrap.LogRegistry 显式注册：
//
//	reg := bootstrap.NewLogRegistry()
//	reg.MustRegister(contract.Type, contract.Provider)
//
// 本包是唯一 import 契约（bconf）的地方；根包（..）保持零契约依赖。
package contract

import (
	"context"
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
	loki "github.com/kalandramo/bald/log/loki"
)

// Type 是契约 logger.type 的注册名（小写约定）。
const Type = "loki"

// Provider 按契约 loki 段构造 Grafana Loki 日志后端。
// 返回的 cleanup 负责冲刷批量缓冲并停止后台推送。
func Provider(ctx context.Context, cfg *bootstrapv1.Logger) (log.Logger, func(), error) {
	c := cfg.GetLoki()
	if c == nil {
		return nil, nil, fmt.Errorf("log/loki/contract: bootstrap.logger.loki is nil")
	}
	if c.GetEndpoint() == "" {
		return nil, nil, fmt.Errorf("log/loki/contract: endpoint is required")
	}

	var opts []loki.Option
	opts = append(opts, loki.WithEndpoint(c.GetEndpoint()))
	for k, v := range c.GetLabels() {
		opts = append(opts, loki.WithLabel(k, v))
	}
	if c.GetBatchSize() > 0 {
		opts = append(opts, loki.WithBatchSize(int(c.GetBatchSize())))
	}
	if c.GetFlushInterval() > 0 {
		opts = append(opts, loki.WithFlushInterval(time.Duration(c.GetFlushInterval())*time.Millisecond))
	}

	l, err := loki.NewLogger(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("log/loki/contract: %w", err)
	}
	cleanup := func() { _ = l.Close() }
	return l, cleanup, nil
}
