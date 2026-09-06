// Package contract 提供 kubernetes 后端的契约装配：bootstrapv1.Registry 段 →
// kubernetes options 映射 + RegistrarRegistry Provider。
//
// 单独成包的原因：provider 根包保持零契约依赖（纯 SDK 实现），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
//
// 注意：kubernetes 路线依赖 in-cluster 环境（自建 clientset 走
// rest.InClusterConfig），集群外请用 NewWithClient 注入后走
// appkit.WithRegistrar 显式注入路径。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/pkg/registry"

	kubernetes "github.com/kalandramo/bald-registry-kubernetes"
)

// Type 是契约 registry.type 字段中 kubernetes 后端的取值。
const Type = "kubernetes"

// Provider 按契约 registry.kubernetes 段构造 kubernetes Registrar。
// 返回的 cleanup 由 appkit 停机 Effect 回放（Deregister 先于它，顺序安全）。
// 仅当 cfg.GetType() == Type 时被 RegistrarRegistry 调度到。
func Provider(ctx context.Context, cfg *bootstrapv1.Registry) (registry.Registrar, func(), error) {
	c := cfg.GetKubernetes()
	if c == nil {
		return nil, nil, fmt.Errorf("registry: type=%q but kubernetes section is missing", Type)
	}

	var opts []kubernetes.Option
	if ns := c.GetNamespace(); ns != "" {
		opts = append(opts, kubernetes.WithNamespace(ns))
	}

	r, err := kubernetes.New(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: build kubernetes registrar: %w", err)
	}
	return r, func() { r.Close() }, nil
}
