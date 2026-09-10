# go-wind-admin 业务移植计划

> 目标：将 `go-wind-admin/backend` 的**精选业务子集**移植到 `bald/examples/go-bald-admin`，
> 接入**云端真实服务依赖**（PostgreSQL / Redis / MinIO / Nacos / OTLP），用真实业务全面验证 bald 框架能力。
> 本计划延续 [`examples/go-bald-admin/docs/设计文档.md`](../../examples/go-bald-admin/docs/设计文档.md) §0
> 「外部依赖禁止 fake/mock/stub」硬契约；所有模块映射、配置键、文件路径均来自已核实的代码事实。

## 1. 背景与目标

`go-bald-admin` 已完成 M0–M9（secret 双协议演示、JWT、casbin RBAC、多租户、审计三后端、
Prometheus/OTLP），但数据层仍以 **SQLite 内存库**为主、未接对象存储与真实注册发现。
`go-wind-admin/backend` 是完整的管理后台（38 个业务 service、PostgreSQL/Redis/MinIO/Jaeger 全家桶，
技术栈 gow 脚手架 + Ent ORM）。

本次移植**不是全量复刻**，而是选一组代表性业务模块迁移到 bald 分层范式上，
把 bald 的每条能力轴都压上真实后端：

| bald 能力轴 | 现状 | 移植后 |
|---|---|---|
| 存储（store-gorm） | SQLite 内存库 | **云端 PostgreSQL**（openDB 的 PG 分支已有，本移植默认走它） |
| 缓存（cache-redis） | 可选、secret 用 | **云端 Redis**：字典 Cache-Aside + 审计 Stream |
| 对象存储（oss/minio） | 未接 | **云端 MinIO**：文件上传/下载 |
| 注册发现（registry/nacos） | `appkit.Registrar(inmemory.New())` | **云端 Nacos**（contrib/registry/nacos 契约装配） |
| 遥测（observability-otlp） | env 可选 | 云端 OTLP Collector 直推（保持 env 机制） |
| 审计（pkg/audit） | log/store/stream 三后端 | 落 PG + 字段增强 + **查询接口** |
| 授权（authz-casbin） | 内嵌 CSV 静态策略 | **策略数据化**（角色策略落 PG，启动期装载） |
| 多租户（P8） | tenant_id 注入 + 双租户种子 | 租户 CRUD 业务 + P8 隔离双层融合 |

## 2. 移植范围

### 2.1 精选子集（本次实施）

| # | 业务域 | 源项目位置（proto / service） | 落点（go-bald-admin） |
|---|--------|------------------------------|----------------------|
| 1 | 租户管理 tenant | `api/protos/identity/service/v1/tenant.proto` | `internal/apiserver/biz/v1/tenant/` + `model.Tenant` |
| 2 | 用户管理 user | `api/protos/identity/service/v1/user.proto`、`user_profile.proto` | 扩展存量 `model.User` + 新增 biz/handler |
| 3 | 角色 role / 权限 permission | `api/protos/permission/service/v1/{role,permission}.proto` | 新增 `model.Permission` + 策略数据化 |
| 4 | 菜单 menu | `api/protos/permission/service/v1/menu.proto` | 新增 `model.Menu`（树形 CRUD） |
| 5 | 字典 dict_type / dict_entry | `api/protos/dict/service/v1/{dict_type,dict_entry}.proto` | 新增 `model.DictType/DictEntry` + Redis Cache-Aside |
| 6 | 审计日志（操作/登录） | `api/protos/audit/service/v1/{operation_audit_log,login_audit_log}.proto` | 扩展 `AuditRecord` + 查询接口 |
| 7 | 文件 file / oss | `api/protos/storage/service/v1/{file,file_transfer,oss}.proto` | 新增 `model.File` + oss/minio 桥 |
| 8 | 认证扩展 | `api/protos/authentication/service/v1/{authentication,user_credential}.proto` | 扩展存量 auth biz（对接真实用户/租户字段） |
| 9 | 注册发现（横切） | 源项目 Consul → 改 **Nacos** | `cmd/go-bald-admin/main.go` 装配替换 |

> 源项目技术栈对照说明：源用 **Ent ORM + kratos-bootstrap proto 强类型配置**；
> 目标统一为 **GORM（contrib/store-gorm）+ appkit 四源配置 + bconf BootstrapConfig proto 段**，
> 数据模型按源 Ent schema（`app/admin/service/internal/data/ent/schema/` 为唯一真源，`sql/` 目录无 DDL）
> 手工映射，禁止引入 Ent。

### 2.2 后续迭代清单（T8 冻结）

> **T8 起冻结**：以下清单为 T0-T8 移植范围之外的功能扩展项，随 bald 能力演进按需启动，
> 不在本移植计划验收范围。

MFA、登录策略（login_policy）、组织单元/岗位（org_unit/position/membership）、
套餐与配额（plan/plan_module/plan_quota）、内部消息（internal_message 3 类）、
任务队列（task → transport/asynq）、SSE 推送、仪表盘（dashboard）、
数据访问审计（data_access_audit_log，需 SQL Driver 包装）、权限评估日志（policy_evaluation_log）、
多语言（language/i18n）、API 资产管理（sys_apis）、Redis 缓存监控、
审计 log_hash + ECDSA 数字签名、密码复杂度策略与历史口令。

## 3. 业务 → bald 能力验证点映射

| 移植模块 | 消费的 bald 组件（已核实存在） | 验证点 |
|---|---|---|
| 全部 CRUD | `contrib/store-gorm` `NewGormProvider[T](db, keyOf)`；多租户在核心 `pkg/store/tenant.go`（`RegisterTenant("tenant_id", store.DefaultTenantFunc)` + 写路径 `injectWriteTenant`） | PG 真实读写、Filters 算子翻译、租户自动隔离 |
| 租户 | 同上 + `pkg/contextx` 五键 | 业务租户 CRUD 与 P8 数据隔离双层语义 |
| 字典 | `contrib/cache-redis`（`New/Get/Delete/Key`，键含租户维度） | Cache-Aside 命中/失效、Redis 故障降级直连 store |
| 文件 | `oss/minio`（`NewStorage(&Config{Endpoint,AccessKey,SecretKey,Token,UseSsl})` + `PutObject/GetObject/SDK()`） | 真实上传下载、SHA256 content_hash、MIME 白名单 |
| 角色/权限 | `contrib/authz-casbin`（`New(policyCSV)` / `NewWithModel`，RBAC 模型内嵌） | 策略从 PG 装载、REST/gRPC 同源归一化（P9） |
| 审计 | `pkg/audit` + `internal/security/audit`（StoreAuditor→PG / StreamAuditor→Redis `XAdd` `audit.events`）+ `audit.backends` 期望态热切换 | 三后端真实落盘、查询 API |
| 认证 | `contrib/authn-jwt`（存量 RSA 生成 + Signer/Authenticator 双实例） | 对接真实用户表 + 租户 claims |
| 注册发现 | `contrib/registry/nacos`（`New(WithServerAddrs/WithNamespace/WithGroup/...)`）+ 契约 `contract.Provider(ctx, *bootstrapv1.Registry)` | 服务注册/注销/发现真实生效 |
| 遥测 | `contrib/observability-otlp`（trace `Setup(WithOTLPAddr)` / metrics `Setup`，env `BALD_ADMIN_OTLP_ADDR`） | 指标+trace 直推云端 collector |
| 横切 | `pkg/middleware/{gin,grpc}`（Authn/Authz/Audit/Observability）、`appkit.Reconcile`、wire | 中间件链对新模块自动生效 |

## 4. 云端真实依赖清单（待用户确认/提供）

> **以下 5 项由用户在云端启动，连接信息填入 `configs/go-bald-admin.yaml`（见 §5）。**
> 请逐项确认：能提供 → 给出连接参数；缺失 → 标注，对应里程碑降级或推迟（见「缺失影响」）。

| # | 依赖 | 版本要求 | 用途 | 需要的连接参数 | 缺失影响 |
|---|------|---------|------|---------------|---------|
| 1 | **PostgreSQL** | 14+ | 用户/租户/角色/菜单/字典/文件/审计全部落库 | host、port、user、password、dbname、sslmode | **必须**。T1 起全部阻塞 |
| 2 | **Redis** | 7.x 可用（源项目要求 8.0+，bald 侧用标准 go-redis 命令，无 HExpire 硬依赖） | 字典 Cache-Aside + 审计 Stream | addr、password、db | **建议提供**。缺失则 `audit.backends` 去掉 `stream`、字典缓存禁用（cache-redis 空 addr 即禁用态，行为已内置） |
| 3 | **MinIO**（S3 兼容） | RELEASE.2024+ | 文件上传/下载 | endpoint、access_key、secret_key、bucket、use_ssl | 仅阻塞 T5；可最后提供 |
| 4 | **Nacos** | 2.x | 服务注册/发现 | server_addrs、namespace、group | 仅阻塞 T7；缺失期间保持 inmemory registrar |
| 5 | OTLP Collector（Jaeger/Tempo/VictoriaMetrics 等支持 OTLP 的任一） | 1.40+（Jaeger） | 指标 + trace 直推 | addr（`host:port` 或 `http(s)://`） | 可选。未提供则维持现状（Prometheus 本地 + no-op trace），零影响 |

**需用户反馈的确认项**：① 各项是否可用；② Postgres 是否已建库（库名建议 `bald_admin`，
AutoMigrate 自建表，无需手工 DDL）；③ Redis 是否有密码/独立 db 号；
④ MinIO 桶是否预建（代码侧 `MakeBucket` 兜底，若账号无建桶权限请预建）。

## 5. 配置文件设计

`configs/go-bald-admin.yaml` 在现有 server/gateway/log/audit 四段基础上**新增真实依赖段**。
键名对齐 bconf 契约 `bconf/proto/bootstrap/v1/bootstrap.proto` 已就绪的
`database.sql` / `cache.redis` / `storage.minio` / `registry.nacos` 段（proto 为配置唯一真相源）；
云端地址由用户填写（下表 `<...>` 占位）：

```yaml
server:
  http: { addr: ":8080" }
  grpc:  { addr: ":9090" }
gateway: { addr: ":8081" }
log: { level: "info", format: "json" }
audit: { backends: "store,stream" }   # Redis 可用才含 stream

# ---- T0 新增：真实服务依赖（云端地址由用户填写）----
database:
  sql:
    driver: "postgres"
    source: "host=<PG_HOST> port=5432 user=<PG_USER> password=<PG_PASSWORD> dbname=<PG_DB> sslmode=disable"
    migrate: true
cache:
  redis:
    addr: "<REDIS_HOST>:6379"
    password: "<REDIS_PASSWORD>"   # 无密码留空
    db: 0
storage:
  minio:
    endpoint: "<MINIO_HOST>:9000"
    access_key: "<MINIO_ACCESS_KEY>"
    secret_key: "<MINIO_SECRET_KEY>"
    use_ssl: false
registry:
  type: "nacos"
  nacos:
    server_addrs: ["<NACOS_HOST>:8848"]
    namespace: "<NAMESPACE>"       # 可留空
    group: "<GROUP>"               # 可留空

# ---- 业务自持段（storage.minio 契约无 bucket 字段，file 模块自持）----
file:
  bucket: "bald-admin"
```

**装配落点**（均已核实的机制，不引入新框架）：

- 配置加载走既有 appkit 四源（flag > env > 文件 > 远程）；`cmd/go-bald-admin/main.go`
  BeforeStart 的 `baldconfig.Unmarshal(m, bootstrap)` 扩展：`internal/bootstrap` 配置结构体
  新增 `Database / Cache / Storage / Registry / File` 字段承接上述段落。
- `openDB()` 保留：DSN 优先取 `database.sql.source`（新增），env `BALD_ADMIN_DB_DSN` 保留为覆盖手段
  （优先级 env > 配置文件，便于 CI 注入）；`dsnScheme()` 的 postgres/mysql/sqlite 分流已有。
- MinIO 构造注意：`oss/minio` 构造失败返回 nil client + 日志（不 panic），bootstrap 必须判 nil 降级。
- OTLP 维持 env `BALD_ADMIN_OTLP_ADDR` / `BALD_ADMIN_METRICS_ADDR` 机制不变。

## 6. 移植范式

每模块统一按既有 apiserver 分层落地（范式参照 `internal/apiserver` 现有 auth/secret 模块）：

```
proto/<域>.proto（从源项目精简搬运）→ buf generate → gen/
  → model/model.go 加 GORM 实体（表结构按源 Ent schema 手工映射）
  → internal/bootstrap：AutoMigrate 注册 + store.Store[T] 包级实例
  → biz/v1/<mod>/（纯 Go 出入参，不依赖传输层）
  → handler/gin/<mod>.go（+ 可选 grpc/<mod>.go），apiserver/server.go RegisterRoutes 挂入
  → wire.go BizSet 加 provider → wire gen
  → casbin 策略行 + e2e 测试（真实 gRPC 连接，范式 internal/apiserver/grpc/secret_e2e_test.go）
```

**关键决策**：

- **D1 存量融合**：存量 `User/Role/Secret/AuditRecord` 实体保持不变（守护回归测试），
  移植模块一律新增实体/表；租户字段统一 `tenant_id`（对齐 P8 `RegisterTenant` 键）。
- **D2 多租户双层语义**：① 框架层——P8 自动注入/过滤不变；② 业务层——租户 CRUD
  （源 `sys_memberships` 模型简化为 `Tenant` 实体 + `platform` 平台租户，对应源 `PlatformTenantID=0`）。
  租户管理接口自身走平台租户上下文，不受本租户过滤劫持（biz 层显式旁路，见 `store` 的 `Where.T` 副本语义）。
- **D3 casbin 策略数据化**：不引入 gorm-adapter（contrib/authz-casbin 是 string-adapter 纯内存，
  保持零改动）。新增 `RolePolicy` 实体（role/object/action/effect，语义对齐源
  `sys_role_permissions`），启动期从 PG 读取拼装策略串经 `NewWithModel` 装载；
  `rbac_policy.csv` 保留为种子兜底。源项目的 casbin/OPA/noop 引擎可插拔机制不移植（YAGNI）。
- **D4 proto 精简搬运**：源 proto 的完整 RPC 面（每服务 Create/Update/Delete/Get/List/Batch…）
  搬运时裁剪为「Get/List/Create/Update/Delete」五个基础方法 + 领域特有方法（如文件预签名），
  google.api.http 注解保持，经既有 buf 流程生成（`proto/buf.gen.yaml`）。
- **D5 种子数据**：对齐源项目 Go 代码种子（`pkg/constants/default_data.go`，惰性表空则种）：
  平台 admin（源默认 `admin/Abcd@1234`，与存量 `admin123` 并存或统一，T2 定夺）、
  默认租户、平台角色、菜单树、字典演示组（源 `postgresql-demo-data.sql` 的 5 组类型 + 18 条条目）。
- **D6 审计增强**：`AuditRecord` 扩展 IP/UserAgent/ActionType 字段；操作/登录分类由
  bald 中间件既有 AuditEvent 归一化结果（P9 object/action）+ biz 层方法名推导；
  log_hash/ECDSA 签名、SQL 层 data_access 审计列入后续迭代（§2.2）。

**已核实缺口（移植时需处置，非阻塞）**：

| 缺口 | 事实 | 处置 |
|---|---|---|
| cache-redis 无 password/db | `contrib/cache-redis/redis.go` 仅 `New(addr string)`，TTL 固定 5min | 扩展构造加 Option（`WithPassword/WithDB`，最小改动），或业务侧自持 `redis.Options` 构造后 `NewWithClient` 式接入；审计 Stream 复用 `Client()` 不受影响 |
| storage.minio 契约无 bucket | `bconf` `storage.proto` 的 Minio 段无 bucket 字段 | 业务自持 `file.bucket` 配置段（§5），不经契约层 |
| registry/nacos 未接线 | `main.go` 现为 `appkit.Registrar(inmemory.New())`；nacos-sdk-go/v2 仅 indirect | T7 按契约装配替换（`RegistrarRegistry` + `nacoscontract.Provider`，参照 `_example/bald/register_nacos.go`，但 go-bald-admin 为独立 module 直接引依赖，无需 build tag 隔离） |

## 7. 里程碑

> 验收基线一律为：`task build`（go build ./...）+ `task test`（go test -shuffle=on ./...，存量全绿）
> + 本阶段接口验证序列。每阶段结束更新本表状态与 README 里程碑表。

| 阶段 | 范围 | 涉及文件（落点） | 验收标准 | 依赖就绪 |
|------|------|----------------|---------|---------|
| **T0** ✅ 依赖确认+配置骨架 | §4 清单用户反馈；yaml 新增段落 + bootstrap 配置结构体扩展 + openDB 读 `database.sql`；cache-redis password/db 缺口处置 | `configs/go-bald-admin.yaml`、`internal/bootstrap/bootstrap.go`、`contrib/cache-redis/redis.go`（如走扩展路线）、`cmd/go-bald-admin/main.go` | 配置加载单测；无云端地址时行为与现状一致（SQLite+禁用缓存），有地址时真实连接 | 无 |
| **T1** ✅ PostgreSQL 接入 | 存量 User/Role/Secret/AuditRecord 迁 PG（AutoMigrate）；种子改 PG；`db_e2e_test` 补 PG 分支（env 注入 DSN 才跑） | `internal/bootstrap/{bootstrap.go,db 相关}` | `task test` 全绿（PG 就绪时含 PG 路径）；`/v1/ping`、登录、secret CRUD 在 PG 上回归 | ① |
| **T2** ✅ 租户+用户 | proto 精简（identity 域）→ `model.Tenant` + User 字段扩展（tenant_id/nickname 等）→ biz/handler → 种子（平台租户+默认租户+admin） | `api/{tenant,user}/v1/*.proto`、`model/`、`biz/v1/{tenant,user}/`、`handler/gin/{tenant,user}.go`、`apiserver/grpc/{tenant,user}.go`、wire.go | 租户 CRUD e2e；跨租户用户隔离 404（复用存量隔离测试形态）；存量 secret/多租户测试不回归 | ① |
| **T3** ✅ 角色/权限/菜单 | `model.{Permission,Menu,RolePolicy}` + D3 策略数据化装载 + 菜单树 CRUD + RBAC 行为验证（viewer 删资源 403） | `api/{menu,permission}/v1/*.proto`、`model/`、`biz/v1/`、`internal/security/casbin/casbin.go`（装载入口） | 策略从 DB 装载（loadPolicyCSV）；REST/gRPC 同源授权 e2e（P9）；无策略时拒绝默认生效（fail-closed 单测） | ① |
| **T4** ✅ 字典 | `model.{DictType,DictEntry}` + Cache-Aside（键 `rediscache.Key("dict", tenant, type)`，写穿透失效） | `proto/dict.proto`、`biz/v1/dict/`、`handler/` | 缓存命中/失效 e2e（真实 Redis）；Redis 停机降级直连 store 验证 | ①② |
| **T5** ✅ 文件 | `model.File` + oss/minio 上传/下载（MIME 白名单、SHA256、大小上限 50MiB，语义对齐源 `file_transfer_service.go`）+ bucket 兜底创建 | `proto/file.proto`、`biz/v1/file/`、`handler/`、`internal/bootstrap`（MinIO 构造判 nil） | 上传→下载内容一致 e2e（真实 MinIO）；非白名单 MIME 拒绝；对象落桶可查 | ①③ |
| **T6** ✅ 审计增强+查询 | AuditRecord 扩展（IP/UA/RequestID/TraceID/Category）+ 操作/登录分类落库 + 分页查询接口 | `model/`、`internal/security/audit/store.go`、`biz/v1/auditlog/`、`api/audit/v1` | 写路径触发审计落 PG（含新字段）；查询 e2e；`audit.backends` 热切换回归 | ①（②可选） |
| **T7** ✅ Nacos | 契约装配：`RegistrarRegistry` + `registry.nacos` 段 → 替换 `appkit.Registrar` | `cmd/go-bald-admin/main.go`、`internal/bootstrap` | Nacos 控制台可见服务注册/注销；不可达时启动明确报错（不静默） | ④ |
| **T8** ✅ 端到端验证+收尾 | §9 全序列跑通（揪出 2 个 e2e 盲区 bug）；README 全面修正（env/端口/架构/目录）；metrics 端口根治；后续迭代清单冻结 | `README.md`、`main.go`、`t3_e2e_test.go`、`file.go`、本文档状态列 | §9 验证序列通过（OTLP 云上报留 T9，collector 不可达）；`task verify` 全绿 | 全部 |
| **T9** OTLP 云上报（预留） | §9 第 11 步完整验证：指标 + trace 直推云端 collector（`:4318` 就绪后启动，见 §8.7 遗留） | `cmd/go-bald-admin/main.go`（如需接线调整） | 云端 backend 可见 go-bald-admin 的 metrics 与 trace；`task verify` 全绿 | ⑤ |
| **T10** ✅ 目录架构对齐（分层参考 miniblog，2026-09-10 完成） | ① `internal/apiserver/grpc/` → `internal/apiserver/handler/grpc/`：gRPC service 归位协议接入层，与 `handler/gin` 对称，清空目录残留（含 `secret_e2e_test.go` 随迁；Go 代码仅 `main.go` 一处 import）；② 包根 9 个 `*_e2e_test.go` → `internal/apiserver/e2e/` 独立测试包：仅依赖导出符号（`RegisterRoutes`/`bootstrap.*`/`biz.New` 均已导出），测试间共享 helper（tinyServer/stubComp/issueToken 等）随包同迁；③ `RegisterRoutes` 10 个 biz 参数收敛为 `*BizSet` 直传：吸收 miniblog IBiz 聚合门面思路但不引入接口层/mockgen（与 §0 契约一致），新增域改动点 3→2，与 ② 联动改测试内调用点 | `internal/apiserver/{server.go, handler/, e2e/}`、`cmd/go-bald-admin/{main.go, wire.go, wire_gen.go}`、`docs/设计文档.md` 与本文档的 grpc 路径引用、README 目录段 | 纯重构零行为变化；`task verify` 全绿（存量 e2e 不回归）；gRPC 直连 + gateway 转码抽查通过；Taskfile `test:audit`/`test:file` 路径核对（`./internal/apiserver/...` 通配已自动覆盖新目录） | 无 |
| **F1** ✅ store-gorm Open 装配上提（框架演进，2026-09-10） | contrib/store-gorm 新增 `Open()` 装配函数 + `DialectorFactory` 注册表（sqlite 预注册为缺省引擎——glebarez 纯 Go 零 CGO；postgres/mysql 由业务侧 import driver 后显式 `RegisterDialector`，未 import 的后端零依赖，与 T7 nacos 注册制同模式，未注册 fail-fast）+ 五 Option（`WithEnv`/`WithDSN`/`WithDriver`/`WithConfig`/`WithGormConfig`）+ `NewTestDB` 测试助手（t.Cleanup 关连接池，防 Windows 句柄坑）；契约段连接池参数（max_idle/max_open/lifetime）由框架消费。示例 `openDB` 收敛为薄封装（仅保留 env `BALD_ADMIN_DB_DSN` + 契约段来源约定），`dsnScheme` 与 driver 分流逻辑上提框架。动机：快速开发不引真实依赖＝换轻量真后端（SQLite 内存库）而非 mock，与 §0 契约一致；「零依赖 clone 即跑」从示例手工逻辑升为框架缺省能力（与 cache-redis 空 addr 禁用、MinIO nil 降级同构的"真实但可选"） | `contrib/store-gorm/{conn.go, conn_test.go, testutil.go}`、`examples/go-bald-admin/internal/bootstrap/{bootstrap.go, db_e2e_test.go}` | contrib 单测 11 例全绿（缺省内存库/env 覆盖/DSN 优先级/注册制别名/fail-fast/空 Source 忽略）；示例 `task verify` 全绿；真实云端 PG e2e 回归（`TestDictREST` 等失败经 stash 对照实验证明为共享库存量数据问题非本次回归，T8 演示残留 `t8_probe` 已清理，其余审计/租户注入失败在原代码同样复现） | 无 |

## 8. 风险与决策记录

### 8.0 实施记录（2026-09-07，T0/T1 完成）

- **云端依赖全部接通**（PG 10.82.138.249:31068 / Redis :30967 db8 / MinIO 10.82.69.251:30658 / Nacos 待 T7 接线）；
  冒烟通过：双用户登录（RS256）、PG 真实读写删、跨租户 404、RBAC 403、Cache-Aside 真实 Redis。
- **处置的两个存量时序 bug（与 M10.1 Authenticator 构造期 nil 同款，e2e 不走 main 装配路径故未暴露）**：
  ① wire `provideSigner()` 构造期把 nil Signer 快照进 auth Biz → 新增 `bootstrap.LazySigner()` 修复；
  ② `secret.New()` 构造期快照 nil `SecretStore` → 改为请求期 `b.store()` 解析。**约定：biz 层引用
  bootstrap 包级桥接一律请求期读取，禁止构造期快照**（T2+ 新模块必须遵守）。
- SQLite driver 换纯 Go `github.com/glebarez/sqlite`（本机/CI 无 gcc，gorm.io/driver/sqlite 的 cgo 依赖导致回退路径整体失效）。
- **metrics 与 gRPC 端口冲突**（默认都 `:9090`，README 已知"巧合"坑）：本机运行须设
  `BALD_ADMIN_METRICS_ADDR=:9091`，T8 收尾时改默认值根治。
- `grpc_auth_e2e_test.go` issueToken 修复 shuffle 顺序耦合（幂等 InitBridges 前置）。

### 8.3 T4 实施记录（2026-09-07，字典完成）

- **实体精简映射**：`DictType.ID` = 源 `type_code`（immutable 业务键做主键，同
  Menu/Permission 范式）；`DictEntry.ID` = 业务键 `<type_code>:<entry_value>`
  （源「同租户同类型 entry_value 唯一」约束的等价实现，type_code/value 不可变，
  Update 仅改展示属性）；源 `sys_dict_entry_i18n` 不移植（§2.2 多语言后续迭代），
  显示标签内联 `Label`（zh 条目）；`Numeric` 保留可空数值（proto3 optional → *int32）。
- **字典是租户级业务数据**（源 mixin TenantID 保留）：P8 自动隔离 +
  Cache-Aside 键含租户维度 `dict:entries:<tenant>:<typeCode>` 防跨租户缓存泄漏；
  种子（3 组类型 8 条条目）归属 t-default。缓存按类型聚合条目 JSON 数组，
  写穿透失效（Create/Update/Delete 后 Delete 键，失效失败静默 TTL 兜底）；
  `type_code` 为空的全量列表直连不缓存。
- **顺手修复 contrib/cache-redis 降级语义（非本计划范围，T4 验收触发）**：
  `Get` 对 Redis 非 Nil 错误（连接失败/超时）降级直连 loader——此前故障直接
  报错，会把缓存层问题放大为业务 5xx；降级路径回填尽力而为（Set 失败静默）。
  miniredis `Close()` 模拟停机单测锁定；存量命中/失效/禁用单测零回归。
- **e2e 验收形态**：`startDictREST` 以 miniredis 真实例注入 dict biz（不走
  wire/env），断言升级为「缓存键真实存在性」：读后键回填可观测、写后键消失、
  再读重载新值；另覆盖 viewer 只读/写 403（dict_type/dict_entry 策略）与
  t-other 用户 P8 隔离空列表。Redis 停机降级由 contrib 单测覆盖（e2e 不动真
  实云端 Redis）。
- **顺带**：菜单种子加 `menu-dict`（menu-system 第 6 子节点，T3 e2e 树断言
  5→6 同步）；miniredis 升 v2.39.0；gateway 注册 dict 双 service handler。

### 8.4 T5 实施记录（2026-09-07，文件完成）

- **实体精简映射**：`File.ID` = 独立 uuid（带横线 `uuid.NewString()`，源 recordFile
  同款——与保存名 uuid 各自独立生成）；`SaveFileName` = 对象键去扩展名的**去横线**
  uuid32 + 扩展名（对象键重建用 `FileDirectory + "/" + SaveFileName`）；
  `FileName` 保留**原始上传名**（下载 `Content-Disposition` 友好，优于源用 uuid 名）；
  源 `FileGuid` 字段省略（ID 即其等价物）。UUID 用 `google/uuid` 替代 tx7do
  `id.NewGUIDv4`（tx7do 已解耦）。
- **ossutil.go 自源 `pkg/oss` 精简移植**（D4 只留上传链路）：`MaxUploadSize=50MiB`、
  MIME 白名单（`image/` `video/` `audio/` 前缀 + pdf/zip/office 等 exact）、
  `DetectFileType`（http.DetectContentType + 10 组魔术头优先，**嗅探结果为准，
  扩展名伪装无效**）、`ContentTypeToBucketName`（image/video/audio→同名桶，
  text+文档→docs，其余→files）、`IsFileDirectorySafe`（白名单字符 + 拒 `..`/绝对路径）。
  不移植：URL 拉取链路（`MaxDownloadSize`/SSRF 防护）、HMAC/时间戳文件名策略、
  预签名 URL 工具（`JoinObjectUrl`/`ReplaceEndpointHost`，随预签名功能后续迭代）。
- **坑 1——EnsureFileExtension 返回格式不统一导致丢点**：`ExtractFileExtension`
  返回不含点（源语义）而 `ContentTypeToFileExtension` 返回含点，`SaveFileName =
  base + ext` 拼出 `uuidpng`。统一为 **EnsureFileExtension 恒返回含前导点**
  （`".png"`），兜底 `".bin"`；e2e 断言 `save_file_name` 为 `<uuid32>.png` 格式锁定。
- **坑 2——tenantToken 直接签 claims 与 casbin subject=userID 的组合约束**：
  e2e 里 t-other 想要 admin 权限时不能凭空签 `u-admin2`（casbin g 分组来自
  **seed users 的 Roles 字段**，非 claims.Roles），必须用真实种子用户（t-other
  只有 u-bob/viewer）。T5 隔离测试改为读方向验证：t-default admin 上传 →
  t-other u-bob Get 404 + 前缀列表 total=0。
- **坑 3——REST 下载是流式端点（原始字节）非 JSON**：e2e 断言不能用
  protojson 解码（得空 content），直接字节级比对 + `Content-Type` /
  `Content-Disposition` 头检查。
- **上传 REST 双通道**：gin 主服务 `POST /v1/file/upload` 走 **multipart form**
  （file + directory 字段）；gateway 转码通道（:8081）走 proto `bytes`（base64
  JSON）。gRPC 直连同样 bytes 承载。桶兜底创建：`SDK().BucketExists` →
  `MakeBucket(minio.MakeBucketOptions{})`（R4 处置落地）；`file.bucket` 配置
  消费为 **files 类兜底桶名**（空 = 源默认 `"files"`），image/video/audio 类仍按
  源分桶。
- **MinIO 桥接判 nil**（T5 计划要求）：`storage.minio` 段缺失时
  `MinioStorage=nil`，biz 三个对象方法开头判 nil 返回
  `berrors.Internal("file/storage_unavailable")`——服务可启动、错误语义明确
  （不 panic、不静默）。文件是租户级业务数据：P8 自动注入/隔离 TenantID，
  `CreatedBy` 取 `contextx.UserIDFromContext`。
- **e2e 验收**（`t5_file_e2e_test.go`，真实 MinIO env 注入 `BALD_ADMIN_TEST_MINIO_*`，
  无 env 整组 Skip 不引入 fake）：PNG（真实字节流）上传→元数据（images 桶/
  image/png/SHA256/大小）→SDK StatObject 落桶可查→下载字节一致→删除后对象与
  元数据一并消失；exe 魔术头 400、目录穿越 400、50MiB+1 400；P8 隔离（读方向）；
  viewer 下载 200/上传删除 403。Taskfile 增 `test:file`（云端 MinIO env 预置）。
- **顺带**：T3 菜单树断言 6→7（`menu-file` 入树）；gateway 注册 file handler；
  存量 e2e 的 `RegisterRoutes` 调用补第 9 参 `filebiz.New(nil, "")`。

### 8.5 T6 实施记录（2026-09-07，审计增强+查询完成）

- **单表 + Category 分类**（源双表精简）：源 OperationAuditLog/LoginAuditLog
  两张表合并为 `AuditRecord` 单表 + `Category` 列（`operation`=拦截器链事件 /
  `login`=登录动作）。Success 由 Result 推导不冗余存储；`device_info` 深度解析、
  BeforeData/AfterData 变更快照、LogHash 合规链、风险评分/MFA 精简省略（后续迭代）。
- **AuditRecord 新列**：`Category`（兜底 operation）、`IPAddress`、`UserAgent`、
  `RequestID`、`TraceID`——`StoreAuditor.Record` 从 `ev.Meta` 提取（metaString
  安全取值）；gin 审计中间件（bald 公共包）Meta 补 `user_agent`（client_ip 原有）。
- **登录审计**（源 login_audit_log 语义）：`Credential` 加 `ClientIP/UserAgent`
  （handler 层 `c.ClientIP()`/`c.Request.UserAgent()` 提取，biz 保持协议无关）；
  Login 三分支（成功 allow / 凭据错 deny+原因 / 内部错 error）显式记
  `category=login` 事件，Object/Action 用 P9 归一化 `"auth"/"login"`；失败分支
  主体记账号名（用户不存在时也是有效线索），TenantID 留空。经全局
  `audit.GetAuditor()`（audit.backends 热切换同一入口），旁路不阻断（recordSafely
  recover 兜底）。
- **坑——e2e 解码**：protojson 输出 Timestamp 为 RFC3339 字符串，标准
  `encoding/json` 解码 `timestamppb.Timestamp` 字段直接报错，必须用项目
  `decodePB`（protojson）helper。审计查询 e2e 一开始用 json.Unmarshal 全线
  build-fail 级失败，换 decodePB 即过。
- **坑——审计查询不做租户读隔离**：审计是跨租户留痕（P8 的 `Where.T` 刻意
  不注入），查询侧访问控制由 casbin 策略承担（audit 对象 admin 专属）。这与
  文件/字典等租户业务数据形成对照。
- **坑——gRPC service 命名须对齐 P9 归一化**：REST 路径定 `/v1/audit`（对象
  "audit"），gRPC service 相应命名 `AuditService`（`DefaultGRPCObject` 去掉
  Service 后缀小写 → "audit"，若叫 AuditLogService 会归一化成 "auditlog" 导致
  casbin 策略双写）。
- **主服务挂载 gin AuditMiddleware**（T6 补缺口）：此前审计只在 gRPC 拦截器链
  生效，gin 侧 REST 写路径无审计。main.go 在 `ginBundle.Gin()` 后全局
  `Use(ginmw.AuditMiddleware(P9 归一化选项))`（wrap 型旁路，subject 由链内
  AuthnMiddleware 注入后可读）。与 biz 层 login 审计并存：login 请求本身产生
  operation 审计（access 语义）+ biz 显式 login 审计（业务语义），不冲突。
- **查询接口**：`AuditService`（ListAuditRecords/GetAuditRecord）只读——源
  Create rpc 是内部补录口，精简省略（伪造审计违背不可抵赖语义）。过滤
  category/subject/object/action/result/ip 精确匹配 + `time` 降序；分页
  page_size（默认 50 上限 200）+ page_token（offset 十进制编码，非法 400），
  `next_page_token` 空串即末页。复用 `store.Where` 的 Offset/Limit/Sorting。
- **e2e**（`t6_audit_e2e_test.go`，真实 SQLite 内存库 + StoreAuditor 落库，
  零外部依赖）：登录 deny/allow 双分支字段断言（IP/UA 独立列 + 失败原因）；
  operation 审计（object=tenant action=post 归一化）；分页三页翻完 + 非法
  token 400；单条详情 + 404；viewer 403。数据隔离技巧：各用例用唯一
  username 作过滤条件，规避全局 auditor/SQLite 跨用例累计。
- **热切换回归**：登录审计走全局 `audit.GetAuditor()` 与 `audit.backends`
  同源；reconcile_audit_test 既有热切换测试全绿；bald 公共包
  `pkg/middleware`/`pkg/audit` 回归全绿（gin 中间件加 user_agent 向后兼容）。



- **D3 策略数据化落地**：静态 `rbac_policy.csv` 删除，p 行落 `RolePolicy` 表
  （主键=业务键 `role:object:action`，Create 冲突天然防重）、g 行由 `User.Roles`
  装载——`bootstrap.loadPolicyCSV` 读库拼 csv 注入 contrib casbin（改库重启生效；
  运行期热重载列后续迭代）。策略单一真源收敛到 DB。
- **fail-closed 验收**：contrib casbin `NewWithModel` 容错空策略文本（此前
  stringadapter 对空串报 invalid line）——空策略=无策略 enforcer=全拒绝；
  单测锁定（contrib + 示例双处）。
- **策略行动作六元组**：REST 归一化为 HTTP 动词小写（`DefaultHTTPAction`），
  gRPC 为 get/list/write（`DefaultGRPCAction`，Create/Update→write）——双协议
  全放行需 get/post/put/delete/list/write 六行（与 T2 tenant/user 策略行同构）。
- **菜单树**：`model.Menu` 自引用（ParentID 空串=根，替代源 uint32 parent_id=0 的
  「零值即未设置」歧义），`Children []*Menu gorm:"-"` 内存嵌套；biz 层 BuildTree
  两遍 O(n)（孤儿跳过、各层按 Order 稳定排序——源项目 BuildTree 不排序靠 meta.order
  前端排，本实现在服务端排定输出）；删除级联子树（BFS 收集）。
- **顺手修复 contrib/store-gorm 缺陷（非本计划范围，T3 触发）**：`toMapExcludeKey`
  反射遍历导出字段不解析 gorm tag，`gorm:"-"` 字段被当列写进 Updates（SQLite 报
  no such column）——现跳过 `gorm:"-"`；测试实体补回归锁；顺带把测试 sqlite driver
  切 glebarez（无 gcc 环境 cgo 版失效）+ t.Cleanup 显式 Close（Windows TempDir
  文件锁，与 go-bald-admin 同款坑）。
- **RolePolicy 主键决策**：放弃 uint 自增（store.Eq 仅 string 值，且 PG bigint
  列对 string 参数有类型转换风险），改 string 业务键 `role:object:action`——与
  仓库「全实体 string 主键」范式一致，防重/删除语义更清晰。

### 8.6 T7 实施记录（2026-09-08，Nacos 注册发现接线完成）

- **装配形态（New 构造路径等价 buildRegistrar）**：main.go 删
  `appkit.Registrar(inmemory.New())`，改 `registrarRegistry()` 显式注册
  `nacoscontract.Type → nacoscontract.Provider`（未 import 的后端零依赖）；
  BeforeStart 在配置装载后 `regRegistry.Build(ctx, bootstrap.GetRegistry())`
  → `app.SetRegistrar(reg)`，cleanup 挂停机 Effect
  `appkit:registrar-client`（Deregister 先于 Effect 回放，顺序安全）。
  registry 段 nil/type 空/未注册均 fail-fast（与 FromBootstrap 同语义）；
  不支持热更新（client 重建侵入性大），变更需重启。
- **框架增量（bald/pkg/appkit）**：新增导出方法 `SetRegistrar`——New 构造
  路径在配置装载后按契约设置 registrar 的最小通道（FromBootstrap 由内部
  buildRegistrar 赋值，无需此方法）。register/deregister 每次读
  `a.registrar` 不做缓存，BeforeStart 赋值即时生效。单测
  `TestNew_SetRegistrarLifecycle` 锁定生命周期（register/deregister/cleanup
  恰好各一次）。
- **契约扩展（bconf registry.proto）**：`Registry_Nacos` 加 `username`/`password`
  （8/9 号字段）——云端 Nacos 开启 auth，gRPC 注册请求被 403 拒绝。
  contrib nacos 三层接线：options（WithUsername/WithPassword）→
  registry.New（ClientConfig 直填，gRPC 连接 setup 时登录）→
  contract.Provider（从契约段映射）。
- **坑①——namespace 必须填「命名空间 ID」而非显示名**：Nacos 控制台的
  "go-bald-admin" 是显示名，真实 ID 是 UUID；namespaceId 填显示名时注册
  落孤儿空间（服务定义可见、临时实例被清理机制剔除，控制台永远空）。
- **坑②——nacos-sdk-go v2 静默吞注册失败**：InstanceRequest 收到
  `{"errorCode":403,"message":"user not found!"}` 仅打 WARN（SDK 默认日志
  关闭，WARN 也看不到），`RegisterInstance` 向上层返回 nil——appkit 打出
  "appkit registered" 但服务端无实例。排查靠显式开 SDK debug 日志
  （ClientConfig.LogDir/LogLevel）看请求响应。**开 SDK 日志后的现状**：
  契约加凭据后注册真实落库（instance/list 返回 healthy=true 实例）。
- **坑③——`--config` flag 撞契约字段（存量 bug，T7 冒烟暴露）**：
  bootstrap/config `flattenFlags` 把所有 Changed flag 落树，而 bconf 契约
  顶层有 `Config *Config` message 字段——`--config=xxx`（string）落树后
  Unmarshal 触发 coerce 报错 `expect object for message field, got string`，
  服务无法用 --config 启动。修复：flattenFlags 排除引导 flag `config`
  （它由 loadConfig 自己消费，落树本就不合理）。
- **坑④——服务名带协议后缀（既有设计，对齐 go-wind）**：Register 按
  endpoint 逐个注册，服务名 = `name.scheme`（`go-bald-admin.grpc` /
  `go-bald-admin.http`，gateway 与 REST 同入 http 名）。验证/查询时按带
  后缀的服务名查，裸 `go-bald-admin` 恒为空（Nacos 对查询自动建空视图，
  勿被骗）。
- **验证（真实云端 Nacos 10.82.130.200:30000，HTTP 30000 / gRPC 31000）**：
  `task smoke:nacos`（cmd/probe 探针，跳过 main.go 完整启动链秒级完成）——
  契约路径注册 → 12s 心跳窗口（API 核对 grpc/http 双实例
  healthy=true/ephemeral=true/metadata kind+version）→ Deregister；
  main.go 装配路径 "appkit registered" 日志含三 endpoint；appkit/bootstrap
  /bconf/nacos 全模块 build+test 回归绿。
- **遗留（T8 已清）**：遗留实例与 T7 冒烟临时副本进程已清理，端口释放；
  README env/端口/架构/目录段已在 T8 全面修正（env 前缀实为 `GO_BALD_ADMIN_`，
  app.name 驱动：`GO_BALD_ADMIN_SERVER_HTTP_ADDR` ⇔ `server.http.addr`）。

### 8.7 T8 实施记录（2026-09-08，端到端验证+收尾完成）

- **§9 全序列执行结果（11 步）**：①ping 200（gateway `/v1/ping` 不在转码表属正常，
  转码面以业务路由为准）②登录 `admin/admin123` ✅（protojson 输出 `AccessToken` 非
  `token`）③secret 回归 ✅（种子 ID 实为 `s-db-pwd`/`s-api-key`/`s-other-pwd`，非文档
  示例 `secret-1`）④租户列表 + bob 跨租户 404 ✅ ⑤alice(viewer) 删资源 403 ✅
  ⑥字典 Cache-Aside 201/201/200 ✅ ⑦文件：**发现主链路 bug（见下 ②③），修复后
  终验闭环（上传→分桶→SHA256→下载一致）**
  ⑧审计新字段落库 ✅（`category=login`/`ipAddress=127.0.0.1`/`userAgent=curl`——
  jq 用 snake_case `ip_address` 查出 null 是 protojson lowerCamel 假阴性，非 bug）
  ⑨gRPC：`grpc` 包真调 e2e 绿 + gateway 转码 200（proto bind 为复数
  `/v1/secrets/{id}`，与 gin 自定义路由单数 `/v1/secret/:id` 并存是既定决策）⑩Nacos
  双实例（`go-bald-admin.grpc:9090`/`go-bald-admin.http:8081`）healthy=true ✅，停机
  注销待服务重启后终验 ⑪OTLP：云端 collector `:4318` 不可达（curl 000），按 §0
  不做假验证，完整上报验证留 T9。
- **T8 揪出的两个 e2e 盲区 bug（§9 全序列真调的价值）**：
  ① **t3 menu-x shuffle flaky**：`TestT3Authz_DataDrivenPolicy` 创建根菜单
  `menu-x` 不清理，与 `TestMenuREST_TreeAndLifecycle` 共享单例 store，`-shuffle=on`
  顺序不定时偶发 "expect 2 roots, got 3" → 加 `t.Cleanup` 删除修复；
  ② **file Biz 构造期快照 nil（违反 §8.0 约定的第三例）**：`InitializeBiz` 在 main
  早期值拷贝 `bootstrap.MinioStorage`/`FileBucket`（InitBridges 在 BeforeStart 才赋值）
  → 主链路上传必报 `file/storage_unavailable`；e2e 测试因 InitBridges 先行测不出。
  修复：file Biz 新增 `SetStorage`（对齐 appkit.SetRegistrar 的"构造期 nil + 运行期
  接线"模式），main.go 在 InitBridges 之后补注。**约定重申：biz 引用 bootstrap 包级
  桥接一律请求期读取或运行期 setter，禁止构造期快照**。
- **metrics 端口根治**：缺省 `:9090`（与 gRPC 同值仅"巧合"可运行，gRPC 先抢端口时
  metrics goroutine 只打一条 error 日志、指标静默丢失）→ 改 `:9091`，README 端口表
  同步。
- **README 全面修正**：运行段 env（`GO_BALD_ADMIN_` 三覆盖手段）、端口表、架构段
  （PG/策略数据化/文件/Nacos/审计热切换）、目录段（`api/` 收敛 + cmd/probe +
  契约驱动配置）。
- **§9 序列同步修正**：登录字段/种子 ID/路径风格按实测校正（见上）。
- **`task verify` 全绿**（apiserver/bootstrap/audit/casbin 全 ok）；main.go doc 注释
  中 viper 时代 `BALD_HTTP_ADDR`/`BALD_SERVER_HTTP_ADDR` 陈旧残留一并清理。
- **T8 收尾后剩余**：OTLP 云上报（T9，collector `:4318` 就绪后）。终验已闭环
  （2026-09-08）：文件链完整跑通（上传→`docs` 桶分桶→`contentHash` SHA256 一致→
  下载内容一致）；Nacos 生命周期完整（注册 healthy=true → SIGINT 优雅停机 →
  双协议实例列表清空，停机链 "stopping → 审计后端 unmount → appkit stopped"）。

### 8.8 T9 实施记录（2026-09-10，可观测性契约化完成；远端终验待 collector 地址）

- **契约化改造（替代 §5 的 env-only 决策）**：`tracer`/`metrics` 契约段
  （bconf proto 既有）从无消费者转为唯一主配置渠道。main.go 新增
  `setupObservability(bootstrap, obs)`，装配点从 serveRunE 顶部（env 读取）挪到
  **BeforeStart**（`baldconfig.Unmarshal` 之后）——Recorder/span 经 otel 全局
  Provider lazy 解析，中间件先行构建不漏采（Servers 监听晚于 BeforeStart，无采样
  窗口损失）；与 T7 registrar 同款「构造期 nil + 运行期接线」模式
  （`observabilityWiring{traceShutdown, metricsSrv}` 容器 + Effect/Component 闭包消费）。
- **contrib 增强（`bald-observability-otlp`，契约字段全有消费者）**：trace 包
  +`WithInsecure`（三态：nil=按地址推断/true=强制/false=强制 TLS）+`WithHeaders`
  （远端 APM 鉴权）+`WithSampler`/`WithSampleRatio`（always_on/always_off/
  trace_id_ratio/parent_based，未知值退化 ParentBased(AlwaysSample)+WARN）；
  metrics 包 +`WithInsecure`/`WithHeaders`，`WithInterval` 非正值忽略。
- **fail-fast 校验（T8 metrics 静默丢失教训的结构化）**：`type=otlp` 而 endpoint
  空 → 启动报错（`tracer.type=otlp requires tracer.otlp.endpoint`）；type 非法值
  同理。段缺省/endpoint 空保持零配置可运行（Prometheus 单通道 + no-op trace）。
- **契约收紧：显式主开关（2026-09-10，用户反馈"太灵活看不懂"）**：废除
  "type 空=宽容、endpoint 非空反推直推"的隐式行为。新语义：段整体缺省=零配置
  默认（no-op trace + 仅 `:9091` 暴露）；段存在则 `type` **必填显式声明**（空串
  启动报错 `tracer.type is required when tracer section is present`）；行为由
  声明决定——`tracer.type` 仅 `"otlp"`；`metrics.type` `"prometheus"`=仅暴露 /
  `"otlp"`=双通道（endpoint 必填）；`type=prometheus` 而 otlp endpoint（含 env）
  非空 → 矛盾配置报错。env 覆盖只提供地址、不改变 type 语义（配 prometheus
  不会因 env 翻转成推送）。contrib 层零改动（收紧只发生在 example 装配层）。
- **env 覆盖通道保留**（优先级 env > yaml，与 flag>env>本地文件一致）：
  `BALD_ADMIN_OTLP_ADDR` 覆盖双通道 endpoint、`BALD_ADMIN_METRICS_ADDR` 覆盖暴露
  端口——Taskfile 冒烟与 CI 既有用法不破。
- **metrics 暴露端点生命周期补全**：`/metrics` server（契约 `metrics.prometheus.addr`
  缺省 `:9091`、`path` 缺省 `/metrics`）从裸 goroutine 升为挂 `Effect("metrics:server")`
  优雅关闭；trace provider 维持 `Components(trace.provider)` 停机 flush。
- **冒烟验证（本地全真）**：契约段装载 → `:9091/metrics` 200 +
  `bald_requests_total{object="ping"}` 计数；SIGINT 停机链完整（deregister →
  stopping → 审计 unmount → stopped）；fail-fast 触发如预期；env 覆盖
  （`BALD_ADMIN_METRICS_ADDR=:19091` 监听+200）。`task verify` 全绿、contrib 两包
  测试全绿。
- **终验结果（2026-09-10 晚，云端真实 collector `10.82.138.249:32414`）**：
  - **trace ✅ 端到端闭环**——云端查询面（Jaeger，`msc-dce5.was.ink/tracing/search`）
    查到 service=go-bald-admin 三条 trace（`GET /v1/ping`×2 + `GET /v1/info`×1），
    时间戳与冒烟流量秒级吻合（21:45:55），各 1 span（gin 入口，无下游出站，符合预期）。
  - **metrics ⚠️ 客户端侧完成，云端核对顺延**——本地 `:9091` 抓取有数（全维度标签）
    + OTLP 推送跨 15s 周期 0 错误 + 探针 metric（`probe_otlp_metrics_pipeline`/
    service `otlp-probe`）推送 200 `partialSuccess`。平台仅有 Jaeger trace 查询面、
    无 metrics 查询入口，疑似 collector 未配 metrics exporter（接收 200 ≠ 入库）；
    待平台侧补 metrics 管线后核对 `bald_requests_total{service_name="go-bald-admin"}`。
  - **T0-T9 移植计划就此收官**（metrics 云端核对属平台配置事项，不阻塞范例交付）。

### 8.1 T2 实施记录（2026-09-07，租户+用户完成）

- **proto 全部收敛 `api/`**（用户指令）：buf 模块根= `api/`（原 `proto/`+`gen/` 删除），源 `api/{secret,tenant,user}/v1/*.proto`、生成物 `api/gen/<域>/v1/`；`protoc-gen-go-grpc`/`protoc-gen-grpc-gateway` 已 go install。lint 放行 `PACKAGE_DIRECTORY_MATCH`（语义包名不逐级对应目录）。
- **REST 路径单数决策**：`/v1/tenant`、`/v1/user`——P9 归一化要求 HTTP object（首资源段）与 gRPC object（service 名去 Service 小写）同源（`TenantService`→`tenant`），策略单写即覆盖双协议；复数路径会造成 `tenants`/`tenant` 双命名空间。
- **protojson 统一绑定/序列化**（`handler/gin/pb.go`）：gin 直连（:8080）与 gateway 转码（:8081）必须同一 JSON 语义——encoding/json 会把枚举输出数字、Timestamp 输出内部字段，违反 proto3 JSON 规范。**T3+ 新 handler 一律用 bindPB/writePB**。
- **ListUsers 迁移**：原 `SecretService.ListUsers`（string 列表演示 RPC，M3）由 `UserService.ListUsers`（对象列表）接管 `GET /v1/user`——避免 gateway 同 method+pattern 重复注册 panic；gRPC/REST 隔离测试同步迁移。
- **列表 total 用 uint32**：proto3 JSON 把 int64/uint64 编码为字符串，会破坏常规 JSON 客户端。
- **casbin 策略新增**：tenant 仅 admin（get/post/put/delete/list/write）；user admin 全权 + viewer 只读（get/list）。租户管理平台专属，biz 层保护 platform 租户不可删。
- **已知现象**：存量 seed 用户（u-admin/u-alice）的 created_at 为零值——T2 才给 User 加时间戳字段，PG 存量行为 NULL 迁移结果，不影响功能；新创建行正常。

| # | 风险/决策 | 处置 |
|---|----------|------|
| R1 | Ent→GORM 映射失真 | 以源 Ent schema 为唯一真源逐字段映射；`sql/*.sql` 仅作种子参考（demo 脚本已知过期，如 `sys_dict_entries.entry_label` 已迁 i18n 表，**不照抄**） |
| R2 | Redis 版本不足 8.0 | bald 侧无 HExpire 硬依赖，7.x 可用；报备用户即可 |
| R3 | cache-redis 桥改动影响存量 | Option 扩展保持 `New(addr)` 签名兼容；存量 secret 缓存行为回归测试守护 |
| R4 | MinIO 桶权限 | 业务启动 `MakeBucket` 兜底 + 显式日志；无权限则要求用户预建（§4 确认项④） |
| R5 | Nacos SDK 重依赖进入范例 module | 接受：范例即真实依赖验证载体（与 §0 契约一致）；bald 核心 go.mod 不受影响（P5 边界） |
| R6 | 多租户旁路滥用 | 租户管理 biz 显式命名（如 `ListAllTenants` 仅平台上下文可调），casbin 策略默认仅 `platform:admin` 角色 |
| R7 | 种子账号迁移冲击存量测试 | 存量测试断言基于 `admin/admin123`；T2 决策点：保留 `admin123`（存量不变）+ 新增源风格账号，或统一改密并同步测试——实施时按最小扰动原则定夺 |

## 9. 端到端验证方案

依赖就绪后（`configs/go-bald-admin.yaml` 填真实地址，`go run ./cmd/go-bald-admin` 启动）：

```bash
# 1. 健康/公开接口
curl -i http://127.0.0.1:8080/v1/ping

# 2. 登录（真实用户表 + JWT）→ 取 token（protojson 字段名为 AccessToken）
TOKEN=$(curl -s -X POST http://127.0.0.1:8080/v1/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | jq -r .AccessToken)

# 3. 存量回归：secret CRUD（PG；种子 ID：s-db-pwd/s-api-key/s-other-pwd）
curl -i http://127.0.0.1:8080/v1/secret/s-db-pwd -H "Authorization: Bearer $TOKEN"

# 4. 租户 CRUD（平台上下文）+ 跨租户隔离（bob 读 t-default 资源 → 404）
# 5. 用户 CRUD；alice(viewer) 删资源 → 403（RBAC 策略数据化验证）
# 6. 字典：POST /v1/dict_type → /v1/dict_entry → GET /v1/dict_entry?type_code=（缓存命中）
# 7. 文件：POST /v1/file/upload（multipart file+directory）→ GET /v1/file/:id/download → SHA256 核对
# 8. 审计：GET /v1/audit?category=login → 核对 category/ipAddress/userAgent（protojson lowerCamel 字段名）
# 9. gRPC 侧抽查 2~3 个服务（P9 归一化：REST 与 gRPC 同策略；gateway 转码面路径为 proto bind 复数 /v1/secrets/{id}）
# 10. Nacos 核对注册（服务名带协议后缀 go-bald-admin.grpc/.http）；kill 服务核对注销
# 11. 遥测：BALD_ADMIN_OTLP_ADDR 指向云端 collector，核对指标+trace 上报
```

回归守护：每阶段 `task verify`（build+vet+test）；存量 e2e（secret 多租户隔离、审计热切换、
网关转码）不得回归；新增虚假实现（fake/mock/硬编码返回值）视为违反 §0 契约的回归。
