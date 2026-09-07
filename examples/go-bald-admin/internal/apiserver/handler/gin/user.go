package gin

// user.go 用户管理 REST handler（T2）。租户内读写：跨租户访问由 P8 自动拦为 404；
// 授权经 P9 归一化权限点（user + get/post/put/delete），admin 全权、viewer 只读。

import (
	"net/http"

	gingonic "github.com/gin-gonic/gin"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"google.golang.org/protobuf/types/known/timestamppb"

	userv1 "github.com/kalandramo/bald/examples/go-bald-admin/api/gen/user/v1"
	userbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/user"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
)

// RegisterUser 挂载用户管理路由。
//   - GET    /v1/user       列出当前租户用户（原 SecretService.ListUsers 接管点）
//   - GET    /v1/user/:id   取用户（租户内）
//   - POST   /v1/user       创建（自动归属 ctx 租户）
//   - PUT    /v1/user/:id   更新
//   - DELETE /v1/user/:id   删除
func RegisterUser(
	e *gingonic.Engine,
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	biz *userbiz.Biz,
) {
	authed := e.Group("/v1")
	authed.Use(mid.AuthnMiddleware(authenticator))
	authzMW := mid.AuthzMiddleware(authorizer,
		mid.WithObjectResolver(authz.DefaultHTTPObject),
		mid.WithActionResolver(authz.DefaultHTTPAction),
	)

	authed.GET("/user", authzMW, func(c *gingonic.Context) {
		users, err := biz.List(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gingonic.H{"error": err.Error()})
			return
		}
		items := make([]*userv1.User, 0, len(users))
		for _, u := range users {
			items = append(items, toUserPB(u))
		}
		writePB(c, http.StatusOK, &userv1.ListUsersResponse{Users: items, Total: uint32(len(items))})
	})

	authed.GET("/user/:id", authzMW, func(c *gingonic.Context) {
		u, err := biz.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "user not found"})
			return
		}
		writePB(c, http.StatusOK, &userv1.GetUserResponse{User: toUserPB(u)})
	})

	authed.POST("/user", authzMW, func(c *gingonic.Context) {
		var req userv1.CreateUserRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		u, err := biz.Create(c.Request.Context(), req.GetId(), req.GetUsername(), req.GetRoles(), req.GetPassword())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusCreated, &userv1.CreateUserResponse{User: toUserPB(u)})
	})

	authed.PUT("/user/:id", authzMW, func(c *gingonic.Context) {
		var req userv1.UpdateUserRequest
		if err := bindPB(c, &req); err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		u, err := biz.Update(c.Request.Context(), c.Param("id"), req.GetUsername(), req.GetRoles(), req.GetPassword())
		if err != nil {
			c.JSON(http.StatusBadRequest, gingonic.H{"error": err.Error()})
			return
		}
		writePB(c, http.StatusOK, &userv1.UpdateUserResponse{User: toUserPB(u)})
	})

	authed.DELETE("/user/:id", authzMW, func(c *gingonic.Context) {
		ok, err := biz.Delete(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "user not found"})
			return
		}
		if !ok {
			c.JSON(http.StatusNotFound, gingonic.H{"error": "user not found"})
			return
		}
		writePB(c, http.StatusOK, &userv1.DeleteUserResponse{Deleted: c.Param("id")})
	})
}

// toUserPB 模型 → proto（密码哈希永不外泄）。
func toUserPB(u *authmodel.User) *userv1.User {
	return &userv1.User{
		Id:        u.ID,
		Username:  u.Username,
		Roles:     u.RolesList(),
		CreatedAt: timestamppb.New(u.CreatedAt),
		UpdatedAt: timestamppb.New(u.UpdatedAt),
	}
}
