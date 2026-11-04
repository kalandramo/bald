// Package contract 提供 etcd 后端的契约装配：bootstrapv1.Registry 段 →
// etcd options 映射 + 段驱动 Provider（AppProvider，见 provider.go）。
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

	etcd "github.com/kalandramo/bald/registry/etcd"
	"github.com/kalandramo/bald/retry"
)

// Type 是契约 registry.type 字段中 etcd 后端的取值。
const Type = "etcd"

// Provider 按契约 registry.etcd 段构造 etcd Registrar。
// 返回的 cleanup 负责 client 生命周期（Close），由 AppProvider 交
// appkit Effect 在停机时回放（Deregister 先于它执行，顺序安全）。
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
	if b, j, err := retreat(c.GetBackoff()); err != nil {
		return nil, nil, err
	} else if b != nil || j != nil {
		opts = append(opts, etcd.WithRetreat(b, j))
	}

	r, err := etcd.New(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: build etcd registrar: %w", err)
	}
	return r, func() { _ = r.Close() }, nil
}

// retreat 把契约的 registry.etcd.backoff 段翻译成 etcd 的退避形状。
// 段缺席返回两个 nil，即「整条曲线沿用后端默认」——这条区分是行为语义，
// 不是风格：缺席与「写了但写全」走的是不同的等待分布。
//
// 必填口径来自契约消息自己的注释：`initial` 必填、除 FIXED 外 `max` 必填。
// 这里不做「未写即沿用后端默认」的逐字段继承，因为那要求契约层知道每个后端
// 的默认数字，同一份数字就会同时住在两处。
func retreat(sec *bootstrapv1.Backoff) (retry.Backoff, retry.Jitter, error) {
	if sec == nil {
		return nil, nil, nil
	}
	initial := sec.GetInitial().AsDuration()
	if initial <= 0 {
		return nil, nil, fmt.Errorf("registry.etcd.backoff: initial 必填且须 >0（零原点的退避就是忙轮询）")
	}
	max := sec.GetMax().AsDuration()
	if sec.GetCurve() != bootstrapv1.Backoff_FIXED && max <= 0 {
		return nil, nil, fmt.Errorf("registry.etcd.backoff: 非 FIXED 曲线必须给 max（无上界的指数曲线在高位次乘出负等待）")
	}

	var b retry.Backoff
	switch sec.GetCurve() {
	case bootstrapv1.Backoff_CURVE_UNSPECIFIED, bootstrapv1.Backoff_EXPONENTIAL:
		// factor 为 0 时 retry 自己按 2 取值，这里不再复述那个默认。
		b = retry.ExponentialBackoff{Initial: initial, Factor: sec.GetFactor(), Max: max}
	case bootstrapv1.Backoff_LINEAR:
		step := sec.GetStep().AsDuration()
		if step < 0 {
			return nil, nil, fmt.Errorf("registry.etcd.backoff: step 须 ≥0（负增量的「曲线」会越等越短）")
		}
		b = retry.LinearBackoff{Initial: initial, Step: step, Max: max}
	case bootstrapv1.Backoff_FIXED:
		b = retry.FixedBackoff(initial)
	default:
		return nil, nil, fmt.Errorf("registry.etcd.backoff: unsupported curve %v", sec.GetCurve())
	}

	// JITTER_UNSPECIFIED 落回 nil，让 etcd 的默认抖动继续生效。
	var j retry.Jitter
	switch sec.GetJitter() {
	case bootstrapv1.Backoff_JITTER_UNSPECIFIED:
	case bootstrapv1.Backoff_NONE:
		j = retry.NoJitter
	case bootstrapv1.Backoff_FULL:
		j = retry.FullJitter
	case bootstrapv1.Backoff_EQUAL:
		j = retry.EqualJitter
	default:
		return nil, nil, fmt.Errorf("registry.etcd.backoff: unsupported jitter %v", sec.GetJitter())
	}
	return b, j, nil
}
