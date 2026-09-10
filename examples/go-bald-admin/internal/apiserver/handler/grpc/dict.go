package grpc

// dict.go 字典管理 gRPC service（T4）。实现生成的 dictv1.DictTypeServiceServer
// 与 dictv1.DictEntryServiceServer，经 main.go registerGRPC 注册；gRPC 与 REST
// 共用同一 dict biz 与 casbin 策略（P9 归一化：FullMethod → "dict_type"/
// "dict_entry" + get/list/write）。

import (
	"context"

	berrors "github.com/kalandramo/bald/berrors"
	dictv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/dict/v1"
	dictbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/dict"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// dictTypeService 实现生成的 dictv1.DictTypeServiceServer。
type dictTypeService struct {
	dictv1.UnimplementedDictTypeServiceServer
	biz *dictbiz.Biz
}

// NewDictTypeServer 构造 DictTypeServiceServer 实现（biz 由 wire 装配注入）。
func NewDictTypeServer(biz *dictbiz.Biz) dictv1.DictTypeServiceServer {
	return &dictTypeService{biz: biz}
}

func (s *dictTypeService) GetDictType(ctx context.Context, req *dictv1.GetDictTypeRequest) (*dictv1.GetDictTypeResponse, error) {
	t, err := s.biz.GetType(ctx, req.GetId())
	if err != nil {
		return nil, berrors.NotFound("dict type")
	}
	return &dictv1.GetDictTypeResponse{DictType: toDictTypePBGRPC(t)}, nil
}

func (s *dictTypeService) ListDictTypes(ctx context.Context, req *dictv1.ListDictTypesRequest) (*dictv1.ListDictTypesResponse, error) {
	ts, total, err := s.biz.ListTypes(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]*dictv1.DictType, 0, len(ts))
	for _, t := range ts {
		items = append(items, toDictTypePBGRPC(t))
	}
	return &dictv1.ListDictTypesResponse{Items: items, Total: uint32(total)}, nil
}

func (s *dictTypeService) CreateDictType(ctx context.Context, req *dictv1.CreateDictTypeRequest) (*dictv1.CreateDictTypeResponse, error) {
	t, err := s.biz.CreateType(ctx, req.GetId(), req.GetTypeName(), req.GetSortOrder(), req.GetRemark())
	if err != nil {
		return nil, err
	}
	return &dictv1.CreateDictTypeResponse{DictType: toDictTypePBGRPC(t)}, nil
}

func (s *dictTypeService) UpdateDictType(ctx context.Context, req *dictv1.UpdateDictTypeRequest) (*dictv1.UpdateDictTypeResponse, error) {
	t, err := s.biz.UpdateType(ctx, req.GetId(),
		req.GetTypeName(), req.GetSortOrder(), req.GetSortOrderSet(),
		req.GetEnabled(), req.GetEnabledSet(), req.GetRemark())
	if err != nil {
		return nil, err
	}
	return &dictv1.UpdateDictTypeResponse{DictType: toDictTypePBGRPC(t)}, nil
}

func (s *dictTypeService) DeleteDictType(ctx context.Context, req *dictv1.DeleteDictTypeRequest) (*dictv1.DeleteDictTypeResponse, error) {
	deleted, err := s.biz.DeleteType(ctx, req.GetId())
	if err != nil || deleted == 0 {
		return nil, berrors.NotFound("dict type")
	}
	return &dictv1.DeleteDictTypeResponse{Deleted: req.GetId()}, nil
}

// dictEntryService 实现生成的 dictv1.DictEntryServiceServer。
type dictEntryService struct {
	dictv1.UnimplementedDictEntryServiceServer
	biz *dictbiz.Biz
}

// NewDictEntryServer 构造 DictEntryServiceServer 实现（biz 由 wire 装配注入）。
func NewDictEntryServer(biz *dictbiz.Biz) dictv1.DictEntryServiceServer {
	return &dictEntryService{biz: biz}
}

func (s *dictEntryService) GetDictEntry(ctx context.Context, req *dictv1.GetDictEntryRequest) (*dictv1.GetDictEntryResponse, error) {
	e, err := s.biz.GetEntry(ctx, req.GetId())
	if err != nil {
		return nil, berrors.NotFound("dict entry")
	}
	return &dictv1.GetDictEntryResponse{DictEntry: toDictEntryPBGRPC(e)}, nil
}

func (s *dictEntryService) ListDictEntries(ctx context.Context, req *dictv1.ListDictEntriesRequest) (*dictv1.ListDictEntriesResponse, error) {
	es, total, err := s.biz.ListEntries(ctx, req.GetTypeCode())
	if err != nil {
		return nil, err
	}
	items := make([]*dictv1.DictEntry, 0, len(es))
	for _, e := range es {
		items = append(items, toDictEntryPBGRPC(e))
	}
	return &dictv1.ListDictEntriesResponse{Items: items, Total: uint32(total)}, nil
}

func (s *dictEntryService) CreateDictEntry(ctx context.Context, req *dictv1.CreateDictEntryRequest) (*dictv1.CreateDictEntryResponse, error) {
	e, err := s.biz.CreateEntry(ctx, req.GetTypeCode(), req.GetValue(),
		req.GetLabel(), req.Numeric, req.GetSortOrder(), req.GetRemark())
	if err != nil {
		return nil, err
	}
	return &dictv1.CreateDictEntryResponse{DictEntry: toDictEntryPBGRPC(e)}, nil
}

func (s *dictEntryService) UpdateDictEntry(ctx context.Context, req *dictv1.UpdateDictEntryRequest) (*dictv1.UpdateDictEntryResponse, error) {
	e, err := s.biz.UpdateEntry(ctx, req.GetId(),
		req.GetLabel(), req.Numeric, req.GetNumericSet(),
		req.GetSortOrder(), req.GetSortOrderSet(),
		req.GetEnabled(), req.GetEnabledSet(), req.GetRemark())
	if err != nil {
		return nil, err
	}
	return &dictv1.UpdateDictEntryResponse{DictEntry: toDictEntryPBGRPC(e)}, nil
}

func (s *dictEntryService) DeleteDictEntry(ctx context.Context, req *dictv1.DeleteDictEntryRequest) (*dictv1.DeleteDictEntryResponse, error) {
	ok, err := s.biz.DeleteEntry(ctx, req.GetId())
	if err != nil || !ok {
		return nil, berrors.NotFound("dict entry")
	}
	return &dictv1.DeleteDictEntryResponse{Deleted: req.GetId()}, nil
}

// toDictTypePBGRPC 模型 → proto（与 gin 侧同构转换，包级不共享以避免耦合）。
func toDictTypePBGRPC(t *authmodel.DictType) *dictv1.DictType {
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

// toDictEntryPBGRPC 模型 → proto（Numeric *int32 ↔ optional int32 指针直传）。
func toDictEntryPBGRPC(e *authmodel.DictEntry) *dictv1.DictEntry {
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
