// Package apiserver 是 go-bald-admin 的 HTTP 服务装配根（server 层）。
//
// 只负责把 handler 挂到 *gin.Engine；认证/授权依赖由 bootstrap 包注入（M1）。
// 不在此写业务，也不在此直接依赖 bald-authn-jwt（保持 server 层与桥接解耦）。
package apiserver

import (
	gingonic "github.com/gin-gonic/gin"

	authbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auth"
	secretbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/secret"
	hgin "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/handler/gin"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
	"github.com/kalandramo/bald/pkg/appkit"
)

// RegisterRoutes 把本应用所有路由挂到 e。认证/授权依赖从 bootstrap 注入；
// 业务对象（auth/secret biz）由 M6.4 wire 装配后传入（InitializeBiz）。
func RegisterRoutes(e *gingonic.Engine, auth *authbiz.Biz, secret *secretbiz.SecretBiz) {
	hgin.RegisterHealth(e)
	hgin.RegisterAuth(e, bootstrappkg.LazyAuthenticator(), bootstrappkg.LazyAuthorizer(), auth, secret)
}

// ComponentFactory 是管理面组件工厂的包级别名（re-export，供 cmd 层构造工厂目录）。
type ComponentFactory = hgin.ComponentFactory

// RegisterAdmin 挂载管理面路由（M10.2，re-export）。
func RegisterAdmin(e *gingonic.Engine, appFn func() *appkit.AppKit, factories map[string]ComponentFactory) {
	hgin.RegisterAdmin(e, appFn, factories)
}
