// Package contract 把契约后端声明项（bootstrapv1.Logger_Backend）的 loki 段
// 映射为 loki 后端的构造选项，供 bootstrap.LogRegistry 显式注册：
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

// Type 是契约后端项 type 的注册名（小写约定）。
const Type = "loki"

// Provider 按契约 loki 段构造 Grafana Loki 日志后端。
// 返回的 cleanup 冲刷剩余批量缓冲（Close 语义——loki 无后台定时推送，
// 缓冲未满的尾批必须经停机 Effect 链兑现）。
func Provider(ctx context.Context, b *bootstrapv1.Logger_Backend) (log.Logger, func(), error) {
	c := b.GetLoki()
	if c == nil {
		return nil, nil, fmt.Errorf("log/loki/contract: loki segment is nil")
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
