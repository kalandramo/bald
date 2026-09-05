package bootstrap

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconfig/consul"
	"github.com/kalandramo/bald/bootstrap/config"
)

// ConsulProvider 返回 consul 配置源的初始化器。
//
// 它知道「契约里 Config.GetConsul() 返回什么字段」与「consul.New 接受什么
// Option」，因此 bconfig/consul 包无需 import bconf，保持源层零契约依赖。
//
// 层语义：consul 源用 watch plan 推送变更，层默认参与热更新（Watch=true）；
// client 自建模式（契约 address/token/scheme），consul client 无需显式释放。
func ConsulProvider() Provider {
	return func(_ context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error) {
		c := cfg.GetConfig().GetConsul()
		if c == nil {
			return nil, nil, nil // 未配置 consul 源，跳过
		}
		var opts []consul.Option
		if a := c.GetAddress(); a != "" {
			opts = append(opts, consul.WithAddress(a))
		}
		if p := c.GetPath(); p != "" {
			opts = append(opts, consul.WithPath(p))
		}
		if t := c.GetToken(); t != "" {
			opts = append(opts, consul.WithToken(t))
		}
		if s := c.GetScheme(); s != "" {
			opts = append(opts, consul.WithScheme(s))
		}
		src, err := consul.New(opts...)
		if err != nil {
			return nil, nil, fmt.Errorf("bootstrap: build consul source: %w", err)
		}
		return &config.Layer{
			Reader: src,
			Watch:  true,
		}, nil, nil
	}
}
