# 用 bald CLI 起步新服务（bald gen app）

> 定位：面向 **bald 使用者**（用 bald 写服务、想从零起步的开发者）的分步指南；不解释框架内部设计，只讲「怎么装、怎么写、怎么跑」。
> 关联设计：[代码生成工具设计.md](../devel/zh-CN/代码生成工具设计.md)、[架构优化路线.md §P12](../devel/zh-CN/架构优化路线.md)、[框架契约总览.md](../devel/zh-CN/框架契约总览.md)
> 状态：已实现（首个对外发布 `v0.1.0`，本文命令与日志均实测验证）

---

## 1. 前置条件

- Go ≥ 1.26（`go.mod` 声明 `go 1.26.5`）。
- 网络可达 `https://goproxy.cn`（国内默认可用）或 `proxy.golang.org`。
- 服务本身只需 bald 库；**代码生成器是独立 CLI**，不随业务代码引入。

## 2. 安装 CLI

```bash
go install github.com/kalandramo/bald/cmd/bald@latest
bald --help   # 验证安装
```

版本语义说明：

- `@latest` 会解析**最新 release tag**（当前 `v0.1.0`）；固定版本用 `@v0.1.0`。
- 新 tag 发布后 sum.golang.org 校验和收录有延迟（数小时），期间安装可能报 `verifying module ... 404 not found: temporarily unavailable`——**属正常现象**，稍后重试即可，不需要任何额外配置。
- 安装后无 PATH 问题（`go install` 输出到 `GOPATH/bin`，默认已在 PATH）。

## 3. 从零起步一个可编译的应用

```bash
# 1) 初始化 module
mkdir my-service && cd my-service
go mod init example.com/my-service

# 2) 声明装配（AppSpec，见 §4）
vim appspec.json

# 3) 用 CLI 生成装配骨架（spec 模式下 <name> 可省略，应用名/输出路径由 AppSpec meta.name 决定）
bald gen app --spec appspec.json
# => generated app (spec-driven): cmd/my-service/main.go

# 4) 拉取依赖并编译
go get github.com/kalandramo/bald@v0.1.0   # 或直接 go mod tidy
go mod tidy
go build ./...    # 生成骨架保证可编译

# 5) 准备本地配置（骨架要求该文件存在，见 §5）
mkdir configs
echo 'http:
  addr: ":8080"
grpc:
  addr: ":9090"' > configs/my-service.yaml

# 6) 运行
go run ./cmd/my-service
```

到此得到一个**可启动**的骨架：HTTP + gRPC 双 server、heartbeat 组件、审计后端协调器全部真实接线（§6 展示启动日志）。接下来按生成文件里的 `[FILL]`/`TODO` 标注填入业务即可。

## 4. AppSpec：声明式装配方言

AppSpec 用单个 JSON 描述「应用要开通哪些 server、挂载哪些组件、声明哪些能力依赖、启用哪些审计后端」——它是装配的**单一真相源**（对应代码生成设计 P12 第二步）。改动装配应先改 AppSpec 再 `bald gen app --spec` 重新生成，**不要手改生成文件**。

### 4.1 字段参考（JSON 字段名即 protojson 形式）

| 字段 | 类型 | 说明 |
|---|---|---|
| `meta.name` | string | 应用/命令名；决定 `cmd/<name>/main.go` 与 `configs/<name>.yaml` |
| `meta.module` | string | 你的 Go module 路径（写入生成文件头，提示用） |
| `meta.desc` | string | 应用短描述 |
| `server.http` | bool | 开通 HTTP（gin） |
| `server.grpc` | bool | 开通 gRPC |
| `server.gateway` | bool | 开通 grpc-gateway（**当前为业务 TODO 填充点**，见 §7） |
| `server.http_addr` / `server.grpc_addr` | string | 默认监听地址（config 未覆盖时生效） |
| `components[]` | 数组 | `{kind, name, config_prefix}`，托管组件（C1 生命周期） |
| `capability.provides[]` | string[] | 本进程提供的能力（S1，启动期 fail-fast） |
| `capability.requires[]` | 数组 | `{component, caps[]}`——**组件对能力的依赖**（如 `audit.store` 需要 `db`），缺失启动即报 |
| `audit_backends[]` | string[] | 审计后端期望态（协调器据此热挂载，见 §5.3） |
| `bundle_normalized` | bool | 启用 P9 归一化链（审计 object/action 双传输同源） |

组件 `kind` 当前实现：`heartbeat`（真实启动）；`db`/`redis`/`otlp`/`casbin`/`store-audit`/`stream-audit` 为工厂占位（`buildComponent` 内 TODO，业务接入后返回真实 `appkit.Component`）。

### 4.2 最小完整示例

```json
{
  "meta": {
    "name": "my-service",
    "module": "example.com/my-service",
    "desc": "演示服务"
  },
  "server": { "http": true, "grpc": true, "httpAddr": ":8080", "grpcAddr": ":9090" },
  "components": [
    { "kind": "heartbeat", "name": "my-service.heartbeat", "configPrefix": "my-service" }
  ],
  "capability": {
    "provides": ["db"],
    "requires": [{ "component": "audit.store", "caps": ["db"] }]
  },
  "auditBackends": ["log"],
  "bundleNormalized": true
}
```

> 仓库自带反向生成对照样本：`_example/bald/_scratch/go-bald-admin.appspec.json`（描述官方范例 go-bald-admin 的框架装配形状，可作参考）。

## 5. 配置

### 5.1 本地配置（必须存在）

骨架用 `appkit.ConfigFile("configs/<meta.name>.yaml")`——**该文件缺失时应用启动即失败**（报 `config: merge local ... no such file`）。最小内容可以很薄，未写的键回退代码/spec 默认：

```yaml
http:
  addr: ":8080"     # 未设置时回退 spec 的 server.http_addr
grpc:
  addr: ":9090"
# log:              # 日志键，见 §5.2
# audit:            # 审计键，见 §5.3
```

### 5.2 日志与 flag 覆盖

生成物将日志选项经 `appkit.Bind("", logOpts)` 绑定，flag/env/文件可覆盖（优先级 **flag > env > 文件 > 远程 > 代码默认**）：

```bash
go run ./cmd/my-service --log.level=debug          # 日志级别
go run ./cmd/my-service --http.addr=:18080         # 覆盖监听地址
```

### 5.3 审计后端期望态（R1-2 热协调）

- 期望态来源：config 键 `audit.backends`；未设置时回退 AppSpec 声明的 `audit_backends`。
- 生成物内置 R1-2 协调器 `reconcileAudit`：启动收敛期与配置热变更时把实际挂载的审计器 **diff-apply** 到期望态（K8s controller 语义，失败不回滚、下次补齐）。
- 已实现后端：`log`（事件落应用日志，开箱即用）；`store`/`stream` 后端为 TODO（依赖业务的 DB/Redis 桥接）。

## 6. 生成物解读（启动日志对照）

`cmd/<name>/main.go` 的真实装配（全部经 `appkit.New(Options...)` 完成，无运行期二次装配）：

- **S1 能力**：`Provides`/`Requires`（component, caps…）——漏声明依赖启动即报 `unresolved capabilities`。
- **C1 组件**：`Components(buildComponent(kind, ...)...)`。
- **R1-2**：`Reconcile("audit.backends", reconcileAudit)` + key 级 `OnKeyChange("http.addr", ...)`。
- **P10 bundle**：`bundle.New(...)` 一次构造，HTTP 走 `router.Use(b.Gin()...)`、gRPC 走 `b.GRPCChain()`。
- **servers**：`server.NewHTTPServer` / `server.NewGRPCServerWithRegister`（health + reflection 自带）。

启动日志（实测 v0.1.0 生成物）：

```
time=... level=INFO msg="appkit starting" name=my-service version=v0.1.0 servers=2
time=... level=INFO msg="appkit component started" component=my-service.heartbeat
time=... level=INFO msg="reconcile audit.backends" add=log remove=""
time=... level=INFO msg="audit backend mounted" backend=log
time=... level=INFO msg="audit event" object=component action=mount result=allow
time=... level=INFO msg="appkit started" servers=2
```

## 7. 骨架边界（生成器不代填的业务）

生成器保证**框架装配开箱可运行**；以下属业务填充点（生成文件内 `[FILL]`/`TODO`）：

| 位置 | 需要填什么 |
|---|---|
| `serveRunE` 内 HTTP 段 | 业务路由注册（`router.POST("/v1/...", handler)`） |
| `registerGRPCService` | gRPC service 注册（`s.RegisterService(...)`） |
| `buildComponent` | `db`/`redis`/`otlp`/`casbin` 等组件的真实工厂 |
| `buildAuditBackend` | `store`/`stream` 审计后端（依赖 DB/Redis） |
| bundle `TODO` 注释 | 业务依赖注入：`bundle.Authn(authn)` / `bundle.Authz(authz)` / `bundle.Metrics(rec)` |
| `gateway` | grpc-gateway 装配（模板未接线，属业务层） |

> 一个直接可抄的「真实项目完整分层范本」是官方范例 `examples/go-bald-admin`（业务路由/biz/wire/管理面齐全），生成骨架与之同形，业务层手写填充即可。

## 8. 其他生成命令

| 命令 | 作用 | 常用 flag |
|---|---|---|
| `bald gen app [name] [--spec spec.json]` | 生成应用装配骨架 main.go（无 `--spec` 时为第一版模板模式，需提供 `<name>`，含 `[FILL]` 填充点；spec 模式下 `<name>` 可省略） | `--out`、`--module`（提示用）、`--spec` |
| `bald gen proto <name>` | 生成 protobuf 服务骨架到 `api/proto/bald/<name>/v1` | `--out`、`--go-package`（默认不写，bald 生态由 buf managed mode 补充） |
| `bald gen store <name>` | 生成实体骨架（gorm tag + keyOf） | `--out`、`--out-pkg` |

```bash
bald gen proto user        # 产出 api/proto/bald/user/v1/user.proto 骨架
bald gen store user        # 产出 ./user.go（gorm tag + keyOf）
```

## 9. 常见问题

- **启动报 `config: merge local ... no such file`** → 未创建 `configs/<name>.yaml`（§5.1）。
- **启动报 `unresolved capabilities: ... (required by ...)`** → 能力声明不匹配：为 `requires` 中组件补齐对应 `Provides`（§4.1）。
- **`go get`/`go install` 报 sumdb 404** → 新 tag 收录延迟，数小时后重试（§2）。
- **想改装配形态** → 改 AppSpec 再 `bald gen app --spec` 重新生成，手改生成文件会被下次生成覆盖。
- **`heartbeat` 之外组件启动报 `unknown component kind`** → 该 kind 工厂未实现（§7 边界），返回错误使装配失败属预期，提示业务补工厂。
