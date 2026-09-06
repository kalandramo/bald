// Package contract 提供 nacos 后端的契约装配：bootstrapv1.Registry 段 →
// nacos options 映射 + RegistrarRegistry Provider。
//
// 单独成包的原因：provider 根包保持零契约依赖（纯 SDK 实现），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/pkg/registry"

	nacos "github.com/kalandramo/bald-registry-nacos"
)

// Type 是契约 registry.type 字段中 nacos 后端的取值。
const Type = "nacos"

// Provider 按契约 registry.nacos 段构造 nacos Registrar。
// 返回的 cleanup 由 appkit 停机 Effect 回放（Deregister 先于它，顺序安全）。
// 仅当 cfg.GetType() == Type 时被 RegistrarRegistry 调度到。
func Provider(ctx context.Context, cfg *bootstrapv1.Registry) (registry.Registrar, func(), error) {
	c := cfg.GetNacos()
	if c == nil {
		return nil, nil, fmt.Errorf("registry: type=%q but nacos section is missing", Type)
	}

	var opts []nacos.Option
	if len(c.GetServerAddrs()) > 0 {
		opts = append(opts, nacos.WithServerAddrs(c.GetServerAddrs()...))
	}
	if ns := c.GetNamespace(); ns != "" {
		opts = append(opts, nacos.WithNamespace(ns))
	}
	if g := c.GetGroup(); g != "" {
		opts = append(opts, nacos.WithGroup(g))
	}
	if cn := c.GetClusterName(); cn != "" {
		opts = append(opts, nacos.WithCluster(cn))
	}
	if w := c.GetWeight(); w > 0 {
		opts = append(opts, nacos.WithWeight(w))
	}
	if p := c.GetPrefix(); p != "" {
		opts = append(opts, nacos.WithPrefix(p))
	}
	if k := c.GetDefaultKind(); k != "" {
		opts = append(opts, nacos.WithDefaultKind(k))
	}

	r, err := nacos.New(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: build nacos registrar: %w", err)
	}
	return r, func() { _ = r.Close() }, nil
}
