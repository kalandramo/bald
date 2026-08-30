// Package apiserver 是 go-bald-admin 的 HTTP 服务装配根（server 层）。
//
// 只负责把 handler 挂到 *gin.Engine；认证/授权依赖由 bootstrap 包注入（M1）。
// 不在此写业务，也不在此直接依赖 bald-authn-jwt（保持 server 层与桥接解耦）。
package apiserver

import (
	gingonic "github.com/gin-gonic/gin"

	hgin "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/handler/gin"
	authbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auth"
	secretbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/secret"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// RegisterRoutes 把本应用所有路由挂到 e。认证/授权依赖从 bootstrap 注入；
// 业务对象（auth/secret biz）由 M6.4 wire 装配后传入（InitializeBiz）。
func RegisterRoutes(e *gingonic.Engine, auth *authbiz.Biz, secret *secretbiz.SecretBiz) {
	hgin.RegisterHealth(e)
	hgin.RegisterAuth(e, bootstrappkg.Authenticator, bootstrappkg.Authorizer, auth, secret)
}
