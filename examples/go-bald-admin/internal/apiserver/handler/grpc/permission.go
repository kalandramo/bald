package grpc

// permission.go 权限管理 gRPC service（T3）：权限点注册表 + 角色策略数据化管理。
// 实现生成的 permissionv1.PermissionServiceServer；gRPC 与 REST 共用同一 biz 与
// casbin 策略（P9 归一化：FullMethod → "permission" + get/list/write/delete）。

import (
	"context"

	"github.com/kalandramo/bald/berrors"
	permissionv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/permission/v1"
	permissionbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/permission"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// permissionService 实现生成的 permissionv1.PermissionServiceServer。
type permissionService struct {
	permissionv1.UnimplementedPermissionServiceServer
	biz *permissionbiz.Biz
}

// NewPermissionServer 构造 PermissionServiceServer 实现（biz 由 wire 装配注入）。
func NewPermissionServer(biz *permissionbiz.Biz) permissionv1.PermissionServiceServer {
	return &permissionService{biz: biz}
}

func (s *permissionService) GetPermission(ctx context.Context, req *permissionv1.GetPermissionRequest) (*permissionv1.GetPermissionResponse, error) {
	p, err := s.biz.GetPermission(ctx, req.GetId())
	if err != nil {
		return nil, berrors.NotFound("permission")
	}
	return &permissionv1.GetPermissionResponse{Permission: toPermissionPBGRPC(p)}, nil
}

func (s *permissionService) ListPermissions(ctx context.Context, req *permissionv1.ListPermissionsRequest) (*permissionv1.ListPermissionsResponse, error) {
	ps, err := s.biz.ListPermissions(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]*permissionv1.Permission, 0, len(ps))
	for _, p := range ps {
		items = append(items, toPermissionPBGRPC(p))
	}
	return &permissionv1.ListPermissionsResponse{Items: items, Total: uint32(len(items))}, nil
}

func (s *permissionService) CreatePermission(ctx context.Context, req *permissionv1.CreatePermissionRequest) (*permissionv1.CreatePermissionResponse, error) {
	p, err := s.biz.CreatePermission(ctx, req.GetId(), req.GetName(), req.GetMenuIds(), req.GetRemark())
	if err != nil {
		return nil, err
	}
	return &permissionv1.CreatePermissionResponse{Permission: toPermissionPBGRPC(p)}, nil
}

func (s *permissionService) UpdatePermission(ctx context.Context, req *permissionv1.UpdatePermissionRequest) (*permissionv1.UpdatePermissionResponse, error) {
	p, err := s.biz.UpdatePermission(ctx, req.GetId(),
		req.GetName(), req.GetMenuIds(), req.GetMenuIdsSet(), req.GetRemark())
	if err != nil {
		return nil, err
	}
	return &permissionv1.UpdatePermissionResponse{Permission: toPermissionPBGRPC(p)}, nil
}

func (s *permissionService) DeletePermission(ctx context.Context, req *permissionv1.DeletePermissionRequest) (*permissionv1.DeletePermissionResponse, error) {
	ok, err := s.biz.DeletePermission(ctx, req.GetId())
	if err != nil || !ok {
		return nil, berrors.NotFound("permission")
	}
	return &permissionv1.DeletePermissionResponse{Deleted: req.GetId()}, nil
}

func (s *permissionService) ListRolePolicies(ctx context.Context, req *permissionv1.ListRolePoliciesRequest) (*permissionv1.ListRolePoliciesResponse, error) {
	ps, err := s.biz.ListRolePolicies(ctx, req.GetRole())
	if err != nil {
		return nil, err
	}
	items := make([]*permissionv1.RolePolicy, 0, len(ps))
	for _, p := range ps {
		items = append(items, toRolePolicyPBGRPC(p))
	}
	return &permissionv1.ListRolePoliciesResponse{Items: items, Total: uint32(len(items))}, nil
}

func (s *permissionService) CreateRolePolicy(ctx context.Context, req *permissionv1.CreateRolePolicyRequest) (*permissionv1.CreateRolePolicyResponse, error) {
	p, err := s.biz.CreateRolePolicy(ctx, req.GetRole(), req.GetObject(), req.GetAction())
	if err != nil {
		return nil, err
	}
	return &permissionv1.CreateRolePolicyResponse{Policy: toRolePolicyPBGRPC(p)}, nil
}

func (s *permissionService) DeleteRolePolicy(ctx context.Context, req *permissionv1.DeleteRolePolicyRequest) (*permissionv1.DeleteRolePolicyResponse, error) {
	ok, err := s.biz.DeleteRolePolicy(ctx, req.GetId())
	if err != nil || !ok {
		return nil, berrors.NotFound("role policy")
	}
	return &permissionv1.DeleteRolePolicyResponse{Deleted: req.GetId()}, nil
}

// toPermissionPBGRPC 模型 → proto（MenuIDs CSV → 列表）。
func toPermissionPBGRPC(p *authmodel.Permission) *permissionv1.Permission {
	pb := &permissionv1.Permission{
		Id:        p.ID,
		Name:      p.Name,
		Remark:    p.Remark,
		CreatedAt: timestamppb.New(p.CreatedAt),
		UpdatedAt: timestamppb.New(p.UpdatedAt),
	}
	for _, m := range splitPermissionCSV(p.MenuIDs) {
		pb.MenuIds = append(pb.MenuIds, m)
	}
	return pb
}

func toRolePolicyPBGRPC(p *authmodel.RolePolicy) *permissionv1.RolePolicy {
	return &permissionv1.RolePolicy{
		Id:     p.ID,
		Role:   p.Role,
		Object: p.Object,
		Action: p.Action,
	}
}

// splitPermissionCSV 逗号分隔解析（与 gin 侧同构，包级不共享避免耦合）。
func splitPermissionCSV(s string) []string {
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
