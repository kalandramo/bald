// Package contract 提供 consul 后端的契约装配：bootstrapv1.Registry 段 →
// consul options 映射 + 段驱动 Provider（AppProvider，见 provider.go）。
//
// 单独成包的原因：provider 根包保持零契约依赖（纯 SDK 实现），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/registry"

	consul "github.com/kalandramo/bald/registry/consul"
)

// Type 是契约 registry.type 字段中 consul 后端的取值。
const Type = "consul"

// Provider 按契约 registry.consul 段构造 consul Registrar。
// 返回的 cleanup 由 appkit 停机 Effect 回放（Deregister 先于它，顺序安全）。
// 由 AppProvider 的 Build 调用（段存在性 gate 见 provider.go）。
func Provider(ctx context.Context, cfg *bootstrapv1.Registry) (registry.Registrar, func(), error) {
	c := cfg.GetConsul()
	if c == nil {
		return nil, nil, fmt.Errorf("registry: type=%q but consul section is missing", Type)
	}

	var opts []consul.Option
	if addr := c.GetAddress(); addr != "" {
		opts = append(opts, consul.WithAddress(addr))
	}
	if scheme := c.GetScheme(); scheme != "" {
		opts = append(opts, consul.WithScheme(scheme))
	}
	if token := c.GetToken(); token != "" {
		opts = append(opts, consul.WithToken(token))
	}
	if dc := c.GetDatacenter(); dc != "" {
		switch dc {
		case "SINGLE":
			opts = append(opts, consul.WithDatacenter(consul.SingleDatacenter))
		case "MULTI":
			opts = append(opts, consul.WithDatacenter(consul.MultiDatacenter))
		}
	}
	// proto3 的 bool 没有 presence，「字段没写」== false == 关掉，会覆盖
	// newRegistry 里的 true 默认值。这是契约口径（见 §7.18），不是 bug。
	opts = append(opts, consul.WithHealthCheck(c.GetEnableHealthCheck()))
	opts = append(opts, consul.WithHeartbeat(c.GetHeartbeat()))
	if iv := int(c.GetHealthCheckInterval()); iv > 0 {
		opts = append(opts, consul.WithHealthCheckInterval(iv))
	}
	if to := int(c.GetHealthCheckTimeout()); to > 0 {
		opts = append(opts, consul.WithHealthCheckTimeout(time.Duration(to)*time.Second))
	}
	if dcsa := int(c.GetDeregisterCriticalServiceAfter()); dcsa > 0 {
		opts = append(opts, consul.WithDeregisterCriticalServiceAfter(dcsa))
	}

	r, err := consul.New(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: build consul registrar: %w", err)
	}
	return r, func() { _ = r.Close() }, nil
}
