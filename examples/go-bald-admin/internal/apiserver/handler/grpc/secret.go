// Package grpc 提供 go-bald-admin 的 gRPC service 装配（M5 起改用 proto 生成代码）。
//
// 取代 M2 的手写 ServiceDesc + JSON codec 范式：SecretService 由 api/secret/v1/secret.proto
// 经 buf generate 生成（adminv1.SecretServiceServer / RegisterSecretServiceServer /
// RegisterSecretServiceHandler），并复用 grpc-gateway 把同一 service 转码为 REST。
// 生成代码见 api/gen/ 目录（T2 起 proto 源与生成物统一收敛到 api/）。
// 原 ListUsers 演示 RPC（M3）已由 UserService.ListUsers（user.go）接管 GET /v1/user。
package grpc

import (
	"context"

	berrors "github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/store"

	adminv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/secret/v1"
	secretbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/secret"

	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// secretService 实现生成的 adminv1.SecretServiceServer（M5）。
// 必须嵌入 UnimplementedSecretServiceServer：protoc-gen-go-grpc 默认开启
// require_unimplemented_servers，嵌入它才能满足接口（否则编译报
// missing method mustEmbedUnimplementedSecretServiceServer）。
type secretService struct {
	adminv1.UnimplementedSecretServiceServer
	biz *secretbiz.SecretBiz // 删除经 biz 落 store（§0：禁止占位式桥接）
}

// NewServer 构造 SecretServiceServer 实现（biz 由 wire 装配注入）。
func NewServer(biz *secretbiz.SecretBiz) adminv1.SecretServiceServer {
	return &secretService{biz: biz}
}

func (s *secretService) GetSecret(ctx context.Context, req *adminv1.GetSecretRequest) (*adminv1.GetSecretResponse, error) {
	claims := authn.AuthClaimsFromContext(ctx)
	viewer := ""
	if claims != nil {
		viewer = claims.Name
	}
	// M6.3：经真实 DAL 读取，自动受 ctx 租户隔离约束（M3/M4）。越权跨租户检索被 store 拦为 NotFound。
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", req.GetId()))
	sec, err := bootstrappkg.SecretStore.Get(ctx, w.T(ctx))
	if err != nil {
		return nil, berrors.NotFound("secret")
	}
	return &adminv1.GetSecretResponse{Id: sec.ID, Content: sec.Content, Viewer: viewer}, nil
}

func (s *secretService) DeleteSecret(ctx context.Context, req *adminv1.DeleteSecretRequest) (*adminv1.DeleteSecretResponse, error) {
	// 真实删除（与 gin 侧 DELETE /v1/secret/:id 同一 biz 语义）：存在性确认 +
	// store 删除 + Cache-Aside 失效，租户隔离由 Where.T 自动完成。此前此处是
	// 占位返回（CR 审查 §0 违例）：gateway 转码的 DELETE /v1/secrets/{id} 曾
	// 全部假成功——数据原封不动却返回 200。
	ok, err := s.biz.Delete(ctx, req.GetId())
	if err != nil || !ok {
		return nil, berrors.NotFound("secret")
	}
	return &adminv1.DeleteSecretResponse{Deleted: req.GetId()}, nil
}
