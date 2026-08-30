// Package bootstrap 负责桥接 bald 核心能力到本应用（M1 认证 + M2 数据化）。
//
// InitBridges 构造并保存：
//   - Authenticator：来自 bald-authn-jwt（HMAC 对称，MVP）；
//   - Authorizer：RBAC，角色→权限为静态策略，subject→角色从 store(User) 加载；
//   - DB / UserStore / RoleStore：bald-store-gorm 接入（M2，SQLite 内存库）。
// server 层装配中间件/gRPC service 时引用这些包级变量。
package bootstrap

import (
	"context"
	"fmt"
	"os"

	authnjwt "github.com/kalandramo/bald-authn-jwt"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	"github.com/kalandramo/bald/pkg/log"
	"github.com/kalandramo/bald/pkg/store"
	baldgorm "github.com/kalandramo/bald-store-gorm"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	casbinauthz "github.com/kalandramo/bald/examples/go-bald-admin/internal/security/casbin"
)

// JWTSecret 是 M1 范本的 HMAC 对称密钥（兼容保留）。
// 生产应走非对称（私钥签发 / 公钥验签）或 KMS，不应保存在进程内存常量。
// 自 M6.5 起范例默认采用 RSA 非对称：签发方持私钥、验证方只持公钥，
// 见 Signer / Authenticator 两个独立实例。
const JWTSecret = "demo-secret-change-me-in-prod"

// Authenticator 登录令牌校验器（来自 bald-authn-jwt，公钥验签实例）。
// 注入 gin/grpc 认证拦截器，仅持 RSA 公钥——无法伪造 token。
var Authenticator authn.Authenticator

// Signer 令牌签发器（来自 bald-authn-jwt，私钥签发实例）。
// 注入 auth biz 的 Login，独占 RSA 私钥；与 Authenticator 分离，实现签发/验签权解耦。
var Signer authnjwt.Signer

// Authorizer 授权器（M6.1 起由 casbin 桥接 authz.Authorizer 实现）。
var Authorizer authz.Authorizer

// DB 是应用主库（M2 起为 SQLite 内存库，生产应换外部 PostgreSQL/MySQL）。
var DB *gorm.DB

// UserStore / RoleStore / SecretStore 是 bald-store-gorm 接入的泛型仓储。
var UserStore *store.Store[authmodel.User]
var RoleStore *store.Store[authmodel.Role]
var SecretStore *store.Store[authmodel.Secret]

// InitBridges 初始化认证/授权/存储桥接。
// 幂等：已初始化（Authenticator 非 nil）则直接返回，避免重复生成 RSA 密钥对
// 导致签发方与验签方密钥不一致（非对称下每次生成新密钥对，重复初始化会破坏闭环）。
func InitBridges(ctx context.Context) error {
	if Authenticator != nil {
		return nil
	}
	// 1) Authenticator（bald-authn-jwt，RSA 非对称）。
	// 演示用启动时生成 RSA-2048 密钥对：签发方持私钥、验证方只持公钥，实现签发/
	// 验签权解耦（核心收益：下游网关/微服务只需公钥即可验签，无需也不敢持有私钥）。
	// 生产应从 KMS / 固定 PEM 文件加载密钥（重启后旧 token 仍有效、私钥可进 HSM）。
	priv, err := authnjwt.GenerateRSA(2048)
	if err != nil {
		return fmt.Errorf("bootstrap: generate RSA key: %w", err)
	}
	Signer = authnjwt.NewAuthenticator(
		authnjwt.WithRSAKeys(priv, &priv.PublicKey),
		authnjwt.WithIssuer("go-bald-admin"),
		authnjwt.WithLeeway(0),
	)
	Authenticator = authnjwt.NewAuthenticator(
		authnjwt.WithRSAKeys(nil, &priv.PublicKey), // 仅公钥：验签方无法伪造
		authnjwt.WithIssuer("go-bald-admin"),
		authnjwt.WithLeeway(0),
	)

	// 2) Store（bald-store-gorm，M4 起 DSN 可配置）。
	// 默认 SQLite 内存库（M2），生产经 env BALD_ADMIN_DB_DSN 切换到外部
	// PostgreSQL/MySQL（需引入对应 gorm driver，此处仅占位提示）。
	db, err := openDB()
	if err != nil {
		return err
	}
	if err := db.AutoMigrate(&authmodel.User{}, &authmodel.Role{}, &authmodel.Secret{}); err != nil {
		return err
	}
	DB = db
	UserStore = store.NewStore[authmodel.User](baldgorm.NewGormProvider(db, func(u *authmodel.User) string { return u.ID }))
	RoleStore = store.NewStore[authmodel.Role](baldgorm.NewGormProvider(db, func(r *authmodel.Role) string { return r.ID }))
	SecretStore = store.NewStore[authmodel.Secret](baldgorm.NewGormProvider(db, func(s *authmodel.Secret) string { return s.ID }))
	if err := seed(ctx); err != nil {
		return err
	}

	// 2.5) 多租户隔离（P8）：注册 tenant_id 维度，Store 查询自动注入等值过滤，
	// 业务 handler 无需手写，避免跨租户数据泄漏。DefaultTenantFunc 读取 authn
	// 认证后注入 contextx 的 TenantID。
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)

	// 3) Authorizer：M6.1 起由 casbin 桥接 authz.Authorizer 实现（策略见
	//    internal/security/casbin/rbac_*.conf/csv，角色→权限、subject→角色均声明于策略文件）。
	//    替换 M1 手写 RBAC 内存表，命中 §0「禁止手写假实现」契约。
	az, err := casbinauthz.New()
	if err != nil {
		return err
	}
	Authorizer = az

	log.GetLogger().Info(ctx, "bridges initialized",
		"authenticator", "bald-authn-jwt", "authorizer", "casbin", "store", "bald-store-gorm/sqlite")
	return nil
}

// seed 写入 MVP 初始用户与角色（生产应走迁移脚本/初始化任务）。
func seed(ctx context.Context) error {
	roles := []*authmodel.Role{
		{ID: "admin", Perms: "secret:get,secret:delete,auth:get,SecretService.GetSecret:call,SecretService.DeleteSecret:call,SecretService.ListUsers:call,AuthService.WhoAmI:call"},
		{ID: "viewer", Perms: "secret:get,auth:get,SecretService.GetSecret:call,SecretService.ListUsers:call"},
	}
	for _, r := range roles {
		if err := RoleStore.Create(ctx, r); err != nil && err != store.ErrConflict {
			return err
		}
	}
	// 密码以 bcrypt 哈希写入（M3 起，不再明文存储）。
	adminHash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	aliceHash, err := bcrypt.GenerateFromPassword([]byte("alice123"), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	users := []*authmodel.User{
		{ID: "u-admin", Username: "admin", PasswordHash: string(adminHash), TenantID: "t-default", Roles: "admin"},
		{ID: "u-alice", Username: "alice", PasswordHash: string(aliceHash), TenantID: "t-default", Roles: "viewer"},
		// 第二租户用户：无 secret 权限，用于验证多租户隔离（越权跨租户检索被拦）。
		{ID: "u-bob", Username: "bob", PasswordHash: string(aliceHash), TenantID: "t-other", Roles: "viewer"},
	}
	for _, u := range users {
		if err := UserStore.Create(ctx, u); err != nil && err != store.ErrConflict {
			return err
		}
	}
	// 受限资源：跨租户落 1 条 t-default + 1 条 t-other，验证 M6.3 真实 DAL 与多租户隔离。
	secrets := []*authmodel.Secret{
		{ID: "s-db-pwd", Name: "数据库口令", Content: "rds-t-default-9f3a2c", TenantID: "t-default"},
		{ID: "s-api-key", Name: "开放平台密钥", Content: "ak-t-default-77be10", TenantID: "t-default"},
		{ID: "s-other-pwd", Name: "他租户口令", Content: "rds-t-other-1d4e8f", TenantID: "t-other"},
	}
	for _, s := range secrets {
		if err := SecretStore.Create(ctx, s); err != nil && err != store.ErrConflict {
			return err
		}
	}
	return nil
}

// openDB 打开应用主库。M4 起支持 env BALD_ADMIN_DB_DSN 切换到外部 PostgreSQL/MySQL；
// 默认 SQLite 内存库（M2，零外部依赖，便于 e2e）。DSN 按 scheme 分流到对应 gorm driver：
//   - postgres:// / postgresql:// → gorm.io/driver/postgres
//   - mysql://                      → gorm.io/driver/mysql
//   - 空 / 其他                      → SQLite 内存库（M2）
//
// 注意：本函数返回的连接用于 AutoMigrate + seed；多连接场景 SQLite 内存库须用
// cache=shared 且 keep 一个引用，否则其他连接读到空库。
func openDB() (*gorm.DB, error) {
	dsn := os.Getenv("BALD_ADMIN_DB_DSN")
	if dsn == "" {
		return gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
	}

	gormCfg := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	switch dsnScheme(dsn) {
	case "postgres", "postgresql":
		return gorm.Open(postgres.Open(dsn), gormCfg)
	case "mysql":
		return gorm.Open(mysql.Open(dsn), gormCfg)
	case "file", "sqlite":
		// 显式 SQLite DSN（如 file::memory:?cache=shared）与默认同形态。
		return gorm.Open(sqlite.Open(dsn), gormCfg)
	default:
		return nil, fmt.Errorf("unsupported BALD_ADMIN_DB_DSN scheme %q: "+
			"only postgres://, postgresql:// and mysql:// are wired", dsnScheme(dsn))
	}
}

// dsnScheme 返回 DSN 的驱动 scheme（用于 openDB 分流）：
//   - "postgres://..." / "mysql://..." → "postgres" / "mysql"
//   - "file::memory:?cache=shared"（SQLite）→ "file"
//   - 裸 "dbname=test sslmode=disable"          → "dbname"
//
// 规则：取开头到首个 ':' / ' ' / '=' 之前的片段（最短前缀即 scheme）。
func dsnScheme(dsn string) string {
	for i, r := range dsn {
		if r == ':' || r == ' ' || r == '=' {
			return dsn[:i]
		}
	}
	return dsn
}

// loadRBACMaps 已从 bootstrap 移除（M6.1）：RBAC 策略改由 casbin 桥接加载，
// 角色→权限、subject→角色声明于 internal/security/casbin/rbac_policy.csv。
