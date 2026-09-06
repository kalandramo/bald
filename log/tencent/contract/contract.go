// Package contract 把契约 bootstrapv1.Logger 的 tencent 段映射为
// tencent 后端的构造选项，供 bootstrap.LogRegistry 显式注册：
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
	tencent "github.com/kalandramo/bald/log/tencent"
)

// Type 是契约 logger.type 的注册名（小写约定）。
const Type = "tencent"

// Provider 按契约 tencent 段构造腾讯云 CLS 日志后端。
// 返回的 cleanup 负责冲刷并关闭 producer。
func Provider(ctx context.Context, cfg *bootstrapv1.Logger) (log.Logger, func(), error) {
	c := cfg.GetTencent()
	if c == nil {
		return nil, nil, fmt.Errorf("log/tencent/contract: bootstrap.logger.tencent is nil")
	}

	var opts []tencent.Option
	if v := c.GetEndpoint(); v != "" {
		opts = append(opts, tencent.WithEndpoint(v))
	}
	if v := c.GetTopicId(); v != "" {
		opts = append(opts, tencent.WithTopicID(v))
	}
	if v := c.GetAccessKey(); v != "" {
		opts = append(opts, tencent.WithAccessKey(v))
	}
	if v := c.GetAccessSecret(); v != "" {
		opts = append(opts, tencent.WithAccessSecret(v))
	}

	l, err := tencent.NewTencentLogger(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("log/tencent/contract: %w", err)
	}
	cleanup := func() { _ = l.Close() }
	return l, cleanup, nil
}
