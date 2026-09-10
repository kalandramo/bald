package grpc

// audit.go 审计日志查询 gRPC service（T6）。实现生成的 auditv1.AuditServiceServer，
// 经 main.go registerGRPC 注册；与 REST 共用同一 auditlog biz 与 casbin 策略
// （P9 归一化：AuditService → "audit" + get/list）。只读查询接口。

import (
	"context"
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/audit/v1"
	auditbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auditlog"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
)

// auditService 实现生成的 auditv1.AuditServiceServer。
type auditService struct {
	auditv1.UnimplementedAuditServiceServer
	biz *auditbiz.Biz
}

// NewAuditServer 构造 AuditServiceServer 实现（biz 由 wire 装配注入）。
func NewAuditServer(biz *auditbiz.Biz) auditv1.AuditServiceServer {
	return &auditService{biz: biz}
}

func (s *auditService) ListAuditRecords(ctx context.Context, req *auditv1.ListAuditRecordsRequest) (*auditv1.ListAuditRecordsResponse, error) {
	res, err := s.biz.List(ctx, auditbiz.ListFilter{
		Category: req.GetCategory(),
		Subject:  req.GetSubject(),
		Object:   req.GetObject(),
		Action:   req.GetAction(),
		Result:   req.GetResult(),
		IP:       req.GetIp(),
	}, auditbiz.Page{Size: req.GetPageSize(), Token: req.GetPageToken()})
	if err != nil {
		return nil, err
	}
	items := make([]*auditv1.AuditRecord, 0, len(res.Items))
	for _, r := range res.Items {
		items = append(items, toAuditPBGRPC(r))
	}
	return &auditv1.ListAuditRecordsResponse{
		Items:         items,
		Total:         uint32(res.Total),
		NextPageToken: res.NextPageToken,
	}, nil
}

func (s *auditService) GetAuditRecord(ctx context.Context, req *auditv1.GetAuditRecordRequest) (*auditv1.GetAuditRecordResponse, error) {
	m, err := s.biz.Get(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return &auditv1.GetAuditRecordResponse{Record: toAuditPBGRPC(m)}, nil
}

// toAuditPBGRPC 模型 → proto（与 gin 侧同构转换）。
func toAuditPBGRPC(m *authmodel.AuditRecord) *auditv1.AuditRecord {
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
