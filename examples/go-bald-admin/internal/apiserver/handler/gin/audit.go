package gin

// audit.go 审计日志查询 REST handler（T6）。分组级 Authn + 路由级 Authz
// （P9 归一化权限点 "audit"——REST /v1/audit 与 gRPC AuditService 同源，
// casbin 策略单写覆盖双协议）。只读接口（list/get），admin 专属。
// 复用 pb.go 的 bindPB/writePB 与本包 toAuditPB 转换。

import (
	"net/http"
	"strconv"
	"time"

	gingonic "github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/audit/v1"
	auditbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auditlog"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
)

// RegisterAudit 挂载审计查询路由（需认证 + audit 权限，admin 专属）。
//   - GET /v1/audit      分页查询（?category=&subject=&object=&action=&result=&ip=&page_size=&page_token=）
//   - GET /v1/audit/:id  单条详情
func RegisterAudit(
	e *gingonic.Engine,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	biz *auditbiz.Biz,
) {
	authed := e.Group("/v1")
	authed.Use(mid.AuthnMiddleware(authenticator))
	authzMW := mid.AuthzMiddleware(authorizer,
		mid.WithObjectResolver(authz.DefaultHTTPObject),
		mid.WithActionResolver(authz.DefaultHTTPAction),
	)

	authed.GET("/audit", authzMW, func(c *gingonic.Context) {
		pageSize, _ := strconv.ParseUint(c.Query("page_size"), 10, 32)
		res, err := biz.List(c.Request.Context(), auditbiz.ListFilter{
			Category: c.Query("category"),
			Subject:  c.Query("subject"),
			Object:   c.Query("object"),
			Action:   c.Query("action"),
			Result:   c.Query("result"),
			IP:       c.Query("ip"),
		}, auditbiz.Page{Size: uint32(pageSize), Token: c.Query("page_token")})
		if err != nil {
			writeBizErr(c, err)
			return
		}
		items := make([]*auditv1.AuditRecord, 0, len(res.Items))
		for _, r := range res.Items {
			items = append(items, toAuditPB(r))
		}
		writePB(c, http.StatusOK, &auditv1.ListAuditRecordsResponse{
			Items:         items,
			Total:         uint32(res.Total),
			NextPageToken: res.NextPageToken,
		})
	})

	authed.GET("/audit/:id", authzMW, func(c *gingonic.Context) {
		m, err := biz.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			writeBizErr(c, err)
			return
		}
		writePB(c, http.StatusOK, &auditv1.GetAuditRecordResponse{Record: toAuditPB(m)})
	})
}

// toAuditPB 模型 → proto（自增 ID 字符串化；UnixNano → Timestamp）。
func toAuditPB(m *authmodel.AuditRecord) *auditv1.AuditRecord {
	return &auditv1.AuditRecord{
		Id:        strconv.FormatUint(uint64(m.ID), 10),
		TenantId:  m.TenantID,
		Category:  m.Category,
		Subject:   m.Subject,
		Object:    m.Object,
		Action:    m.Action,
		Result:    m.Result,
		Error:     m.Error,
		IpAddress: m.IPAddress,
		UserAgent: m.UserAgent,
		RequestId: m.RequestID,
		TraceId:   m.TraceID,
		Time:      timestamppb.New(time.Unix(0, m.Time)),
	}
}
