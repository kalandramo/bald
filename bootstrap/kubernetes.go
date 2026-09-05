package bootstrap

import (
	"context"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconfig/kubernetes"
	"github.com/kalandramo/bald/bootstrap/config"
)

// KubernetesProvider 返回 Kubernetes ConfigMap 配置源的初始化器。
//
// 它知道「契约里 Config.GetKubernetes() 返回什么字段」与「kubernetes.New
// 接受什么 Option」，因此 bconfig/kubernetes 包无需 import bconf，保持源层
// 零契约依赖。
//
// 层语义：ConfigMap watch 推送变更，层默认参与热更新（Watch=true）；
// 惰性建连（in-cluster 或契约 kube_config/master），clientset 无显式关闭
// 接口，cleanup 为 nil。契约 config_map_name/key 经 WithConfigMapName/
// WithDataKey 成为空 key 装配的默认值（层 Load(ctx, "") 直接命中）。
func KubernetesProvider() Provider {
	return func(_ context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error) {
		c := cfg.GetConfig().GetKubernetes()
		if c == nil {
			return nil, nil, nil // 未配置 kubernetes 源，跳过
		}
		var opts []kubernetes.Option
		if ns := c.GetNamespace(); ns != "" {
			opts = append(opts, kubernetes.WithNamespace(ns))
		}
		if name := c.GetConfigMapName(); name != "" {
			opts = append(opts, kubernetes.WithConfigMapName(name))
		}
		if k := c.GetKey(); k != "" {
			opts = append(opts, kubernetes.WithDataKey(k))
		}
		if l := c.GetLabelSelector(); l != "" {
			opts = append(opts, kubernetes.WithLabelSelector(l))
		}
		if f := c.GetFieldSelector(); f != "" {
			opts = append(opts, kubernetes.WithFieldSelector(f))
		}
		if kc := c.GetKubeConfig(); kc != "" {
			opts = append(opts, kubernetes.WithKubeConfig(kc))
		}
		if m := c.GetMaster(); m != "" {
			opts = append(opts, kubernetes.WithMaster(m))
		}
		return &config.Layer{
			Reader: kubernetes.New(opts...),
			Watch:  true,
		}, nil, nil
	}
}
