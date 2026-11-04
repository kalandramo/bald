# bald

[![release](https://img.shields.io/github/v/tag/kalandramo/bald?sort=semver&label=latest%20tag)](https://github.com/kalandramo/bald/releases)

**bald 是一个用 Go 把服务装配「工程化」的框架**：装配写成段驱动的 Provider 契约，业务域是一等公民，代码生成器用三道门守住生成物，高级机制面收进 `pkg/appkit/mech`——业务代码只声明业务，装配知识不进业务目录。

对外口径是「**声明式装配**」并标注等级：装配拓扑由纯函数算出并打印。当前 L1–L4 达成（L4 = 节点级 `ReloadPolicy` + `Drift` + `Reconcile`，含 `biz.<path>` 业务段），L5 在生成物维度达成（`_example/bald` 已迁入生成树，`gen app` 与 `gen domain` 的 `--check` 均零差异，域 REST 面走默认绑定）。判据与口径见 [Bald 装配计划与 doctor](docs/guide/zh-CN/Bald%20装配计划与%20doctor.md)。

## 文档三层导览

| 层 | 回答什么 | 入口 |
|---|---|---|
| 认知层 | bald 是什么、为什么这样设计 | [Bald 入门简介](docs/guide/zh-CN/introduction/README.md) |
| 设计层 | 设计与决策（为什么长成现在的形状） | [开发手册 docs/devel/zh-CN/](docs/devel/zh-CN/) |
| 使用层 | 怎么用（分页操作手册） | [用户手册 docs/guide/zh-CN/](docs/guide/zh-CN/) |

## 30 秒最小路径

在 Go module 根目录放一份 `appspec.json`（最小声明只需 `meta` + `bootstrap.server`）：

```json
{
  "meta": {
    "name": "demo-svc",
    "module": "github.com/you/demo-svc",
    "desc": "我的第一个 bald 服务"
  },
  "bootstrap": {
    "server": {
      "http": { "addr": ":8080" },
      "grpc": { "addr": ":9090" }
    }
  }
}
```

然后生成、拉起：

```bash
# CLI 住在独立 module github.com/kalandramo/bald/cli（go.mod 零 replace），
# 所以这一条装得上——它曾经是纸面承诺，原因见风险评审 §3.4：
go install github.com/kalandramo/bald/cli/cmd/bald@latest

cd /path/to/demo-svc        # 进你的服务目录
go mod init github.com/you/demo-svc   # 尚未 init 时执行，与 meta.module 一致
bald gen app --spec appspec.json
go mod tidy && go run ./cmd/demo-svc
```

把 `@latest` 换成 `@v0.1.0` 就钉住生成器行为。只有要改生成器本身、或你用的代理还没索引到新 tag 时才需要源码检出：`git clone https://github.com/kalandramo/bald.git && cd bald/cli && go install ./cmd/bald`——注意 `cli/` 目录只在拆分之后的 ref 上存在。

`go mod tidy` 拉的是库模块，按各自的 tag 锁版本，例如 `go get github.com/kalandramo/bald/pkg@v0.2.1`（appkit 装配编排与能力包都住这里）。仓库根模块只提供本仓工具，没有可导入的包，别把它当依赖。

一条命令生成整套可编译骨架，`curl :8080/healthz` 即验证探针。审计、组件、能力、业务域等更完整的声明见[快速入门](docs/guide/zh-CN/quickstart/README.md)与[用 bald CLI 起步新服务](docs/guide/zh-CN/用%20bald%20CLI%20起步新服务.md)。

## 仓库结构

```
bald/
├── transport/                # 协议层：Server 契约 + grpc/http/gateway 实现子包 + web 流水线（强绑定 gin）；mcp 独立 module
├── cobramcp/                 # Cobra → MCP 工具桥接（独立 module）
├── bconf/  bconfig/          # 契约（proto 单一真相源）与配置源 provider（独立 module）
├── bootstrap/                # 装配期载体与配置装载内核（config/ 子包：层模型装载器）
├── registry/                 # 注册/发现契约（inmemory 内置；etcd/consul/nacos/kubernetes 直连 SDK）
├── retry/  ratelimit/  broker/  circuitbreaker/   # 契约 + 多实现，各自独立 module
├── pkg/                      # 独立 module（github.com/kalandramo/bald/pkg）：appkit 装配编排
│                             #   （MustServe + 段驱动 Provider + Domain + mech）与 store/authn/
│                             #   authz/audit/metrics/middleware 等能力包；适配器都 require 它而非根
├── contrib/                  # 独立 module 桥接（authn-jwt / store-gorm / authz-casbin / audit-gorm /
│                             #   audit-stream / observability-otlp / database）
├── cli/                      # 官方 CLI 独立 module（github.com/kalandramo/bald/cli）：cmd/bald
│                             #   （gen proto / store / app / domain + release）与 internal/{codegen,release}；
│                             #   go.mod 零 replace，这是它区别于仓库根的唯一目的（风险评审 §3.4）
├── internal/docscheck/       # 仓库根只剩这一包：文档与架构制品的机器闸门（不构成对外 API）
└── _example/bald/            # 最小示例（MustServe + std + 域声明，HTTP/gRPC 双协议）
```

多 module 仓库：在每个 `go.mod` 所在目录内 `go test ./...`。

质量门禁：`task lint` / `task vuln` / `task race` 与 CI 共用同一条模块派生（`scripts/list-modules.sh`），新增 module 不用顺手改清单；每个 module 跑 `golangci-lint`（根 `.golangci.yml`）与 `govulncheck`（只拦代码可达漏洞）。主循环跑 `go test -short`，**非 `-short` 的集成用例由 CI 的 `integration` 作业注入真 etcd/consul/redis/mongo 跑，registry 的 kubernetes 后端另由 `integration-kubernetes` 作业起真 kind 集群跑**（registry / store / 队列三条接缝），并先探活、后逐条断言判据真的 `--- PASS`——服务没起来是作业失败，不是二十几条 Skip。改 `.github/workflows/*.yml` 后先跑 `task ci-workflow-lint`（CI 侧是同内容的 `workflow-lint` 作业）：GitHub 在编译期解析作业文件，一个未登记的键（比如 `services` 下的 `args`）会让整个文件被拒，只留「workflow file issue」和 0 个 job，本地 YAML 解析器看不出任何问题。

**未接线能力（reference-only）**：`circuitbreaker` 的 `vegas`/`sres`/`sentinel` 三个后端、`transport/graphql`、`workflow/argo`、`ai/*`、`oss/{s3,minio}` 在仓内没有消费方。它们照常随仓库发布、按 reference-only 保留——不删除，也不在文档里假装已接线。清单与可复现判据见[当前架构 §6](docs/architecture/current-architecture.md)。`retry` 已不在这份清单里：它的退避原语（`retry.ExponentialBackoff` + `EqualJitter`/`FullJitter`）由 `broker/kafka` 的订阅退避与 `registry/etcd` 的重注册退避消费，但 `retry.Retrier` 的重试循环仍未接线。

**熔断已接进入口链**：`server.http.middleware.circuit_breaker` / `server.grpc.middleware.circuit_breaker` 写上一段，两面就各挂一层 hystrix 熔断（HTTP 拒绝回 503、gRPC 回 `UNAVAILABLE` + `Reason=CIRCUIT_OPEN`），缺段即不挂这一层。链序固定在授权之后、限流之内、超时之外——由 `pkg/middleware/bundle` 构造，不靠业务遵守文档。gRPC 的 stream 面刻意不挂（一条流的「失败」没有一致答案）。最小写法：

```yaml
server:
  http:
    middleware:
      circuit_breaker:
        error_threshold: 0.5   # 窗内失败率过此值开窗
        min_requests: 20       # 窗内样本不足时不开窗（地板保护）
        open_duration_ms: 5000 # 开窗后的休眠时长，到期转半开放一个探针
        window_ms: 10000       # 滑窗时长（hystrix 按绝对格点分桶推进）
  grpc:
    middleware:
      circuit_breaker:         # 字段同上，没有 status_code——gRPC 的拒绝码是协议的一部分
        error_threshold: 0.5
```

上面四个值是 hystrix 的默认值，写出来只为示意字段；段内任何字段留 0 都读作「用默认」（proto3 分不出「写了 0」和「没写」），只有负值、`error_threshold > 1`、`status_code` 非 5xx 才是配置错误——启动期直接失败，不静默降级。

`vegas`（按 RTT 膨胀）/`sres`（概率判据）/`sentinel`（规则中心）三个后端没有契约段，换算法经 `bundle.CircuitBreaker(hystrix-替代品)` 代码级注入——它们没段不是缺陷：sres 无请求量下限、vegas 在入口面没有「下游」可测、sentinel 要引第三方规则中心，各有各的不可声明理由。

**服务发现已接线**：`registry.Discovery` 不再是「只注册不解析」——gRPC 反向代理的后端地址写成 `discovery:///<服务名>` 即由注册中心解析端点（写 `host:port` 仍走直连），段驱动装配会自动取装配好的注册中心，消费方只改一行配置。服务名按后端的真实命名写：内存与 etcd 用注册时的 `Name`，nacos 用 `<名>.grpc`。示例可当场跑：

```bash
cd _example/bald

# 内存注册中心：完整起一个应用，REST 经转码面打到 gRPC 面（无需外部服务）
go test ./internal/bootstrap/ -run TestGatewayBackendViaDiscovery -v

# 真注册中心（opt-in，起容器后可跑；不可达即 Skip，不算失败）
docker run -d --name etcd -p 127.0.0.1:2379:2379 quay.io/coreos/etcd:v3.5.14 /usr/local/bin/etcd \
  --advertise-client-urls http://127.0.0.1:2379 --listen-client-urls http://0.0.0.0:2379
go test ./internal/bootstrap/ -run TestGatewayDiscoveryEtcd -v

docker run -d --name nacos -p 127.0.0.1:8848:8848 -p 127.0.0.1:9848:9848 \
  -e MODE=standalone -e NACOS_AUTH_ENABLE=false --platform linux/amd64 nacos/nacos-server:v2.3.2
go test ./internal/bootstrap/ -run 'TestGatewayDiscoveryNacos|TestNacosDiscovery' -v
```

见[当前架构 §6](docs/architecture/current-architecture.md) 与[流程快照 §③](docs/architecture/flows.md)。

## 可运行示例

| 目录 | 定位 |
|---|---|
| [`_example/bald`](_example/bald) | 最小能力示例：HTTP/gRPC 双协议、配置四源、内存↔GORM 切换、web 流水线（绑定/校验/响应） |
| [kalandramo/bald-admin](https://github.com/kalandramo/bald-admin) | 官方参考范例（独立 monorepo）：真实业务端到端验证段驱动装配与域化 |

## 设计文档

- 全部设计篇目索引：[docs/devel/zh-CN/README.md](docs/devel/zh-CN/README.md)
- 公开契约速查：[框架契约总览](docs/devel/zh-CN/框架契约总览.md)
- 装配系统的设计与取舍：[Bald 声明式装配设计](docs/devel/zh-CN/Bald%20声明式装配设计.md)
