package bootstrap

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconfig/apollo"
	"github.com/kalandramo/bald/bootstrap/config"
)

// ApolloProvider 返回 Apollo 配置源的初始化器。
//
// 它知道「契约里 Config.GetApollo() 返回什么字段」与「apollo.New 接受什么
// Option」，因此 bconfig/apollo 包无需 import bconf，保持源层零契约依赖。
//
// 层语义：Apollo 源自带原生变更推送，层默认参与热更新（Watch=true）；
// 契约 original_config=true 时层开启原始文档模式（结构化命名空间返回原文档，
// 整文档消费推荐）。连接失败立即报错（fail-fast，不 panic）。
// endpoint 优先，为空回退契约 ip 字段。
func ApolloProvider() Provider {
	return func(_ context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error) {
		c := cfg.GetConfig().GetApollo()
		if c == nil {
			return nil, nil, nil // 未配置 apollo 源，跳过
		}
		var opts []apollo.Option
		if id := c.GetAppId(); id != "" {
			opts = append(opts, apollo.WithAppID(id))
		}
		if cl := c.GetCluster(); cl != "" {
			opts = append(opts, apollo.WithCluster(cl))
		}
		if ns := c.GetNamespace(); ns != "" {
			opts = append(opts, apollo.WithNamespace(ns))
		}
		endpoint := c.GetEndpoint()
		if endpoint == "" {
			endpoint = c.GetIp()
		}
		if endpoint != "" {
			opts = append(opts, apollo.WithEndpoint(endpoint))
		}
		if s := c.GetSecret(); s != "" {
			opts = append(opts, apollo.WithSecret(s))
		}
		if c.GetIsBackupConfig() {
			opts = append(opts, apollo.WithEnableBackup())
			if p := c.GetBackupPath(); p != "" {
				opts = append(opts, apollo.WithBackupPath(p))
			}
		}
		if c.GetOriginalConfig() {
			opts = append(opts, apollo.WithOriginalConfig())
		}
		src, err := apollo.New(opts...)
		if err != nil {
			return nil, nil, fmt.Errorf("bootstrap: build apollo source: %w", err)
		}
		// agollo.Client 无显式关闭接口；Stop 需全局状态，此处交由进程生命周期管理。
		return &config.Layer{
			Reader: src,
			Watch:  true,
		}, nil, nil
	}
}
