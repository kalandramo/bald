package gin

// permission.go 权限管理 REST handler（T3）：权限点注册表 + 角色策略数据化管理。
// 路由挂载遵循仓库双范式：分组级 Authn + 路由级 Authz（P9 归一化权限点）。
// REST 路径用单数段（/v1/permission），与 gRPC
// DefaultGRPCObject("PermissionService/...")="permission" 同源——策略单写。

import (
	"net/http"

	gingonic "github.com/gin-gonic/gin"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"google.golang.org/protobuf/types/known/timestamppb"

	permissionv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/permission/v1"
	permissionbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/permission"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
)

// RegisterPermission 挂载权限管理路由（全部需认证 + permission 资源权限）。
//   - GET    /v1/permission              列权限点
//   - GET    /v1/permission/:id          取权限点
//   - POST   /v1/permission              创建
//   - PUT    /v1/permission/:id          更新
//   - DELETE /v1/permission/:id          删除
//   - GET    /v1/permission/policy       列角色策略
//   - POST   /v1/permission/policy       新增策略行
//   - DELETE /v1/permission/policy/:id   删除策略行
//
// 注意路由序：gin 会把 /permission/policy 与 /permission/:id 同树匹配——
// policy 为静态段，gin radix 树优先精确匹配静态段，:id 分支不受影响。
func RegisterPermission(
	e *gingonic.Engine,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	biz *permissionbiz.Biz,
) {
	authed := e.Group("/v1")
	authed.Use(mid.AuthnMiddleware(authenticator))
	authzMW := mid.AuthzMiddleware(authorizer,
		mid.WithObjectResolver(authz.DefaultHTTPObject),
		mid.WithActionResolver(authz.DefaultHTTPAction),
	)

	// ---- 权限点注册表 ----

	authed.GET("/permission", authzMW, func(c *gingonic.Context) {
		ps, err := biz.ListPermissions(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gingonic.H{"error": err.Error()})
			return
		}
		items := make([]*permissionv1.Permission, 0, len(ps))
		for _, p := range ps {
			items = append(items, toPermissionPB(p))
		}
		writePB(c, http.StatusOK, &permissionv1.ListPermissionsResponse{Items: items, Total: uint32(len(items))})
	})

	authed.GET("/permission/:id", authzMW, func(c *gingonic.Context) {
		p, err := biz.GetPermission(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "permission not found"})
			return
		}
		writePB(c, http.StatusOK, &permissionv1.GetPermissionResponse{Permission: toPermissionPB(p)})
	})

	authed.POST("/permission", authzMW, func(c *gingonic.Context) {
		var req permissionv1.CreatePermissionRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		p, err := biz.CreatePermission(c.Request.Context(),
			req.GetId(), req.GetName(), req.GetMenuIds(), req.GetRemark())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusCreated, &permissionv1.CreatePermissionResponse{Permission: toPermissionPB(p)})
	})

	authed.PUT("/permission/:id", authzMW, func(c *gingonic.Context) {
		var req permissionv1.UpdatePermissionRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		p, err := biz.UpdatePermission(c.Request.Context(), c.Param("id"),
			req.GetName(), req.GetMenuIds(), req.GetMenuIdsSet(), req.GetRemark())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusOK, &permissionv1.UpdatePermissionResponse{Permission: toPermissionPB(p)})
	})

	authed.DELETE("/permission/:id", authzMW, func(c *gingonic.Context) {
		ok, err := biz.DeletePermission(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		if !ok {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "permission not found"})
			return
		}
		writePB(c, http.StatusOK, &permissionv1.DeletePermissionResponse{Deleted: c.Param("id")})
	})

	// ---- 角色策略（casbin p 行数据化管理） ----

	authed.GET("/permission/policy", authzMW, func(c *gingonic.Context) {
		ps, err := biz.ListRolePolicies(c.Request.Context(), c.Query("role"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gingonic.H{"error": err.Error()})
			return
		}
		items := make([]*permissionv1.RolePolicy, 0, len(ps))
		for _, p := range ps {
			items = append(items, toRolePolicyPB(p))
		}
		writePB(c, http.StatusOK, &permissionv1.ListRolePoliciesResponse{Items: items, Total: uint32(len(items))})
	})

	authed.POST("/permission/policy", authzMW, func(c *gingonic.Context) {
		var req permissionv1.CreateRolePolicyRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		p, err := biz.CreateRolePolicy(c.Request.Context(),
			req.GetRole(), req.GetObject(), req.GetAction())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusCreated, &permissionv1.CreateRolePolicyResponse{Policy: toRolePolicyPB(p)})
	})

	authed.DELETE("/permission/policy/:id", authzMW, func(c *gingonic.Context) {
		ok, err := biz.DeleteRolePolicy(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		if !ok {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "role policy not found"})
			return
		}
		writePB(c, http.StatusOK, &permissionv1.DeleteRolePolicyResponse{Deleted: c.Param("id")})
	})
}

// toPermissionPB 模型 → proto（MenuIDs CSV 解析为列表）。
func toPermissionPB(p *authmodel.Permission) *permissionv1.Permission {
	pb := &permissionv1.Permission{
		Id:        p.ID,
		Name:      p.Name,
		Remark:    p.Remark,
		CreatedAt: timestamppb.New(p.CreatedAt),
		UpdatedAt: timestamppb.New(p.UpdatedAt),
	}
	for _, m := range splitCSVStrings(p.MenuIDs) {
		pb.MenuIds = append(pb.MenuIds, m)
	}
	return pb
}

// toRolePolicyPB 模型 → proto。
func toRolePolicyPB(p *authmodel.RolePolicy) *permissionv1.RolePolicy {
	return &permissionv1.RolePolicy{
		Id:     p.ID,
		Role:   p.Role,
		Object: p.Object,
		Action: p.Action,
	}
}

// splitCSVStrings 逗号分隔解析（与 model.splitCSV 同构；handler 层不依赖 model
// 私有实现，独立声明避免跨包耦合）。
func splitCSVStrings(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if seg := s[start:i]; seg != "" {
				out = append(out, seg)
			}
			start = i + 1
		}
	}
	return out
}
