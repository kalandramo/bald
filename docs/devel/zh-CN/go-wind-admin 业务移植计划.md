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

### 2.2 后续迭代清单（本次不做，防范围蔓延）

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
| **T5** 文件 | `model.File` + oss/minio 上传/下载（MIME 白名单、SHA256、大小上限 50MiB，语义对齐源 `file_transfer_service.go`）+ bucket 兜底创建 | `proto/file.proto`、`biz/v1/file/`、`handler/`、`internal/bootstrap`（MinIO 构造判 nil） | 上传→下载内容一致 e2e（真实 MinIO）；非白名单 MIME 拒绝；对象落桶可查 | ①③ |
| **T6** 审计增强+查询 | AuditRecord 扩展（IP/UA/ActionType）+ 操作/登录分类落库 + 分页查询接口 | `model/`、`internal/security/audit/store.go`、`biz/v1/auditlog/` | 写路径触发审计落 PG（含新字段）；查询 e2e；`audit.backends` 热切换回归 | ①（②可选） |
| **T7** Nacos | 契约装配：`RegistrarRegistry` + `registry.nacos` 段 → 替换 `appkit.Registrar` | `cmd/go-bald-admin/main.go`、`internal/bootstrap` | Nacos 控制台可见服务注册/注销；不可达时启动明确报错（不静默） | ④ |
| **T8** 端到端验证+收尾 | §9 全序列跑通；README/设计文档更新；后续迭代清单冻结 | `README.md`、`docs/设计文档.md`、本文档状态列 | 全部验证序列通过；`task verify` 全绿 | 全部 |

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

### 8.2 T3 实施记录（2026-09-07，角色/权限/菜单完成）

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

# 2. 登录（真实用户表 + JWT）→ 取 token
TOKEN=$(curl -s -X POST http://127.0.0.1:8080/v1/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<种子密码>"}' | jq -r .token)

# 3. 存量回归：secret CRUD（PG）
curl -i http://127.0.0.1:8080/v1/secret/secret-1 -H "Authorization: Bearer $TOKEN"

# 4. 租户 CRUD（平台上下文）+ 跨租户隔离（bob 租户 → 404）
# 5. 用户 CRUD；viewer 角色删资源 → 403（RBAC 策略数据化验证）
# 6. 字典：写 → 读（缓存命中，二次读 Redis 命中可观测）→ 改 → 失效验证
# 7. 文件：multipart 上传 → 预签名/下载 → MinIO 控制台核对对象与 SHA256
# 8. 审计：执行一次写操作 → 查询接口核对新字段（IP/UA/action_type）落库
# 9. gRPC 侧抽查 2~3 个服务（P9 归一化：REST 与 gRPC 同策略）
# 10. Nacos 控制台核对注册信息；kill 服务核对注销
# 11. 遥测：BALD_ADMIN_OTLP_ADDR 指向云端 collector，核对指标+trace 上报
```

回归守护：每阶段 `task verify`（build+vet+test）；存量 e2e（secret 多租户隔离、审计热切换、
网关转码）不得回归；新增虚假实现（fake/mock/硬编码返回值）视为违反 §0 契约的回归。
