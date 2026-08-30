// Package grpc 提供 go-bald-admin 的 gRPC service 装配（M5 起改用 proto 生成代码）。
//
// 取代 M2 的手写 ServiceDesc + JSON codec 范式：SecretService 由 proto/secret.proto
// 经 buf generate 生成（adminv1.SecretServiceServer / RegisterSecretServiceServer /
// RegisterSecretServiceHandler），并复用 grpc-gateway 把同一 service 转码为 REST。
// 生成代码见 gen/ 目录（secret.pb.go / secret_grpc.pb.go / secret.pb.gw.go）。
package grpc

import (
	"context"

	"github.com/kalandramo/bald/pkg/authn"
	berrors "github.com/kalandramo/bald/pkg/berrors"
	"github.com/kalandramo/bald/pkg/store"

	adminv1 "github.com/kalandramo/bald/examples/go-bald-admin/gen/secretv1"

	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// secretService 实现生成的 adminv1.SecretServiceServer（M5）。
// 必须嵌入 UnimplementedSecretServiceServer：protoc-gen-go-grpc 默认开启
// require_unimplemented_servers，嵌入它才能满足接口（否则编译报
// missing method mustEmbedUnimplementedSecretServiceServer）。
type secretService struct {
	adminv1.UnimplementedSecretServiceServer
}

// NewServer 构造 SecretServiceServer 实现（供 register 回调使用）。
func NewServer() adminv1.SecretServiceServer { return &secretService{} }

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
	return &adminv1.DeleteSecretResponse{Deleted: req.GetId()}, nil
}

// ListUsers 列出当前租户用户，验证 P8 多租户隔离：Where.T(ctx) 自动注入 tenant_id
// 等值条件，无需业务手写，跨租户用户不可见。
func (s *secretService) ListUsers(ctx context.Context, _ *adminv1.ListUsersRequest) (*adminv1.ListUsersResponse, error) {
	users, _, err := bootstrappkg.UserStore.List(ctx, (&store.Where{}).T(ctx))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	return &adminv1.ListUsersResponse{Users: ids}, nil
}
