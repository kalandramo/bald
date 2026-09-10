package apiserver

import (
	auditlogbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auditlog"
	authbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auth"
	dictbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/dict"
	filebiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/file"
	menubiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/menu"
	permissionbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/permission"
	secretbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/secret"
	tenantbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/tenant"
	userbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/user"
)

// BizSet 聚合本应用全部业务对象：wire 装配产物、RegisterRoutes 的入参。
//
// T10 目录架构对齐：吸收 miniblog IBiz 门面思路但不引入接口层——struct 直传，
// 与 §0「外部依赖禁止 fake/mock/stub」契约一致（无 mockgen 场景即无需接口抽象）。
// 新增域只改两处：本结构扩一个字段 + RegisterRoutes 挂一行 handler
// （此前 wire.go / wire_gen.go / server.go 三处签名联动）。
type BizSet struct {
	Auth       *authbiz.Biz
	Secret     *secretbiz.SecretBiz
	Tenant     *tenantbiz.Biz
	User       *userbiz.Biz
	Menu       *menubiz.Biz
	Permission *permissionbiz.Biz
	Dict       *dictbiz.Biz
	File       *filebiz.Biz
	AuditLog   *auditlogbiz.Biz
}
