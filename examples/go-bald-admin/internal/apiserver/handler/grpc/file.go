package grpc

// file.go 文件管理 gRPC service（T5）。实现生成的 filev1.FileServiceServer，
// 经 main.go registerGRPC 注册；gRPC 与 REST 共用同一 file biz 与 casbin 策略
// （P9 归一化：FullMethod → "file" + get/list/write/delete）。bytes 上传/下载
// 经 gRPC 直连承载（gateway 转码为 base64 JSON 可用）；推荐二进制流走 gin 主
// 服务的 multipart/流式 REST。

import (
	"context"
	"errors"

	"github.com/kalandramo/bald/berrors"
	"google.golang.org/protobuf/types/known/timestamppb"

	filev1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/file/v1"
	filebiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/file"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
)

// fileService 实现生成的 filev1.FileServiceServer。
type fileService struct {
	filev1.UnimplementedFileServiceServer
	biz *filebiz.Biz
}

// NewFileServer 构造 FileServiceServer 实现（biz 由 wire 装配注入）。
func NewFileServer(biz *filebiz.Biz) filev1.FileServiceServer {
	return &fileService{biz: biz}
}

func (s *fileService) UploadFile(ctx context.Context, req *filev1.UploadFileRequest) (*filev1.UploadFileResponse, error) {
	m, err := s.biz.Upload(ctx, req.GetFileName(), req.GetFileDirectory(), req.GetContent())
	if err != nil {
		return nil, err
	}
	return &filev1.UploadFileResponse{File: toFilePBGRPC(m)}, nil
}

func (s *fileService) DownloadFile(ctx context.Context, req *filev1.DownloadFileRequest) (*filev1.DownloadFileResponse, error) {
	m, data, err := s.biz.Download(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return &filev1.DownloadFileResponse{
		Content:  data,
		FileName: m.FileName,
		MimeType: m.MimeType,
	}, nil
}

func (s *fileService) GetFile(ctx context.Context, req *filev1.GetFileRequest) (*filev1.GetFileResponse, error) {
	m, err := s.biz.Get(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return &filev1.GetFileResponse{File: toFilePBGRPC(m)}, nil
}

func (s *fileService) ListFiles(ctx context.Context, req *filev1.ListFilesRequest) (*filev1.ListFilesResponse, error) {
	fs, total, err := s.biz.List(ctx, req.GetFileName(), req.GetMimeType())
	if err != nil {
		return nil, err
	}
	items := make([]*filev1.File, 0, len(fs))
	for _, f := range fs {
		items = append(items, toFilePBGRPC(f))
	}
	return &filev1.ListFilesResponse{Items: items, Total: uint32(total)}, nil
}

func (s *fileService) DeleteFile(ctx context.Context, req *filev1.DeleteFileRequest) (*filev1.DeleteFileResponse, error) {
	deleted, err := s.biz.Delete(ctx, req.GetId())
	if err != nil {
		// 对象缺失但元数据存在的场景已由 biz 先查元数据兜底；此处仅区分未找到
		var be *berrors.Error
		if errors.As(err, &be) && be.Code == berrors.CodeNotFound {
			return nil, berrors.NotFound("file")
		}
		return nil, err
	}
	return &filev1.DeleteFileResponse{Deleted: deleted}, nil
}

// toFilePBGRPC 模型 → proto（与 gin 侧同构转换，包级不共享以避免耦合）。
func toFilePBGRPC(m *authmodel.File) *filev1.File {
	return &filev1.File{
		Id:            m.ID,
		Provider:      m.Provider,
		BucketName:    m.BucketName,
		SaveFileName:  m.SaveFileName,
		FileDirectory: m.FileDirectory,
		FileName:      m.FileName,
		Extension:     m.Extension,
		ContentHash:   m.ContentHash,
		Size:          uint32(m.Size),
		LinkUrl:       m.LinkUrl,
		MimeType:      m.MimeType,
		CreatedBy:     m.CreatedBy,
		CreatedAt:     timestamppb.New(m.CreatedAt),
		UpdatedAt:     timestamppb.New(m.UpdatedAt),
	}
}
