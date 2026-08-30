//go:build wireinject
// +build wireinject

// Package main 的 wire 装配声明（M6.4）。
//
// 职责边界：本文件只声明**业务层对象装配**（cache / biz），不接管 bald 核心的框架插件发现
// （appkit.Registry + init 自注册）。wire 据此生成 wire_gen.go，由 main.go 调用 InitializeBiz。
//
// 运行（需 wire 工具链）：go run github.com/google/wire/cmd/wire gen ./cmd/go-bald-admin
// 生成产物 wire_gen.go 受普通 build 编译，本文件因 wireinject build tag 被排除。
package main

import (
	"os"

	"github.com/google/wire"

	authnjwt "github.com/kalandramo/bald-authn-jwt"
	authbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auth"
	secretbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/secret"
	rediscache "github.com/kalandramo/bald/examples/go-bald-admin/internal/cache/redis"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// BizSet 是 wire 装配出的业务对象集合，供 main 注册路由/服务。
type BizSet struct {
	Auth   *authbiz.Biz
	Secret *secretbiz.SecretBiz
	Cache  *rediscache.Cache
}

// redisAddr 是 wire 的命名类型别名，区分 string 依赖（避免多重绑定冲突）。
type redisAddr string

// provideRedisAddr 从 env 提供 Redis 地址（空=禁用缓存，直连 store）。
func provideRedisAddr() redisAddr { return redisAddr(os.Getenv("BALD_ADMIN_REDIS_ADDR")) }

// provideSigner 从 bootstrap 提供 RSA 私钥签发器（auth biz 依赖 Signer 接口）。
func provideSigner() authnjwt.Signer { return bootstrappkg.Signer }

// newRedisCache 适配 redisAddr→rediscache.New（保留错误，Redis 不可达即启动失败）。
func newRedisCache(addr redisAddr) (*rediscache.Cache, error) {
	return rediscache.New(string(addr))
}

// InitializeBiz 由 wire 生成实现：显式拼装 cache + 两个 biz，依赖图编译期校验。
func InitializeBiz() (*BizSet, error) {
	wire.Build(
		provideRedisAddr,
		newRedisCache,
		provideSigner,
		authbiz.New,
		secretbiz.New,
		wire.Struct(new(BizSet), "*"),
	)
	return nil, nil
}
