# go-bald-admin

基于 [bald](../../) 重构 `go-wind-admin/backend` 的**官方参考范例**（验证 P0–P9）。

它用真实业务（用户/角色/机密、RBAC 授权、多租户隔离）跑通 bald 的核心能力：
认证（JWT）、授权（casbin RBAC，REST/gRPC 同源归一化）、审计（旁路不阻断）、
可观测性指标（Prometheus 本地 + OTLP 远端直推）——全部经 bald 中间件/桥接实现，业务不直连引擎。

> 设计见 [`docs/设计文档.md`](docs/设计文档.md)；需求见 [`docs/需求文档.md`](docs/需求文档.md)。
> 实现契约（外部依赖禁止 fake/mock/stub）见设计文档 §0。

## 架构

- 双协议：`gin` HTTP(`:8080`) + `gRPC`(`:9090`)，可选 `grpc-gateway` REST 转码(`:8081`)。
- 认证：`bald-authn-jwt`（RS256/ES256/HMAC，M6.5 非对称）。
- 授权：`internal/security/casbin` 桥接 casbin（RBAC 策略即数据，M6.1）。
- 多租户：`bald-store-gorm` SQLite 内存库 + P8 自动注入 `tenant_id`（M3/M4）。
- 缓存：`internal/cache/redis` Cache-Aside（Redis 空则直连 store，M6.2）。
- 装配：`google/wire` 接管业务对象，appkit 负责框架发现（M6.4）。
- 审计：`pkg/audit` 核心 + `internal/security/audit.LoggerAuditor`（M7）。
- 指标：`pkg/metrics` 核心 + `internal/observability/metrics`（Prometheus/OTLP，M8/M9）。

## 运行

```bash
# 从仓库根（bald 独立 module，replace 指向本地核心）
cd bald/examples/go-bald-admin

# 起服务（默认 :8080 HTTP / :9091? 见下；metrics :9090）
go run ./cmd/go-bald-admin --config=configs/go-bald-admin.yaml

# 或覆盖地址
BALD_HTTP_ADDR=:18080 go run ./cmd/go-bald-admin
```

### 端口

| 端口 | 用途 | 覆盖 env |
|------|------|----------|
| `:8080` | HTTP(gin) 主服务 | `BALD_HTTP_ADDR` / `--http.addr` / yaml `http.addr` |
| `:9090` | gRPC | `BALD_GRPC_ADDR` / yaml `grpc.addr` |
| `:8081` | grpc-gateway REST 转码 | `BALD_GATEWAY_ADDR` / yaml `gateway.addr` |
| `:9090` | `/metrics` Prometheus 抓取 | `BALD_ADMIN_METRICS_ADDR` |

> 注：metrics 端口默认与 gRPC 同值 `:9090` 仅巧合；通过 `BALD_ADMIN_METRICS_ADDR`
> 显式分开（如 `:9091`）以避免冲突。

### 远端指标（M9 OTLP）

设 `BALD_ADMIN_OTLP_ADDR` 即额外直推远端 APM（OTel Collector / VictoriaMetrics / Grafana Cloud）：

```bash
BALD_ADMIN_OTLP_ADDR=http://localhost:4318 \
BALD_ADMIN_METRICS_ADDR=:9091 \
go run ./cmd/go-bald-admin --config=configs/go-bald-admin.yaml
```

裸 `host:port` 走 `WithInsecure`（内网 collector）；`http(s)://` 前缀按完整 URL 解析。

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
| M7 | 审计日志（传输中立 + 旁路不阻断） | ✅ |
| M8 | 可观测性指标（Prometheus） | ✅ |
| M9 | OTLP 远端直推（与 Prometheus 并存） | ✅ |

## 目录

```
examples/go-bald-admin/                 (独立 go module)
├── cmd/go-bald-admin/main.go          入口：appkit.Run + 拦截器链序 + metrics/audit 接线
├── configs/go-bald-admin.yaml         四源配置（viper）
├── proto/ gen/                        业务契约（buf 生成）
├── internal/
│   ├── apiserver/                     业务（auth/secret biz + gin/gRPC handler）
│   ├── bootstrap/                     InitBridges（注入 Authenticator/Authorizer/store）
│   ├── security/{casbin,audit}/       授权/审计后端桥接
│   ├── cache/redis/                   Cache-Aside
│   ├── observability/metrics/         metrics 桥接（Prometheus/OTLP）
│   └── grpcutil/                      传输工具
└── docs/                              设计/需求文档
```
