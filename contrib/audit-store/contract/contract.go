// Package contract 提供 audit-store 后端的契约装配：bootstrapv1 的
// audit 段 → appkit AuditRegistry Provider。
//
// 单独成包的原因（对齐 observability-otlp/contract 模式）：store 根包
// 保持零契约依赖（纯 gorm 实现），只有本包 import bconf + appkit——业务
// 按需 import，依赖图全程可见。
//
// gorm 连接来自业务代码而非契约段（「配置驱动参数，代码声明能力」分工），
// 故导出的是 Provider 构造器（闭包绑定连接实例）：
//
//	ar := appkit.NewAuditRegistry()
//	ar.MustRegister(storecontract.TypeStore, storecontract.NewStoreProvider(db))
//	app, err := appkit.FromBootstrap(cfg, appkit.WithAuditRegistry(ar), ...)
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/audit"

	auditstore "github.com/kalandramo/bald/contrib/audit-store"

	"gorm.io/gorm"
)

// TypeStore 是 audit.type 中落库后端的取值。
const TypeStore = "store"

// NewStoreProvider 返回绑定 gorm 连接的落库审计 Provider
// （audit.type=store：store.migrate=true 时自动迁移默认审计表）。
func NewStoreProvider(db *gorm.DB) appkit.AuditProvider {
	return func(_ context.Context, cfg *bootstrapv1.Audit) (audit.Auditor, func(context.Context) error, error) {
		if cfg.GetStore().GetMigrate() {
			if err := db.AutoMigrate(auditstore.DefaultModel()); err != nil {
				return nil, nil, fmt.Errorf("audit-store: migrate default table: %w", err)
			}
		}
		return auditstore.New(db), nil, nil
	}
}
