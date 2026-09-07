package gin

// tenant.go 租户管理 REST handler（T2）。路由挂载遵循仓库双范式：
// 分组级 Authn（/v1 需登录）+ 路由级 Authz（P9 归一化权限点）。
// REST 路径用单数段（/v1/tenant），与 gRPC DefaultGRPCObject("TenantService/...")="tenant"
// 同源——casbin 策略单写即覆盖双协议。

import (
	"net/http"

	gingonic "github.com/gin-gonic/gin"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"google.golang.org/protobuf/types/known/timestamppb"

	tenantv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/tenant/v1"
	tenantbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/tenant"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
)

// RegisterTenant 挂载租户管理路由（全部需认证 + tenant 资源权限，casbin 仅授予 admin）。
//   - GET    /v1/tenant       列出租户（平台全量）
//   - GET    /v1/tenant/:id   取租户
//   - POST   /v1/tenant       创建
//   - PUT    /v1/tenant/:id   更新
//   - DELETE /v1/tenant/:id   删除（platform 受保护）
func RegisterTenant(
	e *gingonic.Engine,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	biz *tenantbiz.Biz,
) {
	authed := e.Group("/v1")
	authed.Use(mid.AuthnMiddleware(authenticator))
	authzMW := mid.AuthzMiddleware(authorizer,
		mid.WithObjectResolver(authz.DefaultHTTPObject),
		mid.WithActionResolver(authz.DefaultHTTPAction),
	)

	authed.GET("/tenant", authzMW, func(c *gingonic.Context) {
		ts, err := biz.List(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gingonic.H{"error": err.Error()})
			return
		}
		items := make([]*tenantv1.Tenant, 0, len(ts))
		for _, t := range ts {
			items = append(items, toTenantPB(t))
		}
		writePB(c, http.StatusOK, &tenantv1.ListTenantsResponse{Items: items, Total: uint32(len(items))})
	})

	authed.GET("/tenant/:id", authzMW, func(c *gingonic.Context) {
		t, err := biz.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "tenant not found"})
			return
		}
		writePB(c, http.StatusOK, &tenantv1.GetTenantResponse{Tenant: toTenantPB(t)})
	})

	authed.POST("/tenant", authzMW, func(c *gingonic.Context) {
		var req tenantv1.CreateTenantRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		t, err := biz.Create(c.Request.Context(), req.GetId(), req.GetName(), req.GetRemark())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusCreated, &tenantv1.CreateTenantResponse{Tenant: toTenantPB(t)})
	})

	authed.PUT("/tenant/:id", authzMW, func(c *gingonic.Context) {
		var req tenantv1.UpdateTenantRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		// STATUS_UNSPECIFIED 表示「不改」，映射为空串交给 biz 跳过校验。
		status := ""
		if req.GetStatus() != tenantv1.Tenant_STATUS_UNSPECIFIED {
			status = req.GetStatus().String()
		}
		t, err := biz.Update(c.Request.Context(), c.Param("id"),
			req.GetName(), status, req.GetRemark())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusOK, &tenantv1.UpdateTenantResponse{Tenant: toTenantPB(t)})
	})

	authed.DELETE("/tenant/:id", authzMW, func(c *gingonic.Context) {
		ok, err := biz.Delete(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		if !ok {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "tenant not found"})
			return
		}
		writePB(c, http.StatusOK, &tenantv1.DeleteTenantResponse{Deleted: c.Param("id")})
	})
}

// toTenantPB 模型 → proto（时间经 timestamppb；状态字符串回读枚举）。
func toTenantPB(t *authmodel.Tenant) *tenantv1.Tenant {
	pb := &tenantv1.Tenant{
		Id:        t.ID,
		Name:      t.Name,
		Remark:    t.Remark,
		CreatedAt: timestamppb.New(t.CreatedAt),
		UpdatedAt: timestamppb.New(t.UpdatedAt),
	}
	if v, ok := tenantv1.Tenant_Status_value[t.Status]; ok {
		pb.Status = tenantv1.Tenant_Status(v)
	}
	return pb
}
