package gin

// menu.go 菜单管理 REST handler（T3）。路由挂载遵循仓库双范式：
// 分组级 Authn（/v1 需登录）+ 路由级 Authz（P9 归一化权限点）。
// REST 路径用单数段（/v1/menu），与 gRPC DefaultGRPCObject("MenuService/...")="menu"
// 同源——casbin 策略单写即覆盖双协议。

import (
	"net/http"

	gingonic "github.com/gin-gonic/gin"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"google.golang.org/protobuf/types/known/timestamppb"

	menuv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/menu/v1"
	menubiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/menu"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
)

// RegisterMenu 挂载菜单管理路由（全部需认证 + menu 资源权限，admin 写 / viewer 读）。
//   - GET    /v1/menu       菜单树
//   - GET    /v1/menu/:id   取节点
//   - POST   /v1/menu       创建
//   - PUT    /v1/menu/:id   更新
//   - DELETE /v1/menu/:id   删除（级联子树）
func RegisterMenu(
	e *gingonic.Engine,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	biz *menubiz.Biz,
) {
	authed := e.Group("/v1")
	authed.Use(mid.AuthnMiddleware(authenticator))
	authzMW := mid.AuthzMiddleware(authorizer,
		mid.WithObjectResolver(authz.DefaultHTTPObject),
		mid.WithActionResolver(authz.DefaultHTTPAction),
	)

	authed.GET("/menu", authzMW, func(c *gingonic.Context) {
		roots, total, err := biz.ListTree(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gingonic.H{"error": err.Error()})
			return
		}
		items := make([]*menuv1.Menu, 0, len(roots))
		for _, m := range roots {
			items = append(items, toMenuPBTree(m))
		}
		writePB(c, http.StatusOK, &menuv1.ListMenusResponse{Items: items, Total: uint32(total)})
	})

	authed.GET("/menu/:id", authzMW, func(c *gingonic.Context) {
		m, err := biz.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "menu not found"})
			return
		}
		writePB(c, http.StatusOK, &menuv1.GetMenuResponse{Menu: toMenuPB(m)})
	})

	authed.POST("/menu", authzMW, func(c *gingonic.Context) {
		var req menuv1.CreateMenuRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		m, err := biz.Create(c.Request.Context(), req.GetId(), req.GetParentId(),
			menuTypeString(req.GetType()), req.GetName(), req.GetPath(),
			req.GetComponent(), req.GetTitle(), req.GetIcon(), req.GetOrder(), req.GetRemark())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusCreated, &menuv1.CreateMenuResponse{Menu: toMenuPB(m)})
	})

	authed.PUT("/menu/:id", authzMW, func(c *gingonic.Context) {
		var req menuv1.UpdateMenuRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		// UNSPECIFIED 表示「不改」，映射空串交给 biz 跳过；order 用显式 order_set 位。
		m, err := biz.Update(c.Request.Context(), c.Param("id"),
			req.GetParentId(), menuTypeString(req.GetType()), req.GetName(), req.GetPath(),
			req.GetComponent(), req.GetTitle(), req.GetIcon(), req.GetOrder(), req.GetOrderSet(),
			menuStatusString(req.GetStatus()), req.GetRemark())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusOK, &menuv1.UpdateMenuResponse{Menu: toMenuPB(m)})
	})

	authed.DELETE("/menu/:id", authzMW, func(c *gingonic.Context) {
		deleted, err := biz.Delete(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		if deleted == 0 {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "menu not found"})
			return
		}
		writePB(c, http.StatusOK, &menuv1.DeleteMenuResponse{Deleted: c.Param("id")})
	})
}

// toMenuPB 模型 → proto（平铺，不带 children）。
func toMenuPB(m *authmodel.Menu) *menuv1.Menu {
	pb := &menuv1.Menu{
		Id:        m.ID,
		ParentId:  m.ParentID,
		Name:      m.Name,
		Path:      m.Path,
		Component: m.Component,
		Title:     m.Title,
		Icon:      m.Icon,
		Order:     m.Order,
		Remark:    m.Remark,
		CreatedAt: timestamppb.New(m.CreatedAt),
		UpdatedAt: timestamppb.New(m.UpdatedAt),
	}
	if v, ok := menuv1.Menu_Type_value[m.Type]; ok {
		pb.Type = menuv1.Menu_Type(v)
	}
	if v, ok := menuv1.Menu_Status_value[m.Status]; ok {
		pb.Status = menuv1.Menu_Status(v)
	}
	return pb
}

// toMenuPBTree 模型树 → proto 树（递归 children）。
func toMenuPBTree(m *authmodel.Menu) *menuv1.Menu {
	pb := toMenuPB(m)
	for _, ch := range m.Children {
		pb.Children = append(pb.Children, toMenuPBTree(ch))
	}
	return pb
}

// menuTypeString / menuStatusString proto 枚举 → 存储字符串（UNSPECIFIED=不改→空串）。
func menuTypeString(t menuv1.Menu_Type) string {
	if t == menuv1.Menu_TYPE_UNSPECIFIED {
		return ""
	}
	return t.String()[len("TYPE_"):] // "TYPE_MENU" → "MENU"
}

func menuStatusString(s menuv1.Menu_Status) string {
	if s == menuv1.Menu_STATUS_UNSPECIFIED {
		return ""
	}
	return s.String()[len("STATUS_"):] // "STATUS_ON" → "ON"
}
