//go:build wireinject
// +build wireinject

package main

import (
	"github.com/google/wire"

	rediscache "github.com/kalandramo/bald-cache-redis"

	authbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auth"
	dictbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/dict"
	menubiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/menu"
	permissionbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/permission"
	secretbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/secret"
	tenantbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/tenant"
	userbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/user"
)

// BizSet 是 wire 装配出的业务对象集合，供 main 注册路由/服务。
type BizSet struct {
	Auth       *authbiz.Biz
	Secret     *secretbiz.SecretBiz
	Tenant     *tenantbiz.Biz
	User       *userbiz.Biz
	Menu       *menubiz.Biz
	Permission *permissionbiz.Biz
	Dict       *dictbiz.Biz
	Cache      *rediscache.Cache
}

// InitializeBiz 由 wire 生成实现：显式拼装 cache + 各 biz，依赖图编译期校验。
func InitializeBiz() (*BizSet, error) {
	panic("wire not generated") // 生成实现见 wire_gen.go
}
