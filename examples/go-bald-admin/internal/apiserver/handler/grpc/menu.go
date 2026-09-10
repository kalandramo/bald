package grpc

// menu.go 菜单管理 gRPC service（T3）。实现生成的 menuv1.MenuServiceServer，
// 经 main.go registerGRPC 注册；gRPC 与 REST 共用同一 menu biz 与 casbin
// 策略（P9 归一化：FullMethod → "menu" + get/list/write/delete）。

import (
	"context"

	"github.com/kalandramo/bald/berrors"
	menuv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/menu/v1"
	menubiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/menu"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// menuService 实现生成的 menuv1.MenuServiceServer。
type menuService struct {
	menuv1.UnimplementedMenuServiceServer
	biz *menubiz.Biz
}

// NewMenuServer 构造 MenuServiceServer 实现（biz 由 wire 装配注入）。
func NewMenuServer(biz *menubiz.Biz) menuv1.MenuServiceServer {
	return &menuService{biz: biz}
}

func (s *menuService) GetMenu(ctx context.Context, req *menuv1.GetMenuRequest) (*menuv1.GetMenuResponse, error) {
	m, err := s.biz.Get(ctx, req.GetId())
	if err != nil {
		return nil, berrors.NotFound("menu")
	}
	return &menuv1.GetMenuResponse{Menu: toMenuPBGRPC(m)}, nil
}

func (s *menuService) ListMenus(ctx context.Context, req *menuv1.ListMenusRequest) (*menuv1.ListMenusResponse, error) {
	roots, total, err := s.biz.ListTree(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]*menuv1.Menu, 0, len(roots))
	for _, m := range roots {
		items = append(items, toMenuPBTreeGRPC(m))
	}
	return &menuv1.ListMenusResponse{Items: items, Total: uint32(total)}, nil
}

func (s *menuService) CreateMenu(ctx context.Context, req *menuv1.CreateMenuRequest) (*menuv1.CreateMenuResponse, error) {
	m, err := s.biz.Create(ctx, req.GetId(), req.GetParentId(),
		menuTypeStringGRPC(req.GetType()), req.GetName(), req.GetPath(),
		req.GetComponent(), req.GetTitle(), req.GetIcon(), req.GetOrder(), req.GetRemark())
	if err != nil {
		return nil, err
	}
	return &menuv1.CreateMenuResponse{Menu: toMenuPBGRPC(m)}, nil
}

func (s *menuService) UpdateMenu(ctx context.Context, req *menuv1.UpdateMenuRequest) (*menuv1.UpdateMenuResponse, error) {
	m, err := s.biz.Update(ctx, req.GetId(), req.GetParentId(),
		menuTypeStringGRPC(req.GetType()), req.GetName(), req.GetPath(),
		req.GetComponent(), req.GetTitle(), req.GetIcon(), req.GetOrder(), req.GetOrderSet(),
		menuStatusStringGRPC(req.GetStatus()), req.GetRemark())
	if err != nil {
		return nil, err
	}
	return &menuv1.UpdateMenuResponse{Menu: toMenuPBGRPC(m)}, nil
}

func (s *menuService) DeleteMenu(ctx context.Context, req *menuv1.DeleteMenuRequest) (*menuv1.DeleteMenuResponse, error) {
	deleted, err := s.biz.Delete(ctx, req.GetId())
	if err != nil || deleted == 0 {
		return nil, berrors.NotFound("menu")
	}
	return &menuv1.DeleteMenuResponse{Deleted: req.GetId()}, nil
}

// toMenuPBGRPC 模型 → proto（平铺；与 gin 侧同构转换，包级不共享以避免耦合）。
func toMenuPBGRPC(m *authmodel.Menu) *menuv1.Menu {
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

func toMenuPBTreeGRPC(m *authmodel.Menu) *menuv1.Menu {
	pb := toMenuPBGRPC(m)
	for _, ch := range m.Children {
		pb.Children = append(pb.Children, toMenuPBTreeGRPC(ch))
	}
	return pb
}

// menuTypeStringGRPC / menuStatusStringGRPC 枚举 → 存储字符串（UNSPECIFIED→空串）。
func menuTypeStringGRPC(t menuv1.Menu_Type) string {
	if t == menuv1.Menu_TYPE_UNSPECIFIED {
		return ""
	}
	return t.String()[len("TYPE_"):]
}

func menuStatusStringGRPC(s menuv1.Menu_Status) string {
	if s == menuv1.Menu_STATUS_UNSPECIFIED {
		return ""
	}
	return s.String()[len("STATUS_"):]
}
