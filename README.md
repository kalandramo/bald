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
go install github.com/kalandramo/bald/cmd/bald@latest
go mod init github.com/you/demo-svc   # 尚未 init 时执行，与 meta.module 一致
bald gen app --spec appspec.json
go mod tidy && go run ./cmd/demo-svc
```

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
├── pkg/                      # appkit 装配编排（MustServe + 段驱动 Provider + Domain + mech）；
│                             #   berrors/log/store/authn/authz/audit/metrics 等抽象与 middleware
├── contrib/                  # 独立 module 桥接（authn-jwt / store-gorm / authz-casbin / audit-gorm /
│                             #   audit-stream / observability-otlp / database）
├── cmd/bald/                 # 官方 CLI：bald gen proto / store / app / domain
└── _example/bald/            # 最小示例（MustServe + std + 域声明，HTTP/gRPC 双协议）
```

多 module 仓库：在每个 `go.mod` 所在目录内 `go test ./...`。

## 可运行示例

| 目录 | 定位 |
|---|---|
| [`_example/bald`](_example/bald) | 最小能力示例：HTTP/gRPC 双协议、配置四源、内存↔GORM 切换、web 流水线（绑定/校验/响应） |
| [kalandramo/bald-admin](https://github.com/kalandramo/bald-admin) | 官方参考范例（独立 monorepo）：真实业务端到端验证段驱动装配与域化 |

## 设计文档

- 全部设计篇目索引：[docs/devel/zh-CN/README.md](docs/devel/zh-CN/README.md)
- 公开契约速查：[框架契约总览](docs/devel/zh-CN/框架契约总览.md)
- 装配系统的设计与取舍：[Bald 声明式装配设计](docs/devel/zh-CN/Bald%20声明式装配设计.md)
