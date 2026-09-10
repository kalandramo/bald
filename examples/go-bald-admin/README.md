# go-bald-admin

基于 [bald](../../) 重构 `go-wind-admin/backend` 的**官方参考范例**（验证 P0–P9）。

它用真实业务（用户/角色/机密、RBAC 授权、多租户隔离）跑通 bald 的核心能力：
认证（JWT）、授权（casbin RBAC，REST/gRPC 同源归一化）、审计（旁路不阻断）、
可观测性指标（Prometheus 本地 + OTLP 远端直推）——全部经 bald 中间件/桥接实现，业务不直连引擎。

> 设计见 [`docs/设计文档.md`](docs/设计文档.md)；需求见 [`docs/需求文档.md`](docs/需求文档.md)。
> 实现契约（外部依赖禁止 fake/mock/stub）见设计文档 §0。
>
> **与 bald codegen 的关系**：本应用的框架装配骨架（servers / capability / 审计协调 / 配置 flag 覆盖）
> 可由 `bald gen app --spec <AppSpec>` 起步生成同形骨架（样本见 [`_example/bald/_scratch/go-bald-admin.appspec.json`](../../_example/bald/_scratch/go-bald-admin.appspec.json)）；
> 业务层（`internal/` 分层、wire 注入、组件工厂、管理面）为手写扩展——生成器只做装配不做业务。
> 安装生成工具：`go install github.com/kalandramo/bald/cmd/bald@latest`

## 架构

- 双协议：`gin` HTTP(`:8080`) + `gRPC`(`:9090`)，`grpc-gateway` REST 转码(`:8081`)。
- 认证：`bald-authn-jwt`（RS256/ES256/HMAC，M6.5 非对称）。
- 授权：`bald-authz-casbin`（contrib，P11 晋升）桥接 casbin；T3 起策略数据化——p 行落 `RolePolicy` 表、g 行由 `User.Roles` 装载，启动期从 PG 读取（改库重启生效），REST/gRPC 同源归一化（P9）。
- 存储：`bald-store-gorm` 云端 PostgreSQL（T1；无外部 DB 时回退 SQLite 内存库），AutoMigrate 自建表。
- 多租户：P8 自动注入 `tenant_id`（M3/M4）+ T2 租户 CRUD 业务双层语义。
- 缓存：`bald-cache-redis`（contrib，P11 晋升）Cache-Aside（T4 字典真实 Redis；Redis 停机/空配置自动降级直连 store）。
- 对象存储：`bald/oss/minio`（T5 文件上传/下载，MIME 白名单 + SHA256 + 50MiB 上限 + 分桶）。
- 注册发现：`bald-registry-nacos`（T7 契约装配 `RegistrarRegistry`，服务名带协议后缀）。
- 审计：`pkg/audit` 核心 + `internal/security/audit.MultiAuditor`（M7 落库 + M9 Redis Stream 异步 + 日志降级，`audit.backends` 期望态热切换；T6 登录/操作分类 + 分页查询）。
- 指标/trace：`pkg/metrics` 核心 + `bald-observability-otlp`（Prometheus(`:9091`) + OTLP 双通道，M8/M9）。
- 装配：`google/wire` 接管业务对象，appkit 负责框架发现与契约驱动配置（bconf proto 为唯一真相源，M6.4）。

## 运行

```bash
# 从仓库根（bald 独立 module，replace 指向本地核心）
cd bald/examples/go-bald-admin

# 真实配置不入库（含云端凭证）：首次使用先从模板复制并填入连接参数
cp configs/go-bald-admin.yaml.example configs/go-bald-admin.yaml

# 起服务（:8080 HTTP / :9090 gRPC / :8081 gateway / :9091 metrics）
go run ./cmd/go-bald-admin --config=configs/go-bald-admin.yaml

# 覆盖地址（三种等价手段，优先级 flag > env > yaml）
go run ./cmd/go-bald-admin --server.http.addr=:18080
GO_BALD_ADMIN_SERVER_HTTP_ADDR=:18080 go run ./cmd/go-bald-admin
```

### 端口

| 端口 | 用途 | 覆盖手段 |
|------|------|----------|
| `:8080` | HTTP(gin) 主服务 | `--server.http.addr` / `GO_BALD_ADMIN_SERVER_HTTP_ADDR` / yaml `server.http.addr` |
| `:9090` | gRPC | `--server.grpc.addr` / `GO_BALD_ADMIN_SERVER_GRPC_ADDR` / yaml `server.grpc.addr` |
| `:8081` | grpc-gateway REST 转码 | `BALD_GATEWAY_ADDR`（独立 env） / yaml `gateway.addr` |
| `:9091` | `/metrics` Prometheus 抓取 | `BALD_ADMIN_METRICS_ADDR` |

> env 键名由 app name 规范化派生（`go-bald-admin` → `GO_BALD_ADMIN_` 前缀，
> 下划线即点路径分隔：`GO_BALD_ADMIN_SERVER_HTTP_ADDR` ⇔ `server.http.addr`）。
> T8 起 metrics 缺省端口已与 gRPC 错开（`:9091`），无需再手动避让。

### 远端遥测（T9 契约化：tracer/metrics 段驱动）

`configs/go-bald-admin.yaml` 的 `tracer` / `metrics` 段是远端 APM
（OTel Collector / VictoriaMetrics / Grafana Cloud）直推的**主配置渠道**——指标
（Prometheus 暴露端点 + OTLP 直推双通道）与 trace（OTLP 直推）一并驱动，核心埋点
（grpc/gin Observability 起 span、AuditWithMetrics emit）零改动：

```yaml
tracer:
  type: "otlp"                        # 段存在则 type 必填（空串启动期报错）
  otlp: { endpoint: "10.x.x.x:4318", insecure: true, sampler: "always_on" }
metrics:
  type: "otlp"                        # "prometheus"=仅本地抓取 / "otlp"=双通道
  prometheus: { addr: ":9091", path: "/metrics" }
  otlp: { endpoint: "10.x.x.x:4318", insecure: true, push_interval: 15 }
```

**显式主开关契约**（行为由 type 声明决定，不靠 endpoint 反推）：

- 段整体缺省 → 零配置默认：trace no-op + 仅 Prometheus `:9091` 本地抓取；
- 段存在 → `type` 必须显式声明：`tracer.type` 仅支持 `"otlp"`；`metrics.type`
  支持 `"prometheus"`（仅暴露）/ `"otlp"`（暴露+直推）——空串或未知值启动期报错；
- `type=otlp` 而 endpoint 空（含 env）→ 启动报错；`type=prometheus` 而 otlp
  endpoint（含 env）非空 → 矛盾配置启动报错。

裸 `host:port` 默认 insecure（内网 collector）；`insecure: false` 走 TLS；`http(s)://`
前缀按完整 URL 解析；`headers` 支持远端鉴权（如 Grafana Cloud Bearer token）。

env 覆盖通道保留（优先级 env > yaml，只提供地址、不改变 type 语义）：
`BALD_ADMIN_OTLP_ADDR` 覆盖双通道 endpoint、`BALD_ADMIN_METRICS_ADDR` 覆盖暴露
端口——Taskfile 冒烟与 CI 既有用法不破。

## 验证接口

```bash
# 健康检查（公开）
curl -i http://127.0.0.1:8080/v1/ping
curl -i http://127.0.0.1:8080/v1/info

# 登录拿 token（公开）→ 访问受限接口
TOKEN=$(curl -s -X POST http://127.0.0.1:8080/v1/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | jq -r .token)
curl -i http://127.0.0.1:8080/v1/secret/secret-1 \
  -H "Authorization: Bearer $TOKEN"
curl -i http://127.0.0.1:8080/v1/secret/secret-1 \
  -X DELETE -H "Authorization: Bearer $TOKEN"
```

测试账号：`admin/admin123`(角色 admin)、`alice/alice123`(viewer)、`bob/bob`(t-other 租户)。
viewer 删机密 → 403；跨租户查 `bob` 的机密 → 404（租户隔离）。

## 测试

```bash
# 用 Taskfile（推荐，跨平台）
task test          # go test -shuffle=on ./...
task test:metrics  # 仅可观测性包
task build         # go build ./...

# 或直接 go
go test -shuffle=on ./...
```

## 里程碑

| 里程碑 | 内容 | 状态 |
|--------|------|------|
| M0 | 脚手架（独立 module + appkit） | ✅ |
| M1 | 认证/授权（JWT + RBAC，gin+gRPC） | ✅ |
| M2 | 数据化 + 真实 gRPC service（SQLite 内存库） | ✅ |
| M3 | bcrypt + 多租户隔离 | ✅ |
| M4 | 写路径多租户注入 + 外部 DB 配置化 | ✅ |
| M5 | grpc-gateway REST 转码（buf 生成） | ✅ |
| M6 | 成熟库接入（casbin/redis/wire/JWT非对称/外部DB/gateway生产化/CR闭环） | ✅ |
| P9 | 核心授权归一化（反哺核心，根治双命名空间） | ✅ |
| M7 | 审计日志（传输中立 + 旁路不阻断；M9 延伸 `StoreAuditor` 落库 + `StreamAuditor` Redis Stream 异步 + `MultiAuditor` 组合，三重真实后端） | ✅ |
| M8 | 可观测性指标（Prometheus） | ✅ |
| M9 | OTLP 远端直推（指标 Prometheus+OTLP 双通道；trace OTLP 直推，核心埋点零改动） | ✅ |
| T0-T1 | 云端 PG 接入（含 SQLite 回退）+ 存量时序 bug 处置 | ✅ |
| T2 | 租户 + 用户（identity 域 proto 精简、跨租户隔离） | ✅ |
| T3 | 角色/权限/菜单（D3 策略数据化、P9 归一化落 RBAC） | ✅ |
| T4 | 字典（Cache-Aside 真实 Redis、停机降级） | ✅ |
| T5 | 文件（真实 MinIO：MIME 白名单/SHA256/50MiB 上限/分桶） | ✅ |
| T6 | 审计增强 + 查询（Category 分类/登录审计/分页查询） | ✅ |
| T7 | Nacos 注册发现（契约装配 RegistrarRegistry，凭据/namespace 语义踩坑修复） | ✅ |
| T8 | 端到端验证 + 收尾（§9 全序列、metrics 端口根治、文档同步） | ✅ |
| T9 | 可观测性契约化（tracer/metrics 段驱动 OTLP 直推，显式主开关契约；trace 云端 Jaeger 终验闭环，metrics 云端核对待平台管线） | ✅ |

## 目录

```
examples/go-bald-admin/                 (独立 go module)
├── cmd/go-bald-admin/main.go          入口：appkit.Run + 拦截器链序 + metrics/audit/nacos 接线
├── cmd/go-bald-admin/wire*.go         业务装配（wire 声明 + 生成实现；BizSet 定义在 apiserver/bizset.go）
├── cmd/probe/main.go                  T7 冒烟探针（契约路径注册→心跳→注销，task smoke:nacos）
├── configs/go-bald-admin.yaml         契约驱动配置（bconf BootstrapConfig，proto 为唯一真相源）
├── api/                               业务契约（T2 收敛：proto + buf 生成物 api/gen/）
├── internal/
│   ├── apiserver/
│   │   ├── biz/v1/<域>/               业务逻辑（auth/secret/tenant/user/menu/permission/dict/file/auditlog）
│   │   ├── handler/gin/ + grpc/       协议接入层（HTTP 路由 / gRPC service，T10 对称归位）
│   │   ├── model/                     gorm 模型
│   │   ├── e2e/                       端到端测试（T10 独立测试包，真实 gin+store+拦截器链）
│   │   └── server.go + bizset.go      路由装配（RegisterRoutes(*BizSet) 直传）
│   ├── bootstrap/                     InitBridges + Configure（PG/Redis/MinIO/策略装载；openDB 为 baldgorm.Open 薄封装）
│   └── security/{casbin,audit}/       授权（策略数据化）/审计后端桥接
└── docs/                              设计/需求/移植计划文档
```
