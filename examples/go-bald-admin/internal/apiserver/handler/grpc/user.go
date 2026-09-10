package grpc

// user.go 用户管理 gRPC service（T2）。租户内读写：读过滤（Where.T）与写注入
// （injectWriteTenant）由 P8 自动完成；P9 归一化 FullMethod → "user" + 动作。

import (
	"context"

	berrors "github.com/kalandramo/bald/berrors"
	userv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/user/v1"
	userbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/user"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// userService 实现生成的 userv1.UserServiceServer。
type userService struct {
	userv1.UnimplementedUserServiceServer
	biz *userbiz.Biz
}

// NewUserServer 构造 UserServiceServer 实现（biz 由 wire 装配注入）。
func NewUserServer(biz *userbiz.Biz) userv1.UserServiceServer {
	return &userService{biz: biz}
}

func (s *userService) GetUser(ctx context.Context, req *userv1.GetUserRequest) (*userv1.GetUserResponse, error) {
	u, err := s.biz.Get(ctx, req.GetId())
	if err != nil {
		return nil, berrors.NotFound("user")
	}
	return &userv1.GetUserResponse{User: toUserPBGRPC(u)}, nil
}

func (s *userService) ListUsers(ctx context.Context, req *userv1.ListUsersRequest) (*userv1.ListUsersResponse, error) {
	users, err := s.biz.List(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]*userv1.User, 0, len(users))
	for _, u := range users {
		items = append(items, toUserPBGRPC(u))
	}
	return &userv1.ListUsersResponse{Users: items, Total: uint32(len(items))}, nil
}

func (s *userService) CreateUser(ctx context.Context, req *userv1.CreateUserRequest) (*userv1.CreateUserResponse, error) {
	u, err := s.biz.Create(ctx, req.GetId(), req.GetUsername(), req.GetRoles(), req.GetPassword())
	if err != nil {
		return nil, err
	}
	return &userv1.CreateUserResponse{User: toUserPBGRPC(u)}, nil
}

func (s *userService) UpdateUser(ctx context.Context, req *userv1.UpdateUserRequest) (*userv1.UpdateUserResponse, error) {
	u, err := s.biz.Update(ctx, req.GetId(), req.GetUsername(), req.GetRoles(), req.GetPassword())
	if err != nil {
		// 此前一刀切折叠 NotFound，掩盖内部错误——仅 store 未命中归 NotFound。
		return nil, notFoundOr(err, "user")
	}
	return &userv1.UpdateUserResponse{User: toUserPBGRPC(u)}, nil
}

func (s *userService) DeleteUser(ctx context.Context, req *userv1.DeleteUserRequest) (*userv1.DeleteUserResponse, error) {
	ok, err := s.biz.Delete(ctx, req.GetId())
	if err != nil {
		return nil, notFoundOr(err, "user")
	}
	if !ok {
		return nil, berrors.NotFound("user")
	}
	return &userv1.DeleteUserResponse{Deleted: req.GetId()}, nil
}

// toUserPBGRPC 模型 → proto（密码哈希永不外泄）。
func toUserPBGRPC(u *authmodel.User) *userv1.User {
	return &userv1.User{
		Id:        u.ID,
		Username:  u.Username,
		Roles:     u.RolesList(),
		CreatedAt: timestamppb.New(u.CreatedAt),
		UpdatedAt: timestamppb.New(u.UpdatedAt),
	}
}
