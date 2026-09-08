package grpc

// tenant.go 租户管理 gRPC service（T2）。实现生成的 tenantv1.TenantServiceServer，
// 经 main.go registerGRPCService 注册；gRPC 与 REST 共用同一 tenant biz 与 casbin
// 策略（P9 归一化：FullMethod → "tenant" + get/list/write/delete）。

import (
	"context"

	berrors "github.com/kalandramo/bald/berrors"
	tenantv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/tenant/v1"
	tenantbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/tenant"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// tenantService 实现生成的 tenantv1.TenantServiceServer。
type tenantService struct {
	tenantv1.UnimplementedTenantServiceServer
	biz *tenantbiz.Biz
}

// NewTenantServer 构造 TenantServiceServer 实现（biz 由 wire 装配注入）。
func NewTenantServer(biz *tenantbiz.Biz) tenantv1.TenantServiceServer {
	return &tenantService{biz: biz}
}

func (s *tenantService) GetTenant(ctx context.Context, req *tenantv1.GetTenantRequest) (*tenantv1.GetTenantResponse, error) {
	t, err := s.biz.Get(ctx, req.GetId())
	if err != nil {
		return nil, berrors.NotFound("tenant")
	}
	return &tenantv1.GetTenantResponse{Tenant: toTenantPBGRPC(t)}, nil
}

func (s *tenantService) ListTenants(ctx context.Context, req *tenantv1.ListTenantsRequest) (*tenantv1.ListTenantsResponse, error) {
	ts, err := s.biz.List(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]*tenantv1.Tenant, 0, len(ts))
	for _, t := range ts {
		items = append(items, toTenantPBGRPC(t))
	}
	return &tenantv1.ListTenantsResponse{Items: items, Total: uint32(len(items))}, nil
}

func (s *tenantService) CreateTenant(ctx context.Context, req *tenantv1.CreateTenantRequest) (*tenantv1.CreateTenantResponse, error) {
	t, err := s.biz.Create(ctx, req.GetId(), req.GetName(), req.GetRemark())
	if err != nil {
		return nil, err
	}
	return &tenantv1.CreateTenantResponse{Tenant: toTenantPBGRPC(t)}, nil
}

func (s *tenantService) UpdateTenant(ctx context.Context, req *tenantv1.UpdateTenantRequest) (*tenantv1.UpdateTenantResponse, error) {
	// STATUS_UNSPECIFIED 表示「不改」，映射为空串交给 biz 跳过校验。
	status := ""
	if req.GetStatus() != tenantv1.Tenant_STATUS_UNSPECIFIED {
		status = req.GetStatus().String()
	}
	t, err := s.biz.Update(ctx, req.GetId(), req.GetName(), status, req.GetRemark())
	if err != nil {
		// 此前一刀切折叠 NotFound，掩盖校验（InvalidArgument）与内部错误——
		// 仅 store 未命中归 NotFound，其余透传（berrors 由 ErrorInterceptor 收口）。
		return nil, notFoundOr(err, "tenant")
	}
	return &tenantv1.UpdateTenantResponse{Tenant: toTenantPBGRPC(t)}, nil
}

func (s *tenantService) DeleteTenant(ctx context.Context, req *tenantv1.DeleteTenantRequest) (*tenantv1.DeleteTenantResponse, error) {
	ok, err := s.biz.Delete(ctx, req.GetId())
	if err != nil {
		return nil, notFoundOr(err, "tenant") // platform 保护（FailedPrecondition）等不再伪装 NotFound
	}
	if !ok {
		return nil, berrors.NotFound("tenant")
	}
	return &tenantv1.DeleteTenantResponse{Deleted: req.GetId()}, nil
}

// toTenantPBGRPC 模型 → proto（与 gin 侧同构转换；包级不共享以避免 gin/grpc 耦合）。
func toTenantPBGRPC(t *authmodel.Tenant) *tenantv1.Tenant {
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
