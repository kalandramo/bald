package gin

// dict.go 字典管理 REST handler（T4）。路由挂载遵循仓库双范式：
// 分组级 Authn（/v1 需登录）+ 路由级 Authz（P9 归一化权限点）。
// REST 路径用单数段（/v1/dict_type、/v1/dict_entry），与 gRPC
// DefaultGRPCObject("DictTypeService/...")="dict_type"、"dict_entry" 同源——
// casbin 策略单写即覆盖双协议。

import (
	"net/http"

	gingonic "github.com/gin-gonic/gin"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"google.golang.org/protobuf/types/known/timestamppb"

	dictv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/dict/v1"
	dictbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/dict"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
)

// RegisterDict 挂载字典管理路由（全部需认证 + dict_type/dict_entry 资源权限，
// admin 写 / viewer 读）。
//   - GET    /v1/dict_type        类型列表
//   - GET    /v1/dict_type/:id    取类型
//   - POST   /v1/dict_type        创建类型
//   - PUT    /v1/dict_type/:id    更新类型
//   - DELETE /v1/dict_type/:id    删除类型（级联条目）
//   - GET    /v1/dict_entry       条目列表（?type_code= 走 Cache-Aside）
//   - GET    /v1/dict_entry/:id   取条目
//   - POST   /v1/dict_entry       创建条目
//   - PUT    /v1/dict_entry/:id   更新条目
//   - DELETE /v1/dict_entry/:id   删除条目
func RegisterDict(
	e *gingonic.Engine,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	biz *dictbiz.Biz,
) {
	authed := e.Group("/v1")
	authed.Use(mid.AuthnMiddleware(authenticator))
	authzMW := mid.AuthzMiddleware(authorizer,
		mid.WithObjectResolver(authz.DefaultHTTPObject),
		mid.WithActionResolver(authz.DefaultHTTPAction),
	)

	authed.GET("/dict_type", authzMW, func(c *gingonic.Context) {
		ts, total, err := biz.ListTypes(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gingonic.H{"error": err.Error()})
			return
		}
		items := make([]*dictv1.DictType, 0, len(ts))
		for _, t := range ts {
			items = append(items, toDictTypePB(t))
		}
		writePB(c, http.StatusOK, &dictv1.ListDictTypesResponse{Items: items, Total: uint32(total)})
	})

	authed.GET("/dict_type/:id", authzMW, func(c *gingonic.Context) {
		t, err := biz.GetType(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "dict type not found"})
			return
		}
		writePB(c, http.StatusOK, &dictv1.GetDictTypeResponse{DictType: toDictTypePB(t)})
	})

	authed.POST("/dict_type", authzMW, func(c *gingonic.Context) {
		var req dictv1.CreateDictTypeRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		t, err := biz.CreateType(c.Request.Context(), req.GetId(), req.GetTypeName(), req.GetSortOrder(), req.GetRemark())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusCreated, &dictv1.CreateDictTypeResponse{DictType: toDictTypePB(t)})
	})

	authed.PUT("/dict_type/:id", authzMW, func(c *gingonic.Context) {
		var req dictv1.UpdateDictTypeRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		// 空值字段不改；sort_order/enabled 用显式 *_set 位（0/false 是合法值）。
		t, err := biz.UpdateType(c.Request.Context(), c.Param("id"),
			req.GetTypeName(), req.GetSortOrder(), req.GetSortOrderSet(),
			req.GetEnabled(), req.GetEnabledSet(), req.GetRemark())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusOK, &dictv1.UpdateDictTypeResponse{DictType: toDictTypePB(t)})
	})

	authed.DELETE("/dict_type/:id", authzMW, func(c *gingonic.Context) {
		deleted, err := biz.DeleteType(c.Request.Context(), c.Param("id"))
		if err != nil || deleted == 0 {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "dict type not found"})
			return
		}
		writePB(c, http.StatusOK, &dictv1.DeleteDictTypeResponse{Deleted: c.Param("id")})
	})

	authed.GET("/dict_entry", authzMW, func(c *gingonic.Context) {
		es, total, err := biz.ListEntries(c.Request.Context(), c.Query("type_code"))
		if err != nil {
			c.JSON(http.StatusNotFound, gingonic.H{"error": err.Error()})
			return
		}
		items := make([]*dictv1.DictEntry, 0, len(es))
		for _, e := range es {
			items = append(items, toDictEntryPB(e))
		}
		writePB(c, http.StatusOK, &dictv1.ListDictEntriesResponse{Items: items, Total: uint32(total)})
	})

	authed.GET("/dict_entry/:id", authzMW, func(c *gingonic.Context) {
		e, err := biz.GetEntry(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "dict entry not found"})
			return
		}
		writePB(c, http.StatusOK, &dictv1.GetDictEntryResponse{DictEntry: toDictEntryPB(e)})
	})

	authed.POST("/dict_entry", authzMW, func(c *gingonic.Context) {
		var req dictv1.CreateDictEntryRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		e, err := biz.CreateEntry(c.Request.Context(), req.GetTypeCode(), req.GetValue(),
			req.GetLabel(), req.Numeric, req.GetSortOrder(), req.GetRemark())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusCreated, &dictv1.CreateDictEntryResponse{DictEntry: toDictEntryPB(e)})
	})

	authed.PUT("/dict_entry/:id", authzMW, func(c *gingonic.Context) {
		var req dictv1.UpdateDictEntryRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		// numeric 用 numeric_set 位区分「改回空值」与「不改」。
		e, err := biz.UpdateEntry(c.Request.Context(), c.Param("id"),
			req.GetLabel(), req.Numeric, req.GetNumericSet(),
			req.GetSortOrder(), req.GetSortOrderSet(),
			req.GetEnabled(), req.GetEnabledSet(), req.GetRemark())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusOK, &dictv1.UpdateDictEntryResponse{DictEntry: toDictEntryPB(e)})
	})

	authed.DELETE("/dict_entry/:id", authzMW, func(c *gingonic.Context) {
		ok, err := biz.DeleteEntry(c.Request.Context(), c.Param("id"))
		if err != nil || !ok {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "dict entry not found"})
			return
		}
		writePB(c, http.StatusOK, &dictv1.DeleteDictEntryResponse{Deleted: c.Param("id")})
	})
}

// toDictTypePB 模型 → proto。
func toDictTypePB(t *authmodel.DictType) *dictv1.DictType {
	return &dictv1.DictType{
		Id:        t.ID,
		TypeName:  t.TypeName,
		SortOrder: t.SortOrder,
		Enabled:   t.Enabled,
		Remark:    t.Remark,
		CreatedAt: timestamppb.New(t.CreatedAt),
		UpdatedAt: timestamppb.New(t.UpdatedAt),
	}
}

// toDictEntryPB 模型 → proto（Numeric *int32 ↔ optional int32 指针直传）。
func toDictEntryPB(e *authmodel.DictEntry) *dictv1.DictEntry {
	return &dictv1.DictEntry{
		Id:        e.ID,
		TypeCode:  e.TypeCode,
		Value:     e.Value,
		Label:     e.Label,
		Numeric:   e.Numeric,
		SortOrder: e.SortOrder,
		Enabled:   e.Enabled,
		Remark:    e.Remark,
		CreatedAt: timestamppb.New(e.CreatedAt),
		UpdatedAt: timestamppb.New(e.UpdatedAt),
	}
}
