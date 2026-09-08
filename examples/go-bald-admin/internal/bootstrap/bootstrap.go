// Package bootstrap 负责桥接 bald 核心能力到本应用（M1 认证 + M2 数据化）。
//
// InitBridges 构造并保存：
//   - Authenticator：来自 bald-authn-jwt（HMAC 对称，MVP）；
//   - Authorizer：RBAC，角色→权限为静态策略，subject→角色从 store(User) 加载；
//   - DB / UserStore / RoleStore：bald-store-gorm 接入（M2，SQLite 内存库）。
//
// server 层装配中间件/gRPC service 时引用这些包级变量。
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	authnjwt "github.com/kalandramo/bald-authn-jwt"
	rediscache "github.com/kalandramo/bald-cache-redis"
	baldgorm "github.com/kalandramo/bald-store-gorm"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	"github.com/kalandramo/bald/pkg/store"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	// SQLite 用纯 Go driver（glebarez，modernc 内核）：本机/CI 无 gcc 环境零 CGO 依赖；
	// 与 gorm.io/driver/sqlite（cgo）API 兼容，Open 签名一致。
	"github.com/glebarez/sqlite"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	miniooss "github.com/kalandramo/bald/oss/minio"

	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	casbinauthz "github.com/kalandramo/bald/examples/go-bald-admin/internal/security/casbin"
)

// Authenticator 登录令牌校验器（来自 bald-authn-jwt，公钥验签实例）。
// 注入 gin/grpc 认证拦截器，仅持 RSA 公钥——无法伪造 token。
var Authenticator authn.Authenticator

// Signer 令牌签发器（来自 bald-authn-jwt，私钥签发实例）。
// 注入 auth biz 的 Login，独占 RSA 私钥；与 Authenticator 分离，实现签发/验签权解耦。
var Signer authnjwt.Signer

// Authorizer 授权器（M6.1 起由 casbin 桥接 authz.Authorizer 实现）。
var Authorizer authz.Authorizer

// lazyAuthn / lazyAuthz 把上面的包级桥接变量适配为接口——路由/服务在 main 装配期
// 注册（此时 Authenticator/Authorizer 尚为 nil，InitBridges 在 appkit.BeforeStart
// 才赋值），而认证/授权中间件在请求期才真正调用。请求期经本适配器读取最新值。
//
// 背景（M10.1 修复的真实 bug）：此前 RegisterRoutes 直接传 bootstrappkg.Authenticator
// （构造期 nil），gin AuthnMiddleware 对 nil 退化为空操作——main 进程的 HTTP 认证
// 实际一直未生效（e2e 未经过 main 装配路径故未暴露）。lazy 适配器根治该时序错位。
type lazyAuthn struct{}

func (lazyAuthn) Authenticate(ctx context.Context) (*authn.AuthClaims, error) {
	return Authenticator.Authenticate(ctx)
}

func (lazyAuthn) AuthenticateToken(token string) (*authn.AuthClaims, error) {
	return Authenticator.AuthenticateToken(token)
}

type lazyAuthz struct{}

func (lazyAuthz) Authorize(ctx context.Context, subject, object, action string) (bool, error) {
	return Authorizer.Authorize(ctx, subject, object, action)
}

// LazyAuthenticator 返回延迟解析的认证器（请求期读 bootstrappkg.Authenticator 最新值）。
func LazyAuthenticator() authn.Authenticator { return lazyAuthn{} }

// LazyAuthorizer 返回延迟解析的授权器（请求期读 bootstrappkg.Authorizer 最新值）。
func LazyAuthorizer() authz.Authorizer { return lazyAuthz{} }

// lazySigner 把包级 Signer 桥接变量适配为 authnjwt.Signer。wire 的 provideSigner
// 在 main 构造期求值（InitializeBiz 先于 BeforeStart 的 InitBridges），直接传包级
// 变量会把 nil 快照固化进 auth Biz——login 签发即 nil panic（与 M10.1 修复的
// Authenticator 构造期 nil 同款时序错位，e2e 不走 main 装配路径故未暴露）。
// 本适配器在请求期读取最新值（与 lazyAuthn/lazyAuthz 同构）。
type lazySigner struct{}

func (lazySigner) IssueToken(claims authn.AuthClaims, ttl time.Duration) (string, error) {
	return Signer.IssueToken(claims, ttl)
}

// LazySigner 返回延迟解析的签发器（请求期读 bootstrappkg.Signer 最新值）。
func LazySigner() authnjwt.Signer { return lazySigner{} }

// DB 是应用主库（M2 起为 SQLite 内存库，T0 起默认经配置 database.sql 切外部 PostgreSQL）。
var DB *gorm.DB

// RedisCache 是可选 Redis 缓存/消息总线后端（M6.2 Cache-Aside + M9 审计流）。
// rdb 为 nil 表示无 Redis 环境，调用方（缓存直连 store / 审计流降级）应降级。
var RedisCache *rediscache.Cache

// MinioStorage 是可选 MinIO 对象存储后端（T0 起由 storage.minio 配置段构造，
// T5 文件模块消费）。SDK() 为 nil 表示未配置或构造失败，调用方应降级。
var MinioStorage *miniooss.Storage

// FileBucket 是文件模块使用的对象存储桶（业务自持配置段 file.bucket；
// bconf storage.minio 契约无 bucket 字段，见移植计划 §5）。
var FileBucket string

// depsBootstrap 是 Configure 注入的框架契约配置（T0 真实依赖段）。
// BeforeStart 完成配置 Unmarshal 后调用 Configure，InitBridges 消费；
// 未调用时保持既有行为（env 变量 + SQLite 内存库），既有测试零改动。
var depsBootstrap *bootstrapv1.BootstrapConfig

// Configure 注入真实依赖配置：框架契约段（database.sql / cache.redis / storage.minio /
// registry.nacos，经 baldconfig.Unmarshal 填充）与业务自持段 fileBucket（file.bucket）。
// 幂等覆盖，须在 InitBridges 之前调用。
func Configure(cfg *bootstrapv1.BootstrapConfig, fileBucket string) {
	depsBootstrap = cfg
	FileBucket = fileBucket
}

// UserStore / RoleStore / SecretStore / TenantStore 是 bald-store-gorm 接入的泛型仓储。
// T3 新增 MenuStore（菜单树）/ PermissionStore（权限点注册表）/ RolePolicyStore
// （casbin p 行数据化，D3 策略装载真源）。
var UserStore *store.Store[authmodel.User]
var RoleStore *store.Store[authmodel.Role]
var SecretStore *store.Store[authmodel.Secret]
var TenantStore *store.Store[authmodel.Tenant]
var MenuStore *store.Store[authmodel.Menu]
var PermissionStore *store.Store[authmodel.Permission]
var RolePolicyStore *store.Store[authmodel.RolePolicy]
var DictTypeStore *store.Store[authmodel.DictType]
var DictEntryStore *store.Store[authmodel.DictEntry]

// FileStore 文件元数据仓储（T5；MinIO 对象经 MinioStorage 桥接，元数据落本库）。
var FileStore *store.Store[authmodel.File]

// AuditStore 审计日志仓储（T6 查询面；写路径经 security/audit.StoreAuditor 落库，
// 同表双通道：写走审计后端、读走本仓储）。
var AuditStore *store.Store[authmodel.AuditRecord]

// InitBridges 初始化认证/授权/存储桥接。
// 幂等：已完整初始化（DB 与 Authenticator 均非 nil）则直接返回，避免重复生成
// RSA 密钥对导致签发方与验签方密钥不一致（非对称下每次生成新密钥对，重复初始化
// 会破坏闭环）。判据必须是「装配完成」而非「步骤 1 已执行」：Authenticator 在
// openDB/seed 之前置位，若中途失败（如 DB 不可达）后重入，旧判据会直接返回 nil，
// 而 DB/stores 仍为 nil——下游 NPE；失败路径重入重新生成密钥对无害（彼时未对外
// 服务过任何 token）。
func InitBridges(ctx context.Context) error {
	if DB != nil && Authenticator != nil {
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
	if err := db.AutoMigrate(&authmodel.User{}, &authmodel.Role{}, &authmodel.Secret{}, &authmodel.AuditRecord{}, &authmodel.Tenant{},
		&authmodel.Menu{}, &authmodel.Permission{}, &authmodel.RolePolicy{},
		&authmodel.DictType{}, &authmodel.DictEntry{}, &authmodel.File{}); err != nil {
		return err
	}
	DB = db

	// 可选 Redis 后端：供消息总线/缓存复用同一真实连接。不可达仅 warn（审计流降级），
	// 不阻断启动（与 SQLite 内存库同构的"真实但可选"简化，符合 §0）。
	// T0 起：地址/密码/逻辑库经 resolveRedis 解析（env BALD_ADMIN_REDIS_ADDR 优先，
	// 其次配置段 cache.redis），云端带认证实例由此接通。
	redisAddr, redisOpts := resolveRedis()
	if rc, rerr := rediscache.New(redisAddr, redisOpts...); rerr != nil {
		log.GetLogger().Warn(ctx, "redis init skipped, audit stream disabled", "error", rerr.Error())
	} else {
		RedisCache = rc
	}

	// T0：MinIO 对象存储后端（storage.minio 配置段）。minio.New 仅本地构造不联网，
	// 失败时 SDK() 为 nil + 日志；真实可达性与建桶由 T5 文件模块 EnsureBucket 兜底，
	// 此处失败不阻断启动（与 Redis 同构的"真实但可选"）。
	if mc := depsBootstrap.GetStorage().GetMinio(); mc != nil && mc.GetEndpoint() != "" {
		MinioStorage = miniooss.NewStorage(&miniooss.Config{
			Endpoint:  mc.GetEndpoint(),
			AccessKey: mc.GetAccessKey(),
			SecretKey: mc.GetSecretKey(),
			Token:     mc.GetToken(),
			UseSsl:    mc.GetUseSsl(),
		})
		if MinioStorage == nil || MinioStorage.SDK() == nil {
			MinioStorage = nil
			log.GetLogger().Warn(ctx, "minio init failed, file module degraded", "endpoint", mc.GetEndpoint())
		} else {
			log.GetLogger().Info(ctx, "minio storage constructed", "endpoint", mc.GetEndpoint())
		}
	}
	UserStore = store.NewStore[authmodel.User](baldgorm.NewGormProvider(db, func(u *authmodel.User) string { return u.ID }))
	RoleStore = store.NewStore[authmodel.Role](baldgorm.NewGormProvider(db, func(r *authmodel.Role) string { return r.ID }))
	SecretStore = store.NewStore[authmodel.Secret](baldgorm.NewGormProvider(db, func(s *authmodel.Secret) string { return s.ID }))
	TenantStore = store.NewStore[authmodel.Tenant](baldgorm.NewGormProvider(db, func(t *authmodel.Tenant) string { return t.ID }))
	MenuStore = store.NewStore[authmodel.Menu](baldgorm.NewGormProvider(db, func(m *authmodel.Menu) string { return m.ID }))
	PermissionStore = store.NewStore[authmodel.Permission](baldgorm.NewGormProvider(db, func(p *authmodel.Permission) string { return p.ID }))
	RolePolicyStore = store.NewStore[authmodel.RolePolicy](baldgorm.NewGormProvider(db,
		func(p *authmodel.RolePolicy) string { return p.ID }))
	DictTypeStore = store.NewStore[authmodel.DictType](baldgorm.NewGormProvider(db, func(t *authmodel.DictType) string { return t.ID }))
	DictEntryStore = store.NewStore[authmodel.DictEntry](baldgorm.NewGormProvider(db, func(e *authmodel.DictEntry) string { return e.ID }))
	FileStore = store.NewStore[authmodel.File](baldgorm.NewGormProvider(db, func(f *authmodel.File) string { return f.ID }))
	// T6：审计查询仓储（自增主键；审计表全量记录不走租户读隔离，ID 提取器照常）。
	AuditStore = store.NewStore[authmodel.AuditRecord](baldgorm.NewGormProvider(db, func(r *authmodel.AuditRecord) string {
		return strconv.FormatUint(uint64(r.ID), 10)
	}))
	if err := seed(ctx); err != nil {
		return err
	}

	// 2.5) 多租户隔离（P8）：注册 tenant_id 维度，Store 查询自动注入等值过滤，
	// 业务 handler 无需手写，避免跨租户数据泄漏。DefaultTenantFunc 读取 authn
	// 认证后注入 contextx 的 TenantID。
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)

	// 3) Authorizer：M6.1 起由 casbin 桥接 authz.Authorizer 实现（P11 起实现晋升 contrib
	//    bald-authz-casbin，内嵌通用 RBAC 模型）。T3 起策略数据化装载（D3）：p 行读
	//    RolePolicy 表、g 行读 User.Roles——静态 rbac_policy.csv 已删除，策略单一真源
	//    收敛到 DB（改库重启即生效；无策略行时 casbin 默认拒绝，fail-closed）。
	policyCSV, err := loadPolicyCSV(ctx)
	if err != nil {
		return err
	}
	az, err := casbinauthz.New(policyCSV)
	if err != nil {
		return err
	}
	Authorizer = az

	log.GetLogger().Info(ctx, "bridges initialized",
		"authenticator", "bald-authn-jwt", "authorizer", "casbin", "store", "bald-store-gorm")
	return nil
}

// seed 写入 MVP 初始用户与角色（生产应走迁移脚本/初始化任务）。
func seed(ctx context.Context) error {
	// T2 租户种子：platform（平台租户，源 PlatformTenantID=0 等价物）、
	// t-default/t-other 与存量 users/secrets 的租户维度对齐。
	tenants := []*authmodel.Tenant{
		{ID: "platform", Name: "平台租户", Status: "ON", Remark: "平台管理上下文（源 PlatformTenantID=0 等价物）"},
		{ID: "t-default", Name: "默认租户", Status: "ON"},
		{ID: "t-other", Name: "第二租户", Status: "ON", Remark: "多租户隔离验证用"},
	}
	for _, t := range tenants {
		if err := TenantStore.Create(ctx, t); err != nil && err != store.ErrConflict {
			return err
		}
	}
	roles := []*authmodel.Role{
		{ID: "admin", Perms: "secret:get,secret:delete,auth:get,SecretService.GetSecret:call,SecretService.DeleteSecret:call,UserService.ListUsers:call,AuthService.WhoAmI:call"},
		{ID: "viewer", Perms: "secret:get,auth:get,SecretService.GetSecret:call,UserService.ListUsers:call"},
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
	// T3 菜单种子（自 go-wind-admin DefaultMenus 形态精简：2 目录 + 6 页面 + 1 按钮，
	// 树形 ParentID 显式编死，Order 控制展示顺序——源 meta.order 语义）。
	menus := []*authmodel.Menu{
		{ID: "menu-dashboard", Type: "CATALOG", Name: "Dashboard", Path: "/dashboard", Title: "仪表盘", Icon: "lucide:layout-dashboard", Order: -1},
		{ID: "menu-dashboard-overview", ParentID: "menu-dashboard", Type: "MENU", Name: "Overview", Path: "/dashboard/overview", Title: "总览", Order: 1},
		{ID: "menu-system", Type: "CATALOG", Name: "System", Path: "/system", Title: "系统管理", Icon: "lucide:settings", Order: 2000},
		{ID: "menu-tenant", ParentID: "menu-system", Type: "MENU", Name: "Tenant", Path: "/tenant", Title: "租户管理", Order: 1},
		{ID: "menu-user", ParentID: "menu-system", Type: "MENU", Name: "User", Path: "/user", Title: "用户管理", Order: 2},
		{ID: "menu-menu", ParentID: "menu-system", Type: "MENU", Name: "Menu", Path: "/menu", Title: "菜单管理", Order: 3},
		{ID: "menu-permission", ParentID: "menu-system", Type: "MENU", Name: "Permission", Path: "/permission", Title: "权限管理", Order: 4},
		{ID: "menu-secret", ParentID: "menu-system", Type: "MENU", Name: "Secret", Path: "/secret", Title: "机密管理", Order: 5},
		{ID: "menu-dict", ParentID: "menu-system", Type: "MENU", Name: "Dict", Path: "/dict", Title: "字典管理", Order: 6},
		{ID: "menu-file", ParentID: "menu-system", Type: "MENU", Name: "File", Path: "/file", Title: "文件管理", Order: 7},
		{ID: "menu-audit", ParentID: "menu-system", Type: "MENU", Name: "Audit", Path: "/audit", Title: "审计日志", Order: 8},
		{ID: "menu-secret-delete", ParentID: "menu-secret", Type: "BUTTON", Name: "DeleteSecret", Path: "secret:delete", Title: "删除机密", Order: 1},
	}
	for _, m := range menus {
		if err := MenuStore.Create(ctx, m); err != nil && err != store.ErrConflict {
			return err
		}
	}
	// T3 权限点种子（注册表 + 菜单可见性关联；未注册的权限码仍可被 Role.Perms
	// 引用——本表是元数据，非授权判定依据）。
	perms := []*authmodel.Permission{
		{ID: "auth:get", Name: "访问认证接口"},
		{ID: "secret:list", Name: "列出机密", MenuIDs: "menu-secret"},
		{ID: "secret:delete", Name: "删除机密", MenuIDs: "menu-secret-delete"},
		{ID: "tenant:list", Name: "列出租户", MenuIDs: "menu-tenant"},
		{ID: "user:list", Name: "列出用户", MenuIDs: "menu-user"},
		{ID: "menu:list", Name: "列出菜单", MenuIDs: "menu-menu"},
		{ID: "permission:list", Name: "列出权限", MenuIDs: "menu-permission"},
		{ID: "dict_type:list", Name: "列出字典类型", MenuIDs: "menu-dict"},
		{ID: "dict_entry:list", Name: "列出字典项", MenuIDs: "menu-dict"},
		{ID: "file:list", Name: "列出文件", MenuIDs: "menu-file"},
		{ID: "audit:list", Name: "查询审计日志", MenuIDs: "menu-audit"},
	}
	for _, p := range perms {
		if err := PermissionStore.Create(ctx, p); err != nil && err != store.ErrConflict {
			return err
		}
	}
	// T4 字典种子（自 go-wind-admin demo 数据形态精简：3 组类型 + 8 条条目）。
	// 字典是租户级业务数据（源 mixin TenantID），种子归属 t-default——租户字段
	// 由实体显式携带（启动 ctx 无租户 claims，injectWriteTenant 跳过注入，与
	// Secret 种子同构）。
	dictTypes := []*authmodel.DictType{
		{ID: "gender", TenantID: "t-default", TypeName: "性别", SortOrder: 1},
		{ID: "status", TenantID: "t-default", TypeName: "状态", SortOrder: 2},
		{ID: "yes_no", TenantID: "t-default", TypeName: "是否", SortOrder: 3},
	}
	for _, t := range dictTypes {
		if err := DictTypeStore.Create(ctx, t); err != nil && err != store.ErrConflict {
			return err
		}
	}
	num1, num2, num3 := int32(1), int32(2), int32(3)
	dictEntries := []*authmodel.DictEntry{
		{ID: "gender:male", TenantID: "t-default", TypeCode: "gender", Value: "male", Label: "男", SortOrder: 1},
		{ID: "gender:female", TenantID: "t-default", TypeCode: "gender", Value: "female", Label: "女", SortOrder: 2},
		{ID: "gender:unknown", TenantID: "t-default", TypeCode: "gender", Value: "unknown", Label: "未知", Numeric: &num3, SortOrder: 3},
		{ID: "status:on", TenantID: "t-default", TypeCode: "status", Value: "on", Label: "启用", Numeric: &num1, SortOrder: 1},
		{ID: "status:off", TenantID: "t-default", TypeCode: "status", Value: "off", Label: "禁用", Numeric: &num2, SortOrder: 2},
		{ID: "status:frozen", TenantID: "t-default", TypeCode: "status", Value: "frozen", Label: "冻结", Numeric: &num3, SortOrder: 3},
		{ID: "yes_no:y", TenantID: "t-default", TypeCode: "yes_no", Value: "y", Label: "是", Numeric: &num1, SortOrder: 1},
		{ID: "yes_no:n", TenantID: "t-default", TypeCode: "yes_no", Value: "n", Label: "否", Numeric: &num2, SortOrder: 2},
	}
	for _, e := range dictEntries {
		if err := DictEntryStore.Create(ctx, e); err != nil && err != store.ErrConflict {
			return err
		}
	}
	// T3 角色策略种子（casbin p 行数据化，D3）：内容与被删除的静态 rbac_policy.csv
	// 等价（存量授权行为零回归）+ menu/permission 管理域（仅 admin 写，viewer 只读）。
	// RolePolicy 自增主键无业务唯一键，防重复种子用「非空即跳过」。
	return seedPolicies(ctx)
}

// seedPolicies 种子化 casbin p 行（角色策略）。等价旧静态 rbac_policy.csv 的
// 全部授权语义；g 行（subject→角色）不落表，装载时从 User.Roles 生成。
// 主键 role:object:action 冲突即重复策略，防重与其他种子同构（ErrConflict 容忍）。
func seedPolicies(ctx context.Context) error {
	policies := []authmodel.RolePolicy{
		// admin：机密/认证/管理面（M6.1 起存量）。
		{Role: "admin", Object: "secret", Action: "get"},
		{Role: "admin", Object: "secret", Action: "delete"},
		{Role: "admin", Object: "secret", Action: "list"},
		{Role: "admin", Object: "auth", Action: "get"},
		{Role: "admin", Object: "admin", Action: "get"},
		{Role: "admin", Object: "admin", Action: "post"},
		{Role: "admin", Object: "admin", Action: "delete"},
		// admin：租户/用户管理（T2）。
		{Role: "admin", Object: "tenant", Action: "get"},
		{Role: "admin", Object: "tenant", Action: "post"},
		{Role: "admin", Object: "tenant", Action: "put"},
		{Role: "admin", Object: "tenant", Action: "delete"},
		{Role: "admin", Object: "tenant", Action: "list"},
		{Role: "admin", Object: "tenant", Action: "write"},
		{Role: "admin", Object: "user", Action: "get"},
		{Role: "admin", Object: "user", Action: "post"},
		{Role: "admin", Object: "user", Action: "put"},
		{Role: "admin", Object: "user", Action: "delete"},
		{Role: "admin", Object: "user", Action: "list"},
		{Role: "admin", Object: "user", Action: "write"},
		// admin：菜单/权限管理（T3 新增管理域）。动作六元组（get/post/put/delete/
		// list/write）：REST 归一化为 HTTP 动词小写（DefaultHTTPAction），gRPC 归一化为
		// get/list/write（DefaultGRPCAction，Create/Update→write）——双协议同源全放行。
		{Role: "admin", Object: "menu", Action: "get"},
		{Role: "admin", Object: "menu", Action: "post"},
		{Role: "admin", Object: "menu", Action: "put"},
		{Role: "admin", Object: "menu", Action: "delete"},
		{Role: "admin", Object: "menu", Action: "list"},
		{Role: "admin", Object: "menu", Action: "write"},
		{Role: "admin", Object: "permission", Action: "get"},
		{Role: "admin", Object: "permission", Action: "post"},
		{Role: "admin", Object: "permission", Action: "put"},
		{Role: "admin", Object: "permission", Action: "delete"},
		{Role: "admin", Object: "permission", Action: "list"},
		{Role: "admin", Object: "permission", Action: "write"},
		// viewer：只读（存量）。
		{Role: "viewer", Object: "secret", Action: "get"},
		{Role: "viewer", Object: "secret", Action: "list"},
		{Role: "viewer", Object: "auth", Action: "get"},
		{Role: "viewer", Object: "user", Action: "get"},
		{Role: "viewer", Object: "user", Action: "list"},
		// viewer：菜单/权限只读（T3，前端渲染可见性；不持有写动作）。
		{Role: "viewer", Object: "menu", Action: "get"},
		{Role: "viewer", Object: "menu", Action: "list"},
		{Role: "viewer", Object: "permission", Action: "get"},
		{Role: "viewer", Object: "permission", Action: "list"},
		// admin：字典管理（T4）。dict_type/dict_entry 同款六元组（REST HTTP 动词
		// 小写 + gRPC get/list/write 双协议全放行）。
		{Role: "admin", Object: "dict_type", Action: "get"},
		{Role: "admin", Object: "dict_type", Action: "post"},
		{Role: "admin", Object: "dict_type", Action: "put"},
		{Role: "admin", Object: "dict_type", Action: "delete"},
		{Role: "admin", Object: "dict_type", Action: "list"},
		{Role: "admin", Object: "dict_type", Action: "write"},
		{Role: "admin", Object: "dict_entry", Action: "get"},
		{Role: "admin", Object: "dict_entry", Action: "post"},
		{Role: "admin", Object: "dict_entry", Action: "put"},
		{Role: "admin", Object: "dict_entry", Action: "delete"},
		{Role: "admin", Object: "dict_entry", Action: "list"},
		{Role: "admin", Object: "dict_entry", Action: "write"},
		// viewer：字典只读（T4，业务数据读取开放给登录用户）。
		{Role: "viewer", Object: "dict_type", Action: "get"},
		{Role: "viewer", Object: "dict_type", Action: "list"},
		{Role: "viewer", Object: "dict_entry", Action: "get"},
		{Role: "viewer", Object: "dict_entry", Action: "list"},
		// admin：文件管理（T5）。同款六元组（REST HTTP 动词小写 + gRPC
		// get/list/write 双协议全放行；DefaultGRPCObject("FileService/...")="file"）。
		{Role: "admin", Object: "file", Action: "get"},
		{Role: "admin", Object: "file", Action: "post"},
		{Role: "admin", Object: "file", Action: "delete"},
		{Role: "admin", Object: "file", Action: "list"},
		{Role: "admin", Object: "file", Action: "write"},
		// viewer：文件只读（T5；上传/删除仅 admin）。
		{Role: "viewer", Object: "file", Action: "get"},
		{Role: "viewer", Object: "file", Action: "list"},
		// admin：审计查询（T6，审计数据敏感仅 admin；REST /v1/audit 与 gRPC
		// AuditService 同源归一化 "audit"）。
		{Role: "admin", Object: "audit", Action: "get"},
		{Role: "admin", Object: "audit", Action: "list"},
	}
	for i := range policies {
		p := policies[i]
		p.ID = p.Role + ":" + p.Object + ":" + p.Action
		if err := RolePolicyStore.Create(ctx, &p); err != nil && err != store.ErrConflict {
			return err
		}
	}
	return nil
}

// openDB 打开应用主库。DSN 来源优先级：
//  1. env BALD_ADMIN_DB_DSN（CI/运维覆盖手段，URL 形式按 scheme 分流）；
//  2. 配置段 database.sql（driver 字段显式分流——key=value 形式 DSN 无 scheme）；
//  3. 皆空 → SQLite 内存库（M2，零外部依赖，便于 e2e）。
//
// driver/scheme 分流：postgres(/ql) → gorm.io/driver/postgres；mysql → gorm.io/driver/mysql；
// sqlite(3)/file → SQLite。
//
// 注意：本函数返回的连接用于 AutoMigrate + seed；多连接场景 SQLite 内存库须用
// cache=shared 且 keep 一个引用，否则其他连接读到空库。
func openDB() (*gorm.DB, error) {
	dsn := os.Getenv("BALD_ADMIN_DB_DSN")
	driver := ""
	if dsn == "" {
		if sqlCfg := depsBootstrap.GetDatabase().GetSql(); sqlCfg != nil && sqlCfg.GetSource() != "" {
			dsn = sqlCfg.GetSource()
			driver = sqlCfg.GetDriver()
		}
	}
	if dsn == "" {
		return gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
	}

	gormCfg := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	// driver 字段优先（key=value 形式 DSN 无 scheme，须显式 driver）；
	// driver 为空回退 DSN scheme 识别（URL 形式 postgres://... 兼容既有 env 行为）。
	scheme := driver
	if scheme == "" {
		scheme = dsnScheme(dsn)
	}
	switch scheme {
	case "postgres", "postgresql":
		return gorm.Open(postgres.Open(dsn), gormCfg)
	case "mysql":
		return gorm.Open(mysql.Open(dsn), gormCfg)
	case "file", "sqlite", "sqlite3":
		// 显式 SQLite DSN（如 file::memory:?cache=shared）与默认同形态。
		return gorm.Open(sqlite.Open(dsn), gormCfg)
	default:
		return nil, fmt.Errorf("unsupported database driver %q (dsn scheme %q): "+
			"only postgres, mysql and sqlite are wired", driver, dsnScheme(dsn))
	}
}

// resolveRedis 解析 Redis 连接参数：env BALD_ADMIN_REDIS_ADDR 优先（覆盖手段），
// 其次配置段 cache.redis（addr/password/db）；两者皆空返回禁用态
// （New 对空 addr 返回 nil rdb 的 Cache，调用方降级直连）。
func resolveRedis() (string, []rediscache.Option) {
	if addr := os.Getenv("BALD_ADMIN_REDIS_ADDR"); addr != "" {
		return addr, nil
	}
	rc := depsBootstrap.GetCache().GetRedis()
	if rc == nil || rc.GetAddr() == "" {
		return "", nil
	}
	return rc.GetAddr(), []rediscache.Option{
		rediscache.WithPassword(rc.GetPassword()),
		rediscache.WithDB(int(rc.GetDb())),
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

// loadPolicyCSV 从 DB 装载 casbin 策略（D3 策略数据化）：
//   - p 行：RolePolicy 表全量（role,object,action 三元组）；
//   - g 行：UserStore 全量用户的 Roles 字段展开（subject→角色绑定）。
//
// 返回 csv 文本注入 contrib casbin。空库 → 空 csv → casbin 默认拒绝（fail-closed），
// 即「无策略时全部请求 403」——授权不因缺数据而放开。
func loadPolicyCSV(ctx context.Context) (string, error) {
	policies, _, err := RolePolicyStore.List(ctx, &store.Where{})
	if err != nil {
		return "", fmt.Errorf("bootstrap: load role policies: %w", err)
	}
	users, _, err := UserStore.List(ctx, &store.Where{})
	if err != nil {
		return "", fmt.Errorf("bootstrap: load users for policy: %w", err)
	}
	var sb strings.Builder
	for _, p := range policies {
		fmt.Fprintf(&sb, "p, %s, %s, %s\n", p.Role, p.Object, p.Action)
	}
	for _, u := range users {
		for _, r := range u.RolesList() {
			fmt.Fprintf(&sb, "g, %s, %s\n", u.ID, r)
		}
	}
	return sb.String(), nil
}

// loadRBACMaps 已从 bootstrap 移除（M6.1）：RBAC 策略改由 casbin 桥接加载；
// T3 起进一步数据化——p 行 RolePolicy 表、g 行 User.Roles（见 loadPolicyCSV）。
