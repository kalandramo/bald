# D2 · `bald gen app` 目标生成物设计（只读审计 + 设计结论）

范围：`D:\code\konglingfei\bald`（生成器 `internal/codegen/`、契约 `bconf/proto/`、装配面 `pkg/appkit/`）
+ 参考示例 `D:\code\konglingfei\bald-admin\backend`（proto 单一真相源 + buf 扇出）。
约束遵守：未修改任何仓库文件；未运行 `go build`/`go test`/`buf generate`/git 写操作。
行号口径：`read` 工具的真实行号（UTF-8）。禁止 `Get-Content`（本机对含中文源文件按非 UTF-8 解码，行数/行号会算少——本次实测复现：`register_nacos.go` 经 `Get-Content` 输出为乱码）。

接缝假设（不自行设计）：另有一位设计者负责 appkit 目标装配模型，保证「生成代码只需 ≤1 次声明式调用即可完成装配」。本设计的生成物只依赖该入口的**两条外部契约**，见 §6 未决问题 Q1。

---

## 0. 对上游 S3 证据的两处更正（本设计的前提）

| # | S3 说法 | 实测 | 证据 |
|---|---|---|---|
| C1 | G7「`bconf` 的 `FindUnknownKeys` 未被使用」 | **已过时**：`FindUnknownKeys` 有真实消费者，`StrictContract` opt-in 时对合并树做未知键 fail-fast | `bconf/unmarshal.go:41`（定义）→ `bootstrap/config/config.go:240-247`（调用）；框架面 `pkg/appkit/bootstrap.go:298`（`WithStrictContract`） |
| C2 | G7 的缺口描述 | 真实缺口是「**生成物从不 emit `WithStrictContract`**」→ 用户照抄 guide 的配置键（`http.addr`）在生成物上**运行期静默回退**，而不是启动报错 | 生成物模板 `appspec_gen.go:114-121` 只 emit `Name/Version/ConfigFile/WatchConfigFile/Reconcile`，无 `WithStrictContract`；guide `用 bald CLI 起步新服务.md:49-52,113-120` 写 `http.addr` |

另核实：`ServerSpec.gateway`（`appspec.proto:52`）在 `internal/codegen` 中 grep `Gateway|gateway` = **0 命中** → 死声明成立（G8）。

---

## 1. 结论要点（≤12）

1. **声明面不重造，直接复用 `bootstrap.v1.BootstrapConfig`**：新增 `AppSpec.bootstrap`（字段 7）承载 15 个契约段，`meta/components/capability` 保留为生成期声明；旧字段 2/5/6 降级为「快捷方式」，与 `bootstrap` 投影不一致时生成期报错——消灭双真相源而不是制造静默覆盖。
2. **生成物从「96～300 行单文件 `package main`」变为 8 个生成物文件 + 3 个用户物文件**，`cmd/<app>/main.go` ≤25 行，装配收敛到**一次声明式调用**（`internal/bootstrap/assemble.gen.go`）。
3. **`appspecData`（`appspec_gen.go:342-349`）+ `runGenAppSpec` 的 6 字段手工转写（`app.go:241-248`）整体删除**：模板直接吃 `*appspecv1.AppSpec`。这是「加一个能力改 4 处」膨胀的根因，删掉它是消除膨胀的最短路径。
4. **加一个 bconf 段 = 0 处模板改动**：生成物不再为每个段写条件块，而是把整棵 `BootstrapConfig` 交给框架一次装配；`{{if}}` 条件块从 16 处收敛为「段存在性遍历」。
5. **「声明字段必须有消费者」升级为机器守卫**：生成器维护 `字段 → 生成物标记` 表，测试遍历 proto 描述符逐个校验。`gateway` 这类死声明从此不可能再上线（G8 的通用解，而非只修 gateway）。
6. **一致性校验做成三道门**：Gate1 生成期 schema 门（kind 白名单/name 唯一/requires⊆provides/gateway-driver 组合）、Gate2 生成期契约门（`bconf.FindUnknownKeys` 扫生成配置树）、Gate3 运行期门（生成物 emit `WithStrictContract`）。Gate2 直接复用 `bconf/unmarshal.go:41` 的现成 API，不引入新机制。Gate3 修掉文档 D8 那条「照抄即无声失效」。
7. **配置键不再写裸字符串**（`appspec_gen.go:120,124-125,130`），改由 bconf 描述符派生 `json_name` → 「生成物与 bconf 契约不一致」在生成期就不可能发生（类型级而非测试级）。
8. **端到端示例链路落点 = 一个 `demo` 全栈域**：proto（含 `google.api.http`）→ model → biz → `handler/{grpc,gin,convert}.gen.go` → `domains.gen.go` 路由/注册 → 可 `curl` 通。bald-admin 的 `dict.proto` 已全部使用 `google.api.http` 注解（21 处，`dict.proto:7,19-101`），REST 路由与 gateway 登记可确定性生成。
9. **新增业务域：手写触碰 3 个文件（proto / model / biz），手写装配文件修改 0 个**。`bizset.go`、`server.go`、`cmd/app/assembly_biz.go`、`bootstrap.go` 的逐域列表**不再存在**；域清单转移到生成物 `internal/{bootstrap,apiserver}/domains.gen.go`。
10. **沿用 bald-admin 的 `internal/apiserver/{biz,model,handler}` 路径语义**（迁移成本最低、可对抄参考示例），**取代**它的两个手写装配文件（`bizset.go:39`、`server.go:46`）与 `assembly_biz.go` 的 DI 列表。`biz` 作为 `internal/` 下的**层**而非顶层目录——顶层 `biz/` 会让业务代码可被外部 module import，违反边界（设计判断）。
11. **回归生成改为三态文件归属**（`owned` 覆盖 / `seeded` 已存在即跳过且不算错误 / `user` 永不触碰），并修掉 `renderAppSpec` gofmt 失败时的**越权直写**（`appspec_gen.go:357-362`，无 `--force` 也覆盖用户文件）。
12. **验收从「字符串锚点 + 能否编译 + 3 秒存活」升级为可执行脚本**：sha256 白名单断言（判据 1）、装配行数差 = 域数断言（判据 2）、生成物内 layering 不变量测试（判据 3）、`生成 → 起服务 → curl → SIGTERM` 全链计时（判据 4）。

---

## 2. 目标生成物结构

### 2.1 目录树（逐文件职责 / 行数预算 / 归属）

归属三态：
- **生成物** = 每次 `bald gen …` 重生成都覆盖，用户不手改（头部带 `// Code generated … DO NOT EDIT.`）。
- **生成一次** = 生成器只在不存在时落盘；之后归用户，重生成**跳过**（输出 `kept:` 而非报错）。
- **用户物** = 生成器永不读写。

行数预算 = 设计判断（按目标形态估），非实测。

```
<app>/                                     （`bald gen app --spec appspec.json` 的产出根 = CWD）
├── appspec.json                                 用户物   ~45 行   声明真相源（用户维护；重生成读它）
├── cmd/<app>/main.go                            生成物   ≤25 行   仅 main(){ cmd.Execute() }
├── cmd/<app>/app/command.go                     生成物   ~45 行   cobra root + FParseErrWhitelist + 插件发现（对照 bald-admin assembly.go:187-218，623 行→45 行）
├── internal/bootstrap/assemble.gen.go           生成物   ~110 行  声明 → appkit 单次装配调用（§2.3）
├── internal/bootstrap/domains.gen.go            生成物   ~55 行   域清单聚合：import + 域描述符切片 + service 注册切片
├── internal/bootstrap/seed/seed.go              生成一次 ~70 行   业务种子数据（重生成跳过）
├── internal/bootstrap/seed/policies.go          生成一次 ~35 行   授权策略行（重生成跳过；默认从 proto 派生，见 §4）
├── internal/apiserver/apiserver.go              生成一次 ~40 行   路由分组/中间件挂载点（业务唯一装配钩子）
├── internal/apiserver/domains.gen.go            生成物   ~70 行   HTTP 路由登记 + convert 注册（取代 server.go:25-82）
├── internal/apiserver/biz/v1/<d>/<d>.go         用户物   ~80 行   业务逻辑（示例域 demo 由生成器 seed 一份）
├── internal/apiserver/model/<d>.go              用户物   ~30 行   持久化模型 + `KeyOf*`（`bald gen store` seed）
├── internal/apiserver/handler/grpc/<d>.gen.go   生成物   ~60 行   pb service 实现，逐方法委派 biz
├── internal/apiserver/handler/gin/<d>.gen.go    生成物   ~90 行   按 `google.api.http` 注解生成 REST 路由
├── internal/apiserver/handler/convert/<d>.gen.go 生成物  ~50 行   model ↔ pb 转换（取代 bald-admin 手写 convert/model.go）
├── internal/apiserver/layering_invariant_test.go 生成物  ~60 行   分层不变量测试（判据 3 的机器守卫，生成进新项目）
├── api/proto/<d>/v1/<d>.proto                   生成一次 ~60 行   契约（含 google.api.http + buf.validate 注解）
├── api/gen/go/<d>/v1/*.pb.go                    buf 生成       生成器只写 buf.gen.yaml，不产 pb（`buf generate` 产出）
├── api/buf.yaml + api/buf.gen.yaml              生成物   ~25 行   buf 扇出配置（Go / OpenAPI / TS）
├── configs/<app>.yaml                           生成物   ~40 行   由 `AppSpec.bootstrap` 派生 —— Gate2 的校验对象
├── taskfile.yml                                 生成物   ~35 行   `task proto` / `task run` / `task verify`
└── pkg/                                         不生成            业务自持可导出代码（生成器保留空目录占位）
```

### 2.2 关键文件内容形态（非完整代码，示意「职责边界」）

`cmd/<app>/main.go`（≤25 行）——**唯一的 `package main`**：

```go
// Code generated by "bald gen app". DO NOT EDIT.
package main

import (
	"os"

	"example.com/<app>/cmd/<app>/app"
)

func main() {
	if err := app.Execute(); err != nil {
		os.Exit(1)
	}
}
```

`internal/bootstrap/assemble.gen.go`——**装配的全部**（一次声明式调用）：

```go
// Code generated by "bald gen app". DO NOT EDIT.
package bootstrap

// Assemble 返回已按 appspec.json 装配好的 app；main 路径上只有这一处装配。
func Assemble() (*appkit.AppKit, error) {
	return appkit.Assemble(                // ← 接缝：另一位设计者的 ≤1 次声明式入口
		appkit.Contract(contractJSON),     // 整棵 bootstrap.v1.BootstrapConfig 的嵌入字节
		appkit.Domains(domains()...),      // 域描述符切片（domains.gen.go 提供）
		appkit.ConfigFile("configs/<app>.yaml"),
		appkit.StrictContract("biz"),      // Gate3：未知契约键启动即报（修 D8）
	)
}
```

> 注意：`appkit.Assemble` / `Contract` / `Domains` / `StrictContract` 是本设计**对 appkit 目标模型的接口需求**，不是 API 设计（§6 Q1）。

`internal/apiserver/domains.gen.go`——**取代 `server.go:46` 的逐 service 手工路由登记**：

```go
// Code generated by "bald gen domain". DO NOT EDIT.
package apiserver

import (
	"example.com/<app>/internal/apiserver/handler/gin"
	dictbiz "example.com/<app>/internal/apiserver/biz/v1/dict"
)

// MountRoutes 把每个域的路由挂到 app 的 HTTP 面上。
// 域数量增长 → 本函数体增长，但它是**生成物**（开发者不碰），
// 且每域恰好贡献 1 行（gindict.Mount(a, biz.Dict)）。
func MountRoutes(a *APIServer, biz *BizSet) {
	gin.MountDict(a, biz.Dict)
}
```

### 2.3 与现状对照

| 维度 | 现状 | 目标 |
|---|---|---|
| 产出文件数 | **1**（`appspec_gen.go:336` 单文件 `package main`） | 8 生成物 + 3 用户物 + 1 生成一次 |
| `cmd/<app>/main.go` | 就是装配本体（模板模式 96 行 `app.go:24-119`；spec 模式 ≈300 行 `appspec_gen.go:28-338`） | ≤25 行，仅入口 |
| 分层 | **无**（`app.go:139-141` 默认 `cmd/<name>/main.go`） | `cmd/` / `internal/{bootstrap,apiserver/{biz,model,handler}}` / `pkg/` |
| 装配调用形态 | `appkit.New(capOpts...)` + 手写 `bconf.NewBootstrap()`（`appspec_gen.go:81,175`） | 框架单一入口，整棵契约一次传入 |
| 业务入口 | 2 个 TODO 桩（HTTP `appspec_gen.go:109`、gRPC `appspec_gen.go:214` 空函数） | 一个可 `curl` 通的 `demo` 全栈域 |
| 路由登记 | 手写（`server.go:46`） | 生成物 `apiserver/domains.gen.go` |
| 配置键 | 模板裸字符串（`appspec_gen.go:120,124-125,130`） | 由 bconf 描述符派生 + Gate2/Gate3 校验 |

### 2.4 与 bald-admin 现有分层的关系：沿用 vs 取代

**沿用**（路径与语义不变，迁移成本最低，可直接对抄参考示例）：
- `cmd/<app>/main.go` → `cmd/bald-admin/main.go:26-29`（30 行）同形；
- `internal/apiserver/biz/v1/<域>/<域>.go` → `bald-admin …/biz/v1/dict/dict.go` 同路径；
- `internal/apiserver/model/<域>.go` → 同路径；
- `internal/apiserver/handler/{gin,grpc,convert}/` → 同路径，但从「手写」转为「生成物」。

**取代**（这些文件在新形态下**根本不该存在**）：

| bald-admin 文件 | 现行职责 | 取代者 |
|---|---|---|
| `internal/apiserver/bizset.go:39` | 域清单（不注册字段就编译不过） | `internal/bootstrap/domains.gen.go`（生成物） |
| `internal/apiserver/server.go:46` | 逐 service 手工路由登记（21 行） | `internal/apiserver/domains.gen.go`（生成物） |
| `cmd/bald-admin/app/assembly_biz.go:69,109` | 逐域 DI（20 个 `xxx.New` + 21 字段字面量） | 生成物（域描述符切片） |
| `cmd/bald-admin/app/assembly.go:274-288` | 11 个 gRPC service 注册 | 生成物（由 proto service 目录派生） |
| `cmd/bald-admin/app/servers.go:66-102` | 11 个 gateway 转码登记 | 生成物（由 `google.api.http` 注解派生） |
| `internal/bootstrap/bootstrap.go:196-197,300,364-365` | `Repositories` 字段 + `AutoMigrate` + `NewStore` 逐域列表 | 域自述（`model.Migrate` / `store.KeyOf`）+ 生成物聚合 |
| `internal/security/authz/alias.go` + `bootstrap.go:70-77` | 授权归一化别名表 | 生成物（从 proto service 名派生） |

**保留且不逐域增长**：`internal/bootstrap/seed/*`（业务种子与策略**内容**，属业务数据不是装配逻辑）、`cmd/<app>/app/options.go`（业务配置 schema，S2 §2.1 指出它「挟带业务决策」——那是业务决策的正确位置）。

---

## 3. 声明面 schema 设计

### 3.1 设计原则

现状 6 字段（`appspec.proto:15-33`）与 bconf 的 15 段（`bconf/proto/bootstrap/v1/bootstrap.proto:26-71`）是**两套并行的拓扑描述**，且 AppSpec 是劣化子集。目标：**bconf 已是契约真相源，AppSpec 不得复制它**。

### 3.2 schema（`bconf/proto/bald/appspec/v1/appspec.proto` 增量）

```proto
import "bootstrap/v1/bootstrap.proto";

message AppSpec {
  // ---- 生成期声明（bconf 表达不了）----
  AppMeta meta = 1;                       // 保留：name/module/desc（+ desc 落文件头注释）
  repeated ComponentSpec components = 3;  // 保留：kind/name/config_prefix（C1 组件）
  CapabilitySpec capability = 4;          // 保留：provides + requires（S1）

  // ---- 框架契约段：唯一真相源 = bconf，不复制字段 ----
  bootstrap.v1.BootstrapConfig bootstrap = 7;   // 新增：15 段一次带入

  // ---- 生成期专属的新增段 ----
  LayoutSpec layout = 8;                  // 输出根 / 分层命名 / 域清单策略
  BundleSpec bundle = 9;                  // 取代 bundle_normalized（bundle 是装配期概念）
  repeated DomainSpec domains = 10;       // 显式域声明；缺省 = 扫描 api/proto/*/v1/*.proto

  // ---- 2 / 5 / 6：保留字段号，降级为快捷方式 ----
  ServerSpec server = 2;                  // 允许；与 bootstrap.server 投影不一致 → 生成期报错
  repeated string audit_backends = 5;     // 允许；同上
  bool bundle_normalized = 6;             // 允许；同上
}

message LayoutSpec {
  string root = 1;              // 产出根，默认 "."
  string apiserver_dir = 2;     // 默认 "internal/apiserver"
  bool strict_contract = 3;     // 默认 true → 生成物 emit WithStrictContract（Gate3）
}

message BundleSpec {
  bool normalized = 1;          // P9 归一化
  repeated string layers = 2;   // 显式链序覆盖（缺省由框架固化）
}

message DomainSpec {
  string name = 1;              // "dict"
  string proto = 2;             // "api/proto/dict/v1/dict.proto"
  string model = 3;             // "internal/apiserver/model/dict.go"
  string biz = 4;               // "internal/apiserver/biz/v1/dict/dict.go"
  bool authz = 5;               // 是否需要授权（默认 true，策略自动派生）
  string migrate = 6;           // "model.Migrate"（缺省按约定推导）
}
```

`ComponentSpec` / `CapabilitySpec` / `Requirement` / `AppMeta` **不动**（`appspec.proto:36-86`）。`ComponentSpec` 增补一个 additive 字段：

```proto
message ComponentSpec {
  // ... 现有 1/2/3 不变 ...
  map<string, string> params = 4;   // 新增：组件工厂参数（现状 config_prefix 声明了但未被消费，appspec_gen.go:204 只 `_ = prefix`）
}
```

### 3.3 字段 → bconf 段映射（含现状消费者证据）

| AppSpec 声明 | bconf 唯一真相源 | 现状消费者 | 目标消费者 |
|---|---|---|---|
| `server.http` / `http_addr` | `server.http`（optional message **存在即开通**）、`server.http.addr` | `appspec_gen.go:47,104,161-172` | 契约段遍历 |
| `server.grpc` / `grpc_addr` | `server.grpc`、`server.grpc.addr` | `appspec_gen.go:50,159-172` | 契约段遍历 |
| `server.gateway`（**现状死声明**） | `server.gateway`（独立端口，`server.proto:464-473`）/ `server.http.driver="grpc-gateway"`（同端口，`server.proto:98-99`） | **0 命中** | `appkit.WithGatewayRegister`（`bootstrap.go:178`）+ driver 组合校验 |
| `audit_backends` | `audit.{log,store,stream}`（`bootstrap.proto:69-70`） | `appspec_gen.go:75-77,227-253` | 契约段遍历 |
| `bundle_normalized` | **无对应**（bundle 是装配期链，不是契约段） | `appspec_gen.go:97-99` | 生成期 `bundle.normalized` |
| `meta.*` | `app.*`（`app.proto`） | `appspec_gen.go:35,53,115,117,327-328` | 两者都在；`app.name` 缺省取 `meta.name` |
| **（现状缺）** log | `logger`（`bootstrap.proto:40`） | 硬编码 `bslog.NewOptions()`（`appspec_gen.go:91`） | 契约段遍历 |
| **（现状缺）** registry | `registry`（:37） | 无 | 契约段遍历 |
| **（现状缺）** database | `database`（:67） | 无 | 契约段遍历 |
| **（现状缺）** cache | `cache`（:61） | 无 | 契约段遍历 |
| **（现状缺）** storage | `storage`（:52） | 无 | 契约段遍历 |
| **（现状缺）** tracer | `tracer`（:43） | 无 | 契约段遍历 |
| **（现状缺）** metrics | `metrics`（:46） | 无 | 契约段遍历 |
| **（现状缺）** middleware | `server.http.middleware`（`server.proto:31-94`，7 个中间件） | 无 | 契约段遍历 |
| **（现状缺）** 多协议 server | `server.{http3,graphql,sse,websocket,tcp,udp,kcp,thrift,trpc,webtransport,cron,hptimer,mcp,signalr,socketio,webrtc,asynq,machinery,kafka,rabbitmq,redis_server,nats,mqtt,pulsar,activemq,azuresb,nsq,rocketmq,sqs,gateway}`（`server.proto:329-473`，32 段） | 无 | 契约段遍历 |
| **（现状缺）** broker/ai/workflow/script | `broker`/`ai`/`workflow`/`script`（`bootstrap.proto:49,55,58,64`） | 无 | 契约段遍历 |

### 3.4 消除线性膨胀

现状「加一个能力改 4 处」的实测链条：`appspec.proto` 加字段 → `appspecData` 加字段（`appspec_gen.go:342-349`）+ `runGenAppSpec` 手工赋值（`app.go:241-248`）→ 模板加 `{{if}}` 块（16 处之一）→ `app_test.go` 加锚点（:111-120）。

目标形态的对应关系：

| 现状 4 处 | 目标 | 改几处 |
|---|---|---|
| proto 字段 | proto 字段（在 `bootstrap.v1.BootstrapConfig` 里加，**那是框架契约的正常演进**） | 1（在 bconf，不在 appspec） |
| `appspecData` + 手工赋值 | **删除**：模板直接吃 `*appspecv1.AppSpec` | 0 |
| 模板 `{{if}}` 条件块 | **删除**：改为遍历 `bootstrap` 的已设置段（protoreflect 驱动），段 → 一行段工厂调用 | 0 |
| 测试锚点 | 断言从「字符串存在」改为「字段有消费者」（描述符遍历） | 0（新字段若忘接消费者则测试红） |

即：**加一个 bconf 契约段 = 改 bconf proto 1 处，生成器与测试 0 处**。这是「装配代码行数不随域/能力数量增长」在生成器侧的成立条件。

### 3.5 声明 → 生成物一致性校验（关 G6/G7）

三道门，全部可在 `internal/codegen` 内实现（`bconf` 已在根 module 的 replace 中：`go.mod: replace github.com/kalandramo/bald/bconf => ./bconf`）。

**Gate 1 · 生成期 schema 门** —— 新函数 `validateAppSpec(as *appspecv1.AppSpec) []error`，在 `app.go:232` 之后、`app.go:236` 之前调用：

| 检查 | 报错必须含 | 现状缺口证据 |
|---|---|---|
| `components[].kind` ∈ 已实现白名单 | 字段名 + 非法值 + 白名单 | `appspec_gen.go:207-209` 现在到运行期才炸 `unknown component kind` |
| `components[].name` 唯一 | 冲突名 + 首次出现位置 | 无任何校验 |
| `capability.requires[].caps ⊆ capability.provides` | 缺失的能力名 + 依赖它的组件 | S1 fail-fast 推迟到 `Run` 期 |
| `server.gateway=true` 时 `server.http.driver` 必须显式为 `"grpc-gateway"` | 两个字段名 | `bootstrap.go:170-177` 的「显式 > 隐式」纪律现在无人执行 |
| `bootstrap` 非空时，`server`/`audit_backends`/`bundle_normalized` 若同时非空，其投影必须与 `bootstrap` 一致 | 不一致的字段路径与两侧取值 | 现状三处真相源（AppSpec / config / 包级 var），S3 §2.3 第 4 条 |
| `layout.strict_contract=true` 但 `bootstrap.server.http` 为空 | 字段名 | 避免生成死配置 |

**Gate 2 · 生成期契约门** —— 把声明与生成物双向对齐：

1. `bootstrap` 段 `protojson.Marshal` → `map[string]any`；
2. `unknown := bconf.FindUnknownKeys(m, bconf.NewBootstrap())`，非空即报错（**复用 `bconf/unmarshal.go:41` 的现成 API**，与 `bootstrap/config/config.go:241` 同一调用形状）；
3. 把 `configs/<app>.yaml` 的解析结果做同一校验（捕获生成器自己拼错的键）；
4. **根治手段**：模板不再写裸字符串键（`appspec_gen.go:120,124-125,130`），改为由 bconf 描述符派生 `fd.JSONName()`。这样第 3 步在结构上不可能失败，第 3 步退化为回归锁。

**Gate 3 · 运行期门** —— 生成物必须 emit `WithStrictContract`（`pkg/appkit/bootstrap.go:298`），把 Gate 2 同款校验搬到运行期，覆盖 env / flag 来源的笔误（`bootstrap/config/config.go:238-247` 的注释明确「只查文件会漏掉 env 的笔误」）。**这一门直接修掉文档 D8**：用户按 guide 写 `http.addr` 会得到

```
config: unknown contract keys (typo? or declare in AllowBusinessSections): http.addr, grpc.addr
```

而不是现在的静默回退（`bconf/unmarshal.go:151-153` 的 `DiscardUnknown: true`）。

**G8 的通用解 · 字段消费表** —— 生成器持有 `fieldConsumers map[string]string`（proto 字段全名 → 生成物中必现的标记字符串），测试 `TestAppSpecDescriptorFieldsAllConsumed` 遍历 `AppSpec` 描述符逐字段校验；`gateway` 这类「声明了但无消费者」从「人肉 grep」升级为机器守卫。**新增字段忘接消费者 = 测试红**。

---

## 4. 端到端示例链路与「新增域」触碰清单

### 4.1 目标域结构（以 `dict` 域为对照）

| # | 文件 | 现状（S2 §4.2 实测） | 目标 | 归属 |
|---|---|---|---|---|
| 1 | `api/proto/<d>/v1/<d>.proto` | 新（手写） | **新，用户写**（`bald gen proto` seed 模板，含 `google.api.http`） | 用户 1/3 |
| 2 | `internal/apiserver/model/<d>.go` | 新（手写） | **新，用户写**（`bald gen store` seed） | 用户 2/3 |
| 3 | `internal/apiserver/biz/v1/<d>/<d>.go` | 新（手写） | **新，用户写**（`bald gen domain` seed 骨架） | 用户 3/3 |
| 4 | `api/gen/go/<d>/v1/*.pb.go`（3 个） | buf 生成 | buf 生成（`task proto`） | 生成物 |
| 5 | `internal/apiserver/handler/gin/<d>.gen.go` | 手写 | **生成物**（`google.api.http` 驱动） | 生成物 |
| 6 | `internal/apiserver/handler/grpc/<d>.gen.go` | 手写 | **生成物** | 生成物 |
| 7 | `internal/apiserver/handler/convert/<d>.gen.go` | 手写共享层 `convert/model.go` | **生成物** | 生成物 |
| 8 | `internal/apiserver/bizset.go:39` | **手写装配** | **文件不存在** | 删除 |
| 9 | `internal/apiserver/server.go:46` | **手写装配** | **文件不存在** | 删除 |
| 10 | `cmd/bald-admin/app/assembly_biz.go:69,109` | **手写装配** | **文件不存在** | 删除 |
| 11 | `cmd/bald-admin/app/assembly.go:280-281` | **手写装配** | **生成物**（`domains.gen.go` 的 service 切片） | 生成物 |
| 12 | `cmd/bald-admin/app/servers.go:83-88` | **手写装配** | **生成物**（注解驱动） | 生成物 |
| 13 | `internal/bootstrap/bootstrap.go:196-197,300,364-365` | **手写装配** | **文件不存在**（域自述 + 生成物聚合） | 删除 |
| 14 | `internal/security/authz/alias.go` | **手写装配** | **生成物**（proto service 名派生） | 生成物 |
| 15 | `bootstrap.go:760-776` seedPolicies | 手写装配 | `internal/bootstrap/seed/policies.go`（生成一次；默认自动派生，仅自定义策略需手写） | 生成一次 |
| 16 | `assets/openapi.yaml` | 生成（改） | 生成（buf 扇出） | 生成物 |

**计数**：现状 15–17 文件 / 装配 6–7（S2 §4.2）；目标 **手写 3 文件 / 手写装配 0**。

### 4.2 如何压到 3 文件（四个机制）

1. **路由由 proto 注解派生**：`google.api.http` → gin 路由 + gateway 登记。可行性已实证——bald-admin `dict.proto:7,19-101` 全部 RPC 都带注解（21 处命中）。
2. **域清单由生成器扫描产出**：`bald gen domain <name>` 扫描 `api/proto/*/v1/*.proto` + `internal/apiserver/{biz,model}/` 目录，**确定性重写** `internal/{bootstrap,apiserver}/domains.gen.go`。Go 无运行期包发现，导入清单必须有承载者；承载者从「手写文件」变成「生成物」是「零装配修改」的唯一可行路径。（仓库已有同向先例：`_example/bald/register_grpcgw.go` 用 build tag + `init()` 注入装配，注释明写「无需再手工改本文件」——但它的**导入**仍需手写，正是本设计要消除的残余。）
3. **持久化由域自述**：每个 `model` 包暴露 `func Migrate(db *gorm.DB) error` 与 `func KeyOf(v *T) string`；生成物聚合两者。取代 `bootstrap.go` 的 `Repositories`/`AutoMigrate`/`NewStore` 逐域列表（S2 §2 测算每域 ≈6.5 处引用）。
4. **授权策略默认自动派生**：策略 object 名从 proto service/method 名按固定规则派生（现状 `bootstrap.go:70-77` 的 `grpcObjectAliases` 就是手工补丁，`bootstrap.go:648-654` 记录了「只有四条策略 → List/Create 403」的实测教训）。默认派生后，新域**开箱可授权**；只有**自定义**策略才需写 `seed/policies.go`。

---

## 5. 回归生成语义

### 5.1 文件归属规则

| 状态 | 判定依据 | 重生成行为 | 输出 |
|---|---|---|---|
| `owned`（生成物） | 文件头含 `// Code generated by bald … DO NOT EDIT.` | 覆盖；若内容与「生成器上次写下的 hash」不符 → **警告**（`modified: <path> (you edited a generated file; overwriting)`），`--keep-modified` 可跳过 | `generated: <path>` |
| `seeded`（生成一次） | 不存在则创建，头含 `// Code generated once by bald …; now maintained by you.` | **已存在即跳过，不算错误** | `kept: <path>` |
| `user`（用户物） | 由 `appspec.json` 的 `DomainSpec` 指出的 `biz`/`model` 路径 + `seed/*` | 永不读写 | `skip: <path> (user file)` |

`hash` 记账方式（设计判断，二选一）：① 同目录 `.bald.gen.json` 记录 `path → sha256`；② 文件头注释行 `// gen-hash: <sha256 of body>`。推荐 ①（不污染文件内容，且能记录「生成器从未写过此文件」与「写过但被你改了」的区别）。

### 5.2 落盘通道升级

现状 `writeFileGuarded(path, content, force, streams)`（`iostreams.go:38-54`）语义太粗（「存在即报错」把「seeded 正常跳过」也变成错误）。目标：

```go
type Ownership int
const (Owned Ownership = iota; Seeded; User)

// 落盘唯一通道：按归属决定覆盖/跳过/警告；--force 只提升 owned 的覆盖权限。
func writeGuarded(path string, body []byte, own Ownership, force bool, streams IOStreams) error
```

`gen proto` / `gen store` / `gen app` 全部改走它。

### 5.3 修掉越权直写

`renderAppSpec`（`appspec_gen.go:357-362`）在 gofmt 失败时 `_ = os.WriteFile(path, buf.Bytes(), 0o644)`，**无 `--force` 也覆盖用户文件**（第 360 行注释自称「不会静默替换用户文件」，但 `os.WriteFile` 对已存在文件就是截断重写）。修法：

- **不写目标路径**；写 `<path>.rej`（或仅在 stdout 打印带行号的原始模板输出），并在错误里附 `.rej` 路径。
- 回归锁：新增测试 `TestRenderAppSpec_GofmtFailureNeverTouchesTarget`——预置目标文件内容，注入一个会 gofmt 失败的模板，断言目标文件字节不变、`.rej` 存在。

### 5.4 `gen store` 落点约定（关 G4）

现状 `--out` 默认 `.`、`--out-pkg` 默认 `main`（`gen.go:169-174`）且**零测试**（`genStoreCmd` 仅在 `gen.go` 自身出现）。目标：默认 `internal/apiserver/model/<name>.go`，`--out-pkg` 默认 `model`（从 `layout.apiserver_dir` 推导），`--out` 仍可覆盖；补命令级测试（默认路径 + 二次拒绝 + `--force`）。

---

## 6. G1–G10 逐项关闭方案

| # | 缺口 | 改什么文件 | 怎么改 | 怎么验证 |
|---|---|---|---|---|
| G1 | 声明面不完整（6 字段 vs bconf 15 段） | `bconf/proto/bald/appspec/v1/appspec.proto`（加 `import bootstrap/v1/bootstrap.proto` + `bootstrap` 字段 7 + `LayoutSpec`/`BundleSpec`/`DomainSpec`） | §3.2；生成器改为段存在性遍历 | 全段开启的 `appspec.sample.json` → 生成物编译 + 起服务，每个段有对应 provider 装配日志 |
| G2 | 生成物用旧装配路径（`appkit.New` + 手写 `bconf.NewBootstrap` + `Bind`×3） | 删除 `appspec_gen.go:28-338` 的 `appspecTmpl`，改为 8 个文件模板；`internal/bootstrap/assemble.gen.go` 只调一次框架入口 | §2.2 | 断言生成物源码**不含** `appkit.New(` 与 `bconf.NewBootstrap()`；含 `WithStrictContract`；`WithHTTP` 声明而契约 `server.http` 为空 → 启动报错（`bootstrap.go:478-498` 的 fail-fast 在生成物上被到达） |
| G3 | 无分层骨架 | 新增目录模板集 + `layout` 声明 | §2.1 | `TestGenAppSpec_DirectoryLayout`：断言路径集合存在（`cmd/<app>/main.go`、`internal/bootstrap/assemble.gen.go`、`internal/apiserver/{biz,model,handler}`、`configs/<app>.yaml`、`api/{proto,buf.yaml}`） |
| G4 | `gen store` 无落点约定 + 零测试 | `gen.go:165-196` | §5.4 | 新增 `TestGenStore_DefaultLayout`（默认 `internal/apiserver/model/<n>.go` + 二次拒绝 + `--force`） |
| G5 | 无端到端示例链路 | 新增 `bald gen domain` 子命令 + 各 `.gen.go` 模板 | §4 | 端到端：生成 → `go mod tidy` → 起服务 → `curl :8080/v1/demo` 断言 200 + JSON body 非空 |
| G6 | 生成期零语义校验 | `app.go`（`runGenAppSpec` 插 `validateAppSpec`）+ 新文件 `internal/codegen/appspec_validate.go` | §3.5 Gate1（6 条检查） | 6 个负例测试，每个断言错误消息含**具体字段名与非法值**；1 个正例全过 |
| G7 | 生成物↔bconf 无一致性校验 | `internal/codegen/appspec_contract.go`（新）+ 模板键改为描述符派生 | §3.5 Gate2/Gate3 | ① 生成期：把模板键故意改成 `server.htpp` → 生成失败并列出该键（回归锁）；② 运行期：生成物 + 笔误配置 → 启动报 `unknown contract keys` |
| G8 | `server.gateway` 死声明 | `appspec.proto:52` 保留字段，赋消费者：生成 `appkit.WithGatewayRegister`（`bootstrap.go:178`）+ 契约 `server.http.driver` 组合校验 | §3.3 + Gate1 第 4 条 | ① `TestAppSpecDescriptorFieldsAllConsumed`（通用守卫）；② gateway on 的矩阵冒烟（G10） |
| G9 | 无结构级断言 | `app_test.go` 新增结构断言 + 生成物内 layering 测试 | 断言路径集合 + 生成物内 `layering_invariant_test.go`（形状抄 `bald-admin …/layering_invariant_test.go:34-69`，含显式跳过 `_test.go`） | 删掉模板里的 `internal/bootstrap/` 目录 → 测试红 |
| G10 | 无 gateway/多 server/多 kind 运行时覆盖 | `app_test.go` 的冒烟测试改矩阵 | {无传输, http, grpc, http+grpc} × {gateway on/off} × {heartbeat, db}；每例真实起进程 + 端口探测 + HTTP GET 断言 + SIGTERM 优雅退出断言 | 矩阵全绿；任一组合崩溃即红（取代现状单一 `Server: &ServerSpec{}`，`app_test.go:183`） |

---

## 7. 文档与实现不一致修正清单

| # | 文件:行 | 文档说法 | 修正为 |
|---|---|---|---|
| D1 | `代码生成工具设计.md:3,30` | AppSpec proto 在 `api/proto/bald/appspec/v1` | `bconf/proto/bald/appspec/v1/appspec.proto`（`Test-Path bald/api/proto/bald/appspec/v1/appspec.proto` = False） |
| D2 | `代码生成工具设计.md:33` | 生成代码在核心 `pkg/conf/gen/go/bald/appspec/v1` | `bconf/gen/go/bald/appspec/v1`，独立 module `github.com/kalandramo/bald/bconf`（`bconf/go.mod:1`；消费者 `app.go:12`） |
| D3 | `代码生成工具设计.md:41` | `gen proto` 产物「含 `PagingRequest`/`FilterExpr` 引用」 | 删 `FilterExpr`；模板只有 `PagingRequest`（`gen.go:47`）与 `PaginationResponseMeta`（`gen.go:52`） |
| D4 | `代码生成工具设计.md:106`、`用 bald CLI 起步新服务.md:105` | 对照样本 `_example/bald/_scratch/go-bald-admin.appspec.json` | 实为 `_example/bald/_scratch/appspec.sample.json`（该目录仅此一文件） |
| D5 | `用 bald CLI 起步新服务.md:128` | `--http.addr=:18080` | `--server.http.addr=:18080`（绑定前缀 `server.http`，`appspec_gen.go:124`） |
| D6 | `用 bald CLI 起步新服务.md:143` | `OnKeyChange("http.addr", ...)` | `appkit.OnKeyChange("server.http.addr", ...)`（`appspec_gen.go:130`） |
| D7 | `用 bald CLI 起步新服务.md:40` | `=> generated app (spec-driven): cmd/my-service/main.go` | 实际只输出 `generated: <path>`（`iostreams.go:52`） |
| **D8** | `用 bald CLI 起步新服务.md:49-52,113-120` | 配置示例写顶层 `http.addr` / `grpc.addr` | 改 `server.http.addr` / `server.grpc.addr`；**并新增一节「写错键名会怎样」**：现状静默回退到 spec 默认值（`DiscardUnknown: true`，`bconf/unmarshal.go:151-153`），目标形态下生成物开 `WithStrictContract` → 启动即报 `unknown contract keys`（`bootstrap/config/config.go:240-247`）。**这是「用户照抄会无声失效」的那条，优先级最高** |
| D9 | `代码生成工具设计.md:91` | 「`server.gateway` … 模板 TODO 改为生成该接线」 | 兑现它（本设计 G8），或删除该承诺 |
| D10 | `代码生成工具设计.md:1,37-44` | 三个子命令 | **文档正确**（`gen.go:82`）；是任务书的「四个子命令」有误，不改文档 |
| D11 | `用 bald CLI 起步新服务.md:181` | 「三个子命令均遵守不静默覆盖」 | 补「落盘前 gofmt 诊断写盘除外」**并**修掉越权直写（§5.3）；修完后该绝对说法才成立（现状 `appspec_gen.go:357-362`） |
| D12 | `用 bald CLI 起步新服务.md:145` | `server.NewHTTPServer` / `server.NewGRPCServerWithRegister` | 与模板 alias 对齐为 `httpserver.NewHTTPServer` / `grpcserver.NewGRPCServerWithRegister`（`appspec_gen.go:164,169`）。低危 |
| D13 | `代码生成工具设计.md:12,118`（§5 骨架边界表） | 边界只列「业务填充点」，未列「分层骨架 / 端到端示例链路」 | 按本设计补 §5 两行：分层骨架与示例链路**属于生成物**（目标形态要求），不是业务填充点 |
| **D14**（S3 未列） | `代码生成工具设计.md`（§2 表格）与 `app.go:142` | 文档/help 把 `--module` 描述为「业务 module 路径」 | 实际 `_ = o.Module` 被丢弃，`appspec_gen.go:35` 只写进注释；import 前缀硬编码 `github.com/kalandramo/bald/...`（`appspec_gen.go:55-69`）→ 生成物只适用于用 bald 核心模块路径的消费者。要么实现落地，要么 help 文案明确标「提示用」 |
| **D15**（S3 未列，是 C1 的更正） | `代码生成工具设计.md:46-49` | 「共用 `writeFileGuarded`…不静默覆盖」 | 补 `renderAppSpec` 的例外**精确行号** `appspec_gen.go:357-362`；同时更正 S3 的 G7 前提：`bconf.FindUnknownKeys` **已有消费者**（`bootstrap/config/config.go:241`），真实缺口是生成物未开 `WithStrictContract` |

---

## 8. 验收脚本设计

现状验收 = 「字符串锚点 + `go build` 通过 + 3 秒存活」（`app_test.go:41-49,63-88,222`），**无法回答四条判据中的任何一条**。目标：一个可在 CI 跑的脚本 `scripts/verify-genac.sh`（或 `internal/codegen/acceptance_test.go`）。

### 8.1 判据 1：新增域只动 3 文件、零装配修改

```bash
tmp=$(mktemp -d); cd "$tmp"
# T0 记时开始
bald gen app --spec appspec.json                 # 产出完整骨架（含 demo 域）
go mod init example.com/svc && go mod tidy
snapshot() { find . -name '*.go' -not -path './api/gen/*' -exec sha256sum {} \; | sort; }
snapshot > before.txt
bald gen domain dict --proto api/proto/dict/v1/dict.proto   # 唯一额外命令
go build ./... && go vet ./...
# 机器断言：变化文件集合 ⊆ 白名单
changed=$(diff <(cut -c68- before.txt) <(snapshot | cut -c68-) | grep '^[<>]' | sed 's/^[<>] //' | sort -u)
whitelist='internal/bootstrap/domains.gen.go internal/apiserver/domains.gen.go
internal/apiserver/handler/gin/dict.gen.go internal/apiserver/handler/grpc/dict.gen.go
internal/apiserver/handler/convert/dict.gen.go internal/security/authz/alias.gen.go
api/proto/dict/v1/dict.proto internal/apiserver/model/dict.go internal/apiserver/biz/v1/dict/dict.go'
# 断言：changed ⊆ whitelist 且 |changed ∩ 手写装配文件| == 0（手写装配文件集合已为空集）
```

**关键**：断言「不存在 `internal/apiserver/{server.go,bizset.go}`、`internal/bootstrap/bootstrap.go` 被修改」——因为这些文件在目标形态下**不存在**，diff 里出现即判据 1 不成立。

### 8.2 判据 2：装配行数不随域数量增长

```bash
# 装配物仅两个生成文件
assembly_lines() { cat internal/bootstrap/assemble.gen.go internal/apiserver/domains.gen.go | wc -l; }
L1=$(assembly_lines)                              # 1 个域
for d in d2 d3 d4 d5; do bald gen domain $d --proto api/proto/$d/v1/$d.proto; done
L5=$(assembly_lines)                              # 5 个域
# 断言：每域恰好贡献 1 行（一次 append / 一次 Mount 调用）
test $((L5 - L1)) -eq 4
# 断言：手写装配文件行数变化为 0（不存在手写装配文件）
test -z "$(git diff --name-only -- 'internal/apiserver/server.go' 'internal/bootstrap/bootstrap.go' 2>/dev/null)"
```

口径声明：判据 2 覆盖**生成物装配文件**；「框架内代码随能力域增长」（S1 §3.3：每新增一个资源域 ≈+200 行框架代码）不在本判据范围（S1 §7 已判定那是能力域而非业务域）。

### 8.3 判据 3：业务代码不 import 装配层

生成物内**自带**一份 `internal/apiserver/layering_invariant_test.go`（生成物，`owned`），新项目 `go test ./...` 即生效：

- 遍历 `internal/**/biz/**/*.go`（显式跳过 `_test.go`，同 `bald-admin …/layering_invariant_test.go:49-51`）；
- 断言 import 路径不含：`pkg/appkit`、`/bootstrap`、`internal/bootstrap`、`registry/`、`contrib/*/contract`、`pkg/appkit/...`；
- 允许：`pkg/store`、`pkg/contextx`、`berrors`、`bconf/gen/go/bald/store/v1`（口径按任务书：这些算业务 API）。

生成器侧另有 `TestGeneratedBizImportsAreBusinessOnly`：对模板产出的 biz 骨架文件做同一断言（防止模板自己引入装配层 import）。

### 8.4 判据 4：新服务 30 分钟内跑起来

```bash
T0=$(date +%s)
bald gen app --spec appspec.json && go mod tidy && go build ./...
mkdir -p configs && ./svc &          # 或 go run ./cmd/svc
sleep 2
curl -fsS localhost:8080/readyz                  # 断言 200
curl -fsS localhost:8080/v1/demo | jq -e .items  # 断言示例链路通（不是 3 秒存活）
kill -TERM %1; wait %1; echo "exit=$?"           # 断言 0
T1=$(date +%s); test $((T1-T0)) -le 1800
```

**取代**现状的「3 秒存活」（`app_test.go:222`）：存活断言无法区分「装配正确」与「装配了但链路不通」。新脚本断言**端到端可达**。计时数据需落 `docs/devel/zh-CN/` 的验收记录（现状无任何计时证据，S1 §7 判据 4 标「待考」）。

### 8.5 现有测试：保留 / 改造 / 补

| 测试 | 处置 | 理由 |
|---|---|---|
| `guard_test.go:20-79` 三例 `TestWriteFileGuarded_*` | **保留**（改签名以适配 `Ownership`） | 防覆盖底线，语义不变 |
| `guard_test.go:83-122` `TestGenProto_CommandGuard` | **保留** | 输出走 `Out` + 二次拒绝 + `--force` |
| `guard_test.go:125-147` `TestGenApp_CommandGuardForce` | **保留** | 命令级契约 |
| `app_test.go:232-263` `TestGenAppSpec_CommandOmitsName` | **保留** | CLI 参数层 UX |
| `app_test.go:31-59` `TestGenApp_TemplateFormats` | **改造** | 旧锚点针对 `appkit.New` 路径（G2 修完后必然失效）。注意 `store.UnregisterTenant("tenant_id")`（:56-58）这条断言对应的 Effect**本身可疑**：模板注释（`app.go:70-76`）说「配合业务 BeforeStart 里的 `store.RegisterTenant`」，但生成物从未执行正向注册 → 生成物带一条「无正向操作的逆操作」。G2 后该 Effect 应由声明派生或删除，断言一并重写 |
| `app_test.go:63-88` `TestGenApp_GeneratedCodeCompiles` | **改造** | 断言从「单文件编译」升级为「整目录 `go build ./...` + 结构断言」 |
| `app_test.go:93-139` `TestGenAppSpec_TemplateFormats` | **改造** | 两条 P0 断言（New 在 append 之后 :125-132、无 `.MountComponent(` :136-138）在 G2 下失去对象（不再有 `appkit.New` 与 `capOpts`），改为「恰好一次装配入口调用」+「无 `appkit.New(`」 |
| `app_test.go:143-171` `TestGenAppSpec_GeneratedCompiles` | **保留并扩展** | 矩阵从 2 例扩到 G10 的组合 |
| `app_test.go:177-227` `TestGenAppSpec_GeneratedRuns` | **改造**（升级为 G10 矩阵） | 现状只覆盖「无传输 + heartbeat」（:183-184），且是 3 秒存活断言 |
| **补** `TestGenStore_DefaultLayout` | 新增 | G4；`genStoreCmd` 现状零命令级测试 |
| **补** `TestGenAppSpec_ValidateRejects*`（6 例） | 新增 | G6 |
| **补** `TestGenAppSpec_ContractKeysMatchBconf` | 新增 | G7（模板键改成 `server.htpp` 必红） |
| **补** `TestAppSpecDescriptorFieldsAllConsumed` | 新增 | G8 通用守卫 |
| **补** `TestGenAppSpec_DirectoryLayout` | 新增 | G3/G9 |
| **补** `TestRenderAppSpec_GofmtFailureNeverTouchesTarget` | 新增 | §5.3 越权直写回归锁 |
| **补** `TestGenDomain_RewritesOnlyGeneratedFiles` | 新增 | 判据 1 的生成器侧版本 |
| **补** `TestGenAppSpec_AssemblyLineBudget` | 新增 | 判据 2（`domains.gen.go` 行数 = 1 + 域数） |

---

## 9. 未决问题

1. **appkit 单一装配入口的输入形状（阻塞性）**：生成物需要该入口同时接受「整棵 `bootstrap.v1.BootstrapConfig`」与「域描述符切片」。若目标模型只接受契约、域注册走独立注册表，则 `internal/bootstrap/domains.gen.go` 需改为生成「注册到域注册表」的调用，`assemble.gen.go` 形态随之变化。**需另一位设计者确认这一条接缝**，否则 §2.2 的两个文件模板要重写。
2. **`internal/apiserver` 还是顶层 `biz/`**：本设计沿用前者（迁移成本最低、可对抄 bald-admin）。若作者坚持顶层 `biz/`，改一个 `layout` 常量即可，但需接受「业务代码可被外部 module import」的边界代价。
3. **判据 1 的「3 文件」在「域需要自定义授权策略」时变 4**：本设计的默认派生（proto service/method → 策略 object 名）能否覆盖作者所需的授权粒度？
4. **`gen proto` 模板是否升级**：G5 依赖 `google.api.http` 注解来生成 REST 路由。现状模板（`gen.go:24-58`，31 行）**无注解、无 buf.validate、无 CRUD 六件套**，只有 List/Get。升级后模板约 60 行。作者是否接受「`gen proto` 产出更重但可直接跑通」？
5. **是否保留「模板模式」**：若 AppSpec 成为唯一真相源，`gen app <name>`（无 `--spec`）应删除或降级为 `bald new`（纯空骨架）。这是破坏性变更（`app.go:155-158` 的分支 + 2 个测试），需作者决定。
6. **`--module` 落地**：现状被丢弃（`app.go:142`），import 前缀硬编码（`appspec_gen.go:55-69`）→ 生成物只适用于 bald 核心模块路径的消费者。落地需重写 import 前缀 + 生成后提示 `go mod tidy`。
7. **判据 2 的口径**：本设计按「生成物装配文件」；若作者口径含「框架内代码」，则 S1 §3.3 的「每资源域 ≈+200 行框架代码」会把判据 2 判为不成立（那是能力域轴，非业务域轴）。
8. **`seeded` 文件的单调演进**：`api/proto/<d>/v1/<d>.proto` 生成一次后归用户；若生成器模板升级（如注解规范变化），已有项目**拿不到**升级。是否需要 `bald gen proto --upgrade` 之类的显式迁移命令？（设计判断：YAGNI，先不做，靠文档。）

---

## 10. 只读性与证据声明

- 未修改 `D:\code\konglingfei` 下任何文件；未执行 `go build`/`go test`/`buf generate`；无 git 写操作。
- 所有 `文件:行` 依据来自 `read`/`grep`/`glob` 工具输出（UTF-8 正确解码）；行数基线复核：`internal/codegen/app.go` 250 行、`appspec_gen.go` 368 行、`gen.go` 240 行、`app_test.go` 263 行、`guard_test.go` 148 行、`iostreams.go` 54 行、`bconf/proto/bald/appspec/v1/appspec.proto` 86 行、`bconf/unmarshal.go` 644 行、`bconf/proto/bootstrap/v1/server.proto` 474 行、`bootstrap/config/config.go` 581 行、`_example/bald/main.go` 441 行、`_example/bald/register_grpcgw.go` 213 行。
- 行数预算、模板行数、验收脚本阈值均为**设计判断**，已在正文逐处标注。
- 本机 `Get-Content` 对含中文源文件按非 UTF-8 解码（`register_nacos.go` 实测输出乱码）→ 全程改用 `read` / `[System.IO.File]::ReadAllText`。
