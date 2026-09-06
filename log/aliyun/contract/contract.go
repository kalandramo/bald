// Package contract 把契约 bootstrapv1.Logger 的 aliyun 段映射为
// aliyun 后端的构造选项，供 bootstrap.LogRegistry 显式注册：
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
	aliyun "github.com/kalandramo/bald/log/aliyun"
)

// Type 是契约 logger.type 的注册名（小写约定）。
const Type = "aliyun"

// Provider 按契约 aliyun 段构造阿里云 SLS 日志后端。
// 返回的 cleanup 负责冲刷并关闭 producer。
func Provider(ctx context.Context, cfg *bootstrapv1.Logger) (log.Logger, func(), error) {
	c := cfg.GetAliyun()
	if c == nil {
		return nil, nil, fmt.Errorf("log/aliyun/contract: bootstrap.logger.aliyun is nil")
	}

	var opts []aliyun.Option
	if v := c.GetEndpoint(); v != "" {
		opts = append(opts, aliyun.WithEndpoint(v))
	}
	if v := c.GetProject(); v != "" {
		opts = append(opts, aliyun.WithProject(v))
	}
	if v := c.GetLogstore(); v != "" {
		opts = append(opts, aliyun.WithLogstore(v))
	}
	if v := c.GetAccessKey(); v != "" {
		opts = append(opts, aliyun.WithAccessKey(v))
	}
	if v := c.GetAccessSecret(); v != "" {
		opts = append(opts, aliyun.WithAccessSecret(v))
	}
	if v := c.GetSecurityToken(); v != "" {
		opts = append(opts, aliyun.WithSecurityToken(v))
	}

	l, err := aliyun.NewAliyunLogger(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("log/aliyun/contract: %w", err)
	}
	cleanup := func() { _ = l.Close() }
	return l, cleanup, nil
}
