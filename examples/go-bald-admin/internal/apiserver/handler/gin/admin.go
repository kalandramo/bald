package gin

import (
	"net/http"

	gingonic "github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/authz"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
)

// ComponentFactory 按名构造组件实例（管理面挂载请求经工厂创建，再交 appkit 挂载）。
// 工厂目录由业务定义——核心框架无法知道「如何构造业务组件」，这正是 bald 依赖倒置
// 的边界：管理面端点只做转发，组件构造知识留在业务侧。
type ComponentFactory = func() appkit.Component

// RegisterAdmin 挂载管理面路由（M10.2）：运行期组件观测与热插拔。
//   - GET    /admin/components           观测：当前停机序列中的组件名
//   - POST   /admin/components           挂载：{"name":"<工厂名>"} → 工厂构造 → MountComponent
//   - DELETE /admin/components/:name     卸载：UnmountComponent（立即 Dispose）
//
// appFn 为迟到绑定（gin 装配先于 appkit.New 构造，请求期才取 App 实例）。
//
// 三端点全部要求认证 + "admin" 资源授权（casbin 策略 p, admin, admin, get|post|delete，
// 归一化后 /admin/components → object="admin"、method → action，见 P9 DefaultHTTPObject）。
// 挂载/卸载经 A1 MountComponent/UnmountComponent 自动产生重组审计事件
// （AuditEvent{Object:"component", Action:"mount"/"unmount", Subject:请求身份}）。
func RegisterAdmin(
	e *gingonic.Engine,
	appFn func() *appkit.AppKit,
	factories map[string]ComponentFactory,
) {
	authed := e.Group("/admin")
	authed.Use(mid.AuthnMiddleware(bootstrap.LazyAuthenticator()))
	authzMW := mid.AuthzMiddleware(bootstrap.LazyAuthorizer(),
		mid.WithObjectResolver(authz.DefaultHTTPObject),
		mid.WithActionResolver(authz.DefaultHTTPAction),
	)

	// GET /admin/components：观测当前组件序列（含启动期静态组件与运行期挂载的）。
	authed.GET("/components", authzMW, func(c *gingonic.Context) {
		c.JSON(http.StatusOK, gingonic.H{"components": appFn().ListComponents()})
	})

	// POST /admin/components：按工厂名构造并运行期挂载（挂载失败=无副作用，A1 保证）。
	authed.POST("/components", authzMW, func(c *gingonic.Context) {
		var req struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": "body must be {\"name\":\"<factory>\"}"})
			return
		}
		factory, ok := factories[req.Name]
		if !ok {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "unknown component factory: " + req.Name})
			return
		}
		comp := factory()
		if err := appFn().MountComponent(c.Request.Context(), comp); err != nil {
			c.JSON(http.StatusInternalServerError, gingonic.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gingonic.H{"mounted": comp.Name()})
	})

	// DELETE /admin/components/:name：卸载并立即 Dispose（组件名=工厂产出的 Name()）。
	authed.DELETE("/components/:name", authzMW, func(c *gingonic.Context) {
		name := c.Param("name")
		if err := appFn().UnmountComponent(c.Request.Context(), name); err != nil {
			c.JSON(http.StatusNotFound, gingonic.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gingonic.H{"unmounted": name})
	})
}
