// Package contract 提供 audit-stream 后端的契约装配：bootstrapv1 的
// audit 段 → appkit AuditRegistry Provider。
//
// 单独成包的原因（对齐 observability-otlp/contract 模式）：stream 根包
// 保持零契约依赖（纯 go-redis 实现），只有本包 import bconf + appkit——
// 业务按需 import，依赖图全程可见。
//
// redis 客户端来自业务代码而非契约段（「配置驱动参数，代码声明能力」
// 分工），故导出的是 Provider 构造器（闭包绑定客户端实例）：
//
//	ar := appkit.NewAuditRegistry()
//	ar.MustRegister(streamcontract.TypeStream, streamcontract.NewStreamProvider(rdb))
//	app, err := appkit.FromBootstrap(cfg, appkit.WithAuditRegistry(ar), ...)
//
// cleanup 即 StreamAuditor.Close（停后台 goroutine + drain 尾批缓冲），
// 由 FromBootstrap 挂 Effect 逆序回放（逆序最后执行：审计尾批在指标/
// span flush 之后收集）。
package contract

import (
	"context"
	"errors"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/audit"

	auditstream "github.com/kalandramo/bald/contrib/audit-stream"

	"github.com/redis/go-redis/v9"
)

// TypeStream 是 audit.backends 中 Redis Stream 后端的取值。
const TypeStream = "stream"

// NewStreamProvider 返回绑定 redis 客户端的 Stream 审计 Provider
// （audit.backends 含 stream：stream.stream / stream.buffer 段消费，
// 空值走实现缺省；nil 客户端 Build 时 fail-fast）。
func NewStreamProvider(rdb redis.UniversalClient) appkit.AuditProvider {
	return func(_ context.Context, cfg *bootstrapv1.Audit) (audit.Auditor, func(context.Context) error, error) {
		scfg := cfg.GetStream()
		a := auditstream.New(rdb,
			auditstream.WithStream(scfg.GetStream()),
			auditstream.WithBuffer(int(scfg.GetBuffer())),
		)
		if a == nil {
			return nil, nil, errors.New("audit-stream: nil redis client")
		}
		return a, func(context.Context) error { return a.Close() }, nil
	}
}
