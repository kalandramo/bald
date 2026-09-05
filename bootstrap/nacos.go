package bootstrap

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconfig/nacos"
	"github.com/kalandramo/bald/bootstrap/config"
)

// NacosProvider 返回 nacos 配置源的初始化器。
//
// 它知道「契约里 Config.GetNacos() 返回什么字段」与「nacos.New 接受什么
// Option」，因此 bconfig/nacos 包无需 import bconf，保持源层零契约依赖。
//
// 层语义：nacos 源自带 ListenConfig 推送，层默认参与热更新（Watch=true）；
// client 自建模式（契约 server_addrs → 惰性建连，namespace 进 ClientConfig），
// nacos-sdk-go 无显式关闭接口，cleanup 为 nil。
// 契约 format 字段透传层 Format（配置内容格式声明）。
func NacosProvider() Provider {
	return func(_ context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error) {
		c := cfg.GetConfig().GetNacos()
		if c == nil {
			return nil, nil, nil // 未配置 nacos 源，跳过
		}
		var opts []nacos.Option
		if addrs := c.GetServerAddrs(); len(addrs) > 0 {
			opts = append(opts, nacos.WithServerAddrs(addrs...))
		}
		if ns := c.GetNamespace(); ns != "" {
			opts = append(opts, nacos.WithNamespace(ns))
		}
		if g := c.GetGroup(); g != "" {
			opts = append(opts, nacos.WithGroup(g))
		}
		if id := c.GetDataId(); id != "" {
			opts = append(opts, nacos.WithDataID(id))
		}
		src, err := nacos.New(opts...)
		if err != nil {
			return nil, nil, fmt.Errorf("bootstrap: build nacos source: %w", err)
		}
		return &config.Layer{
			Reader: src,
			Format: c.GetFormat(),
			Watch:  true,
		}, nil, nil
	}
}
