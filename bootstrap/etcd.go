package bootstrap

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconfig/etcd"
	"github.com/kalandramo/bald/bootstrap/config"
)

// EtcdProvider 返回 etcd 配置源的初始化器。
//
// 它知道「契约里 Config.GetEtcd() 返回什么字段」与「etcd.New 接受什么 Option」，
// 因此 bconfig/etcd 包无需 import bconf，保持源层零契约依赖。
//
// 层语义：etcd 源自带原生 watch 推送，层默认参与热更新（Watch=true）；
// client 自建模式（契约 endpoints → 惰性建连），资源归层 cleanup 释放。
func EtcdProvider() Provider {
	return func(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error) {
		c := cfg.GetConfig().GetEtcd()
		if c == nil {
			return nil, nil, nil // 未配置 etcd 源，跳过
		}
		var opts []etcd.Option
		if ctx != nil {
			opts = append(opts, etcd.WithContext(ctx))
		}
		if addrs := c.GetEndpoints(); len(addrs) > 0 {
			opts = append(opts, etcd.WithEndpoints(addrs...))
		}
		if p := c.GetPath(); p != "" {
			opts = append(opts, etcd.WithPath(p))
		}
		if u := c.GetUsername(); u != "" {
			opts = append(opts, etcd.WithUsername(u))
		}
		if p := c.GetPassword(); p != "" {
			opts = append(opts, etcd.WithPassword(p))
		}
		if c.GetPrefix() {
			opts = append(opts, etcd.WithPrefix(true))
		}
		src, err := etcd.New(opts...)
		if err != nil {
			return nil, nil, fmt.Errorf("bootstrap: build etcd source: %w", err)
		}
		return &config.Layer{
			Reader: src,
			Watch:  true,
		}, func() { _ = src.Close() }, nil
	}
}
