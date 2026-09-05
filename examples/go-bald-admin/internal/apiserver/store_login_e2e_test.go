package apiserver

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/store"
	"golang.org/x/crypto/bcrypt"

	authbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auth"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

func mustWhere(field, value string) *store.Where {
	return &store.Where{Filters: []*storev1.FilterCondition{store.Eq(field, value)}}
}

// TestStoreLogin_M2 验证 M2 数据化：Login 走 bald-store-gorm（SQLite 内存库）。
func TestStoreLogin_M2(t *testing.T) {
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	biz := authbiz.New(bootstrappkg.Signer)

	// admin 登录成功，签发 token 含角色。
	pair, err := biz.Login(context.Background(), authbiz.Credential{Username: "admin", Password: "admin123"})
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	if pair.AccessToken == "" {
		t.Fatal("empty token")
	}

	// alice 登录成功。
	if _, err := biz.Login(context.Background(), authbiz.Credential{Username: "alice", Password: "alice123"}); err != nil {
		t.Fatalf("alice login: %v", err)
	}

	// 错误密码应 401。
	if _, err := biz.Login(context.Background(), authbiz.Credential{Username: "admin", Password: "wrong"}); err == nil {
		t.Fatal("want ErrBadCredential for wrong password")
	}

	// 不存在用户应 401。
	if _, err := biz.Login(context.Background(), authbiz.Credential{Username: "nobody", Password: "x"}); err == nil {
		t.Fatal("want ErrBadCredential for unknown user")
	}
}

// TestStoreSeed_UsersAndRoles 验证 seed 写入了用户与角色到 store。
func TestStoreSeed_UsersAndRoles(t *testing.T) {
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	ctx := context.Background()

	admin, err := bootstrappkg.UserStore.Get(ctx, mustWhere("username", "admin"))
	if err != nil {
		t.Fatalf("get admin: %v", err)
	}
	if len(admin.RolesList()) == 0 || admin.RolesList()[0] != "admin" {
		t.Fatalf("admin roles wrong: %v", admin.RolesList())
	}

	role, err := bootstrappkg.RoleStore.Get(ctx, mustWhere("id", "viewer"))
	if err != nil {
		t.Fatalf("get viewer role: %v", err)
	}
	if len(role.PermsList()) == 0 {
		t.Fatal("viewer role has no perms")
	}
}

// TestStoreSeed_BcryptHash 验证 M3：seed 密码以 bcrypt 哈希存储，非明文。
func TestStoreSeed_BcryptHash(t *testing.T) {
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	ctx := context.Background()
	admin, err := bootstrappkg.UserStore.Get(ctx, mustWhere("username", "admin"))
	if err != nil {
		t.Fatalf("get admin: %v", err)
	}
	// 明文不应直接等于存储值（已哈希）。
	if admin.PasswordHash == "admin123" {
		t.Fatal("password stored in plaintext; expected bcrypt hash")
	}
	// bcrypt 校验应成功，错误密码应失败。
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte("admin123")); err != nil {
		t.Fatalf("bcrypt verify admin123: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte("wrong")); err == nil {
		t.Fatal("bcrypt accepted wrong password")
	}
}

// TestStoreWrite_TenantInjection M4：写路径多租户自动注入（与读路径 mergeTenant 对称）。
// Create 经 ctx 中的 TenantID 自动写入实体 tenant_id 列，业务无需手写；且越权设置的
// 租户值会被 ctx 真实租户覆盖，防止误建到他租户归属。
func TestStoreWrite_TenantInjection(t *testing.T) {
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	ctx := contextx.WithTenantID(context.Background(), "t-other")

	// 1) 未显式设 TenantID → 自动注入为 ctx 租户。
	u := &authmodel.User{ID: "u-new", Username: "newbie", PasswordHash: "x", Roles: "viewer"}
	if err := bootstrappkg.UserStore.Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.TenantID != "t-other" {
		t.Fatalf("TenantID not injected: got %q", u.TenantID)
	}
	got, err := bootstrappkg.UserStore.Get(ctx, mustWhere("id", "u-new"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TenantID != "t-other" {
		t.Fatalf("persisted TenantID: want t-other, got %q", got.TenantID)
	}

	// 2) 越权设置租户值 → 被 ctx 真实租户覆盖（防改归属）。
	u2 := &authmodel.User{ID: "u-rogue", Username: "rogue", PasswordHash: "x", TenantID: "t-default", Roles: "viewer"}
	if err := bootstrappkg.UserStore.Create(ctx, u2); err != nil {
		t.Fatalf("create rogue: %v", err)
	}
	if u2.TenantID != "t-other" {
		t.Fatalf("rogue TenantID not overridden: got %q", u2.TenantID)
	}

	// 3) 读路径隔离：t-other ctx 看不到 t-default 的已建用户（闭环验证）。
	others, _, err := bootstrappkg.UserStore.List(ctx, (&store.Where{}).T(ctx))
	if err != nil {
		t.Fatalf("list t-other: %v", err)
	}
	for _, o := range others {
		if o.TenantID != "t-other" {
			t.Fatalf("read isolation leak: saw tenant %q", o.TenantID)
		}
	}
}
