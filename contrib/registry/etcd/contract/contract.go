// Package contract 提供 etcd 后端的契约装配：bootstrapv1.Registry 段 →
// etcd options 映射 + RegistrarRegistry Provider。
//
// 单独成包的原因：provider 根包保持零契约依赖（纯 SDK 实现），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/pkg/registry"

	etcd "github.com/kalandramo/bald-registry-etcd"
)

// Type 是契约 registry.type 字段中 etcd 后端的取值。
const Type = "etcd"

// Provider 按契约 registry.etcd 段构造 etcd Registrar。
// 返回的 cleanup 负责 client 生命周期（Close），由 RegistrarRegistry 交
// appkit Effect 在停机时回放（Deregister 先于它执行，顺序安全）。
// 仅当 cfg.GetType() == Type 时被 RegistrarRegistry 调度到。
func Provider(ctx context.Context, cfg *bootstrapv1.Registry) (registry.Registrar, func(), error) {
	c := cfg.GetEtcd()
	if c == nil {
		return nil, nil, fmt.Errorf("registry: type=%q but etcd section is missing", Type)
	}

	var opts []etcd.Option
	if len(c.GetEndpoints()) > 0 {
		opts = append(opts, etcd.WithEndpoints(c.GetEndpoints()...))
	}
	if u := c.GetUsername(); u != "" {
		opts = append(opts, etcd.WithUsername(u))
	}
	if p := c.GetPassword(); p != "" {
		opts = append(opts, etcd.WithPassword(p))
	}
	if ns := c.GetPrefix(); ns != "" {
		opts = append(opts, etcd.WithNamespace(ns))
	}
	if ttl := c.GetTtl(); ttl > 0 {
		opts = append(opts, etcd.WithRegisterTTL(time.Duration(ttl)*time.Second))
	}
	if mr := int(c.GetMaxRetry()); mr > 0 {
		opts = append(opts, etcd.WithMaxRetry(mr))
	}

	r, err := etcd.New(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: build etcd registrar: %w", err)
	}
	return r, func() { _ = r.Close() }, nil
}
