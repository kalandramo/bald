# 用 bald CLI 起步新服务（bald gen app）

> 定位：面向 **bald 使用者**（从零起步的开发者）的分步指南：安装 CLI → 写 AppSpec → 生成骨架 → 起服务 → 加域 → 接授权。框架内部设计见 `docs/devel/zh-CN/装配层评估.md`（§4.6 目标骨架、§11 端到端实测、§12 授权闭环）；本页只讲「怎么装、怎么写、怎么跑」。
> 形态基准：仓库示例 [`_example/bald`](https://github.com/kalandramo/bald/blob/main/_example/bald)——本页代码块均与该示例或生成器模板逐语句对账。
> 状态：已实现（本文以 `v0.25.0` 为版本锚；生成 → 拉依赖 → 编译 → 起服务 → `/readyz` 200 → 优雅退出的端到端实测 1m16.8s，见装配层评估 §11）。

---

## 1. 前置条件

- Go ≥ 1.27（bald `go.mod` 声明 `go 1.27.1`）。
- 网络可达 `https://goproxy.cn`（国内默认可用）或 `proxy.golang.org`。
- 一个 Go module 项目（下文以 `example.com/my-service` 为例，按需替换）。
- 服务本身只需 bald 库；**代码生成器是独立 CLI**，不随业务代码引入。

## 2. 安装 CLI

```bash
go install github.com/kalandramo/bald/cmd/bald@v0.25.0
bald --help   # 验证安装
```

- `@latest` 解析最新 release tag（本文锚定 `v0.25.0`；固定版本用 `@v0.25.0`）。
- 新 tag 发布后 sum.golang.org 校验和收录有延迟（数小时），期间安装可能报 `verifying module ...: 404 Not Found`——属正常现象，稍后重试即可。
- CLI 提供 4 个生成子命令：`gen app`（装配骨架目录树）、`gen domain`（域驱动生成物）、`gen proto`（CRUD proto 骨架）、`gen store`（实体骨架）。

## 3. AppSpec：装配的单一真相源

AppSpec 用一份 JSON 声明应用的装配形态——开通哪些 server、挂哪些组件、声明哪些能力依赖、带哪些业务域。它是**唯一真相源**：改装配先改 AppSpec 再重新生成，不要手改生成文件。

### 3.1 字段参考（JSON 字段名即 protojson 形式）

| 字段 | 类型 | 说明 |
|---|---|---|
| `meta.name` | string | 应用/命令名：决定 `cmd/<name>/` 与 `configs/<name>.yaml`；契约 `app.name` 缺省取它 |
| `meta.module` | string | 你的 Go module 路径（生成文件 import 前缀，必填） |
| `meta.desc` | string | 应用短描述（落在 command.go 的 Short 与文件头） |
| `server.http` / `server.grpc` | bool | [快捷方式] 开通 HTTP / gRPC 面 |
| `server.gateway` | bool | [快捷方式] 开通 grpc-gateway 转码面（投影 = `server.http.driver="grpc-gateway"`，同端口单面语义） |
| `server.httpAddr` / `server.grpcAddr` | string | [快捷方式] 监听地址（投影 = 契约 `server.http.addr` / `server.grpc.addr`） |
| `components[]` | 数组 | `{kind, name, configPrefix, params}`；kind 白名单当前仅 `heartbeat`（Gate1 生成期校验） |
| `capability.provides[]` | string[] | 本进程提供的能力（S1 启动期 fail-fast 校验） |
| `capability.requires[]` | 数组 | `{component, caps[]}`：组件对能力的依赖；`caps` ⊆ `provides`（Gate1 校验） |
| `auditBackends[]` | string[] | [快捷方式] 审计后端期望态（投影 = `bootstrap.audit.backends`，含顺序） |
| `bundleNormalized` | bool | [快捷方式] 归一化中间件链（推荐改用 `bundle.normalized`） |
| `bootstrap` | 对象 | bconf 契约段（15 段整棵带入）——**唯一真相源**；生成物 `configs/<app>.yaml` 由它经 protoreflect 派生 |
| `layout.root` | string | 产出根目录（默认 `.`） |
| `layout.apiserverDir` | string | apiserver 目录名（默认 `apiserver`） |
| `layout.strictContract` | bool | 生成物是否 emit `appkit.StrictContract()`（proto3 optional 保 presence：未声明 = **true**） |
| `bundle.normalized` | bool | 装配期 bundle 归一化声明（audit object/action 双传输同源） |
| `domains[]` | 数组 | 显式域清单 `{name, protoPackage, service, model, goPackage}`（§6） |
| `variants[]` | 数组 | 构建期变体 `{name, build_tags[], description}`：声明恒进计划（`--plan`/`doctor` 的 `variants:` 段，按 `declared`/`active` 列出），命中的变体另产 `ambient` 诊断（Subject = `variant:<name>`）；只做可见性、不改变装配 |

快捷方式纪律：`bootstrap` 为空时，快捷方式归一化进契约投影；`bootstrap` 非空时，任一非零快捷值与其投影不一致即 Gate1 报错——消灭双真相源，不做静默覆盖。

### 3.2 示例

最小可跑：

```json
{
  "meta": {
    "name": "my-service",
    "module": "example.com/my-service",
    "desc": "演示服务"
  },
  "server": { "http": true, "grpc": true, "httpAddr": ":8080", "grpcAddr": ":9090" }
}
```

带组件/能力/审计的完整形态见 `_example/bald/_scratch/appspec.sample.json`；带业务域的声明见 `_example/bald/appspec.json`（greet 域）。

## 4. gen app：生成目标目录树

```bash
bald gen app --spec appspec.json   # spec 模式下 <name> 可省略，一切以 AppSpec 为准
```

flag：`--spec`（AppSpec JSON 路径）、`--out`（产出根，默认 `layout.root` 或 `.`）、`--force`（覆盖已存在的生成物）、`--module`（提示用）。无 `--spec` 时是单文件模板模式（`gen app <name>`，含 `[FILL]` 填充点）——起步推荐 spec 模式。

### 4.1 三道门

落盘前依次过三道门：

- **Gate1（schema 门）**：`meta.name`/`meta.module` 必填、组件 kind 白名单、`requires[].caps ⊆ provides`、域 `name` 唯一 + `go_package` 必填 + import 别名不冲突、快捷方式与 bootstrap 投影一致。报错逐条指明字段路径与非法值。
- **Gate2（契约门）**：`configs/<app>.yaml` 的键由 proto 字段描述符派生（模板不写任何裸配置键）并回读复检——生成物配置键在结构上错不了。
- **Gate3（运行期契约门）**：生成物 emit `appkit.StrictContract()`（`layout.strictContract` 默认 true），未知契约键启动即报。

### 4.2 生成物树与归属三态

```text
my-service/
├─ appspec.json                                    # 用户物：声明真相源，生成器永不读写
├─ cmd/my-service/main.go                          # 生成物：≤25 行，装配知识为零
├─ cmd/my-service/app/command.go                   # 生成物：cobra 根 + 唯一 MustServe 调用（serve / doctor / version）
├─ internal/bootstrap/assemble.gen.go              # 生成物：Options() 是装配的全部
├─ internal/bootstrap/domains.gen.go               # 生成物：业务域清单（暂空，占位）
├─ internal/bootstrap/variants.gen.go              # 生成物：仅 appspec.variants 非空时落盘（build tag 变体 → ambient 诊断）
├─ internal/bootstrap/seed/seed.go                 # 生成一次：演示种子数据
├─ internal/bootstrap/seed/policies.go             # 生成一次：默认授权策略行
├─ internal/apiserver/apiserver.go                 # 生成一次：HTTP 装配钩子（加业务路由处）
├─ internal/apiserver/domains.gen.go               # 生成物：域声明对账清单
├─ internal/apiserver/layering_invariant_test.go   # 生成物：biz 层不 import 装配层的守卫测试
├─ api/buf.yaml / api/buf.gen.yaml                 # 生成物：buf 模块与生成配置
├─ configs/my-service.yaml                         # 生成物：契约配置（由 AppSpec 派生）
└─ taskfile.yml                                    # 生成物：task run / verify / proto
```

入口链只有三跳：`main.go`（≤25 行硬预算，预算表见 `internal/codegen/appspec_budget_test.go`）→ `app.Execute()`（cobra 根）→ `appkit.MustServe(context.Background(), bootstrap.Options()...)`。装配知识全部在 `internal/bootstrap/assemble.gen.go` 的 `Options()`：`std.Standard()`（server/audit/logger 三类框架自有后端的段驱动 Provider）+ `ConfigFile`/`WatchConfigFile` + `appkit.StrictContract()` + 传输面能力声明（`WithGRPC`/`WithHTTP`/`WithGatewayRegister`，按声明裁剪）+ `WithHealth`（双探针 + gRPC health 联动）+ `mech.Provides/Requires`（能力）+ `mech.Components`（组件）+ `domainOptions`（域清单变参直传）。

归属三态语义：**生成物（owned）**——每次重写，已存在时需 `--force`；**生成一次（seeded）**——`seed/` 与 `apiserver.go`，不存在才落盘（重生成输出 `kept:`，`--force` 也不覆盖），此后归你维护；**用户物**——`appspec.json` 与业务代码，生成器永不读写。

### 4.3 rerun：改声明，不改手写

```bash
vim appspec.json
bald gen app --spec appspec.json --force   # owned 重写；seeded 输出 kept: 跳过；用户物不动
```

## 5. 起服务

```bash
go get github.com/kalandramo/bald@v0.25.0
go mod tidy
go run ./cmd/my-service
```

- **配置**：`configs/my-service.yaml` 已由 AppSpec 派生（`app.name` 缺省取 `meta.name` 写入）。四源覆盖同一条键路径：flag `--server.http.addr=:18080` ⇔ 环境变量 `MY_SERVICE_SERVER_HTTP_ADDR` ⇔ yaml `server.http.addr`（优先级 flag > env > 本地文件 > 远程；详见[Bald 配置系统](Bald%20配置系统.md)）。
- **探针**：`/healthz`（存活）与 `/readyz`（就绪，依赖未就绪 503）由框架默认挂载，数据源是生成物的 `healthChecker`。
- **业务端点**：加在 `internal/apiserver/apiserver.go` 的 `NewRouter`（生成一次、归你维护）。
- **启动日志**：引擎打印 `appkit starting` → `appkit started`（声明组件后还有 `appkit component started`；注册中心生效时还有 `appkit registered`，含最终 endpoints）。
- **启动前审阅**：`go run ./cmd/my-service --plan` 打印装配计划（节点/相位/依赖/段覆盖/hash）；`go run ./cmd/my-service doctor` 输出逃逸舱诊断。两者与真实启动同源、只读，详见[装配计划与 doctor](Bald%20装配计划与%20doctor.md)。
- **计划基线**：生成物默认声明 `appkit.WithPlanCache(".bald/plan-cache.json")`——真实启动记录计划基线，`doctor` 报告相对上次的节点级差异。`.bald/` 是本地目录，记得进 `.gitignore`。

端到端实测基线：gen → tidy → build → 起服务 → `GET /readyz` 200 → 优雅退出共 1m16.8s（装配层评估 §11；`internal/codegen/appspec_timed_test.go` 可复跑）。

## 6. gen domain：新增业务域

一个业务域 = 手写两样 + 一行声明：

1. **域 proto**：落在 `api/proto`、`api/protos` 或 `proto` 任一候选根下（`google.api.http` 注解会被派生成 REST 路由表与授权动作投影）。
2. **域包**：在 `domains[].goPackage` 指向的包里导出 `appkit.Domain` 声明值——三个钩子由引擎在既定时点消费：`Wire`（读自己的配置段、构造能力）、`Routes`（REST 路由自持）、`Services`（gRPC 注册自持）。逐行范本：[`_example/bald/greetv1/domain.go`](https://github.com/kalandramo/bald/blob/main/_example/bald/greetv1/domain.go)。
3. **声明**：在 AppSpec 的 `domains[]` 加 `{name, protoPackage, service, model, goPackage}`，`service` 必须是实际 proto 的 `<package>.<Service>` 全名。

然后生成：

```bash
bald gen app --spec appspec.json --force    # 重写 owned：默认策略行等随域声明更新（seeded kept）
bald gen domain --spec appspec.json --force # 重写域清单与逐域扇出
```

`gen domain` 产物（owned，重写需 `--force`）：

| 产物 | 作用 |
|---|---|
| `internal/bootstrap/domains.gen.go` | 域清单聚合（`appkit.Domain` 值变参直传 MustServe；与 gen app 共用同一模板） |
| `internal/apiserver/domains.gen.go` | 逐域 handler 挂载扇出（`DomainHandlers`） |
| `internal/apiserver/handler/grpc/<d>.gen.go` | gRPC 注册委托 |
| `internal/apiserver/handler/gin/<d>.gen.go` | REST 挂载委托 + 注解派生路由表 |
| `internal/apiserver/handler/convert/<d>.gen.go` | 授权动作投影（Delete→delete、List→list、Create/Update/Set/Put→write、其余→get） |
| `internal/apiserver/authz/alias.gen.go` | 授权别名表 + 默认动作聚合（proto service 派生） |
| `<go_package>/domain.gen.go` | 域声明值骨架（**生成一次**：域包已有导出 `Domain` 时跳过——既有域包如 `_example/bald/greetv1` 不会被覆盖） |

rerun 语义：新增域 = 手写 proto + 域包 + 改 `domains[]` + 上面两条命令——装配文件改动为 0（域清单存在的意义）。Gate1 会校验 `domains[].service` 与产出根下实际 proto 匹配（扫描 `api/proto`、`api/protos`、`proto`），失配报错指明哪个域、哪个 proto 文件、实际声明了什么。详细展开见[新增业务域 - Domain 与 gen domain](新增业务域%20-%20Domain%20与%20gen%20domain.md)。

## 7. gen proto / gen store：契约骨架

```bash
bald gen proto greet   # → api/proto/greet/v1/greet.proto
bald gen store greet   # → ./greet.go（gorm tag + keyOf）
```

`gen proto` 产出 **CRUD 六件套**：service + List/Get/Create/Update/Delete 五个 RPC，每个 RPC 带 `google.api.http` 注解（同一个 RPC 同时暴露 gRPC 与 REST/JSON），`buf.validate` 字段校验规则，List 请求带 `bald.store.v1` 的分页与结构化过滤（`PagingRequest`/`FilterExpr`）。proto 包名用 `<name>.v1`，与 `domains[].protoPackage` 的声明口径一致（`gen domain` 的 service 校验以此对账）。常用 flag：`--out`（默认 `api/proto/<name>/v1`）、`--plural`（REST 资源段，默认 `<name>s`）、`--go-package`（缺省由 `--module` 派生）、`--module`、`--force`。

生成后把 proto 纳入 buf 模块跑 `buf generate` 得到 Go 代码（`api/buf.yaml`/`api/buf.gen.yaml` 已由 gen app 生成；deps 需 googleapis、protovalidate 与 bald store 模块）——`taskfile.yml` 的 `task proto` 就是这一步。全部生成命令遵守**不静默覆盖**：目标已存在时拒绝并提示，`--force` 显式放行。

## 8. 授权接线（生成物数据 + 一个桥）

生成物已把授权的**数据面**备好，你只写一个桥：

- `internal/bootstrap/seed/policies.go`：默认策略行，从域 proto service 派生（object = service 全名去 `Service` 后缀转小写）：`admin` 覆盖六元组 get/list/write/delete/post/put，`viewer` 只读 get/list。只写 REST 四动词的历史教训是补 gRPC 面时 403——生成物已按六元组预置。
- `internal/apiserver/authz/alias.gen.go`：`ObjectAliases` 别名表（仅列 gRPC/HTTP 两侧归一化不一致的域）+ `DefaultActions` 逐 RPC 动作聚合。

装载经 `contrib/authz-casbin`（生成物只产数据、桥产行为），以下逐语句对照真实接线范本 `internal/codegen/testdata/authz/bridge.go.tmpl`：

```go
// internal/apiserver/authz/bridge.go（package authz，与 alias.gen.go 同包，ObjectAliases 直接可见）
func NewAuthorizer() (baldauthz.Authorizer, error) {
	rows := make([]authzcasbin.PolicyRow, 0, len(seed.Rows))
	for _, r := range seed.Rows {
		rows = append(rows, authzcasbin.PolicyRow{Role: r.Role, Object: r.Object, Action: r.Action})
	}
	grants := []authzcasbin.Grant{
		{Subject: "u-admin", Role: "admin"},   // g 行（subject→角色）是运行期业务数据——
		{Subject: "u-viewer", Role: "viewer"}, // 真实业务从 DB 装载并随授权变更 Reload
	}
	az, err := authzcasbin.NewRows(rows, grants) // p 行策略 + g 行绑定
	if err != nil {
		return nil, err
	}
	return authzcasbin.NewAlias(az, ObjectAliases), nil // 双向别名桥接（HTTP 面 greets ⇄ 策略行 greet）
}
```

再把桥挂进 `internal/apiserver/apiserver.go` 的 `NewRouter`（对照范本 `internal/codegen/testdata/authz/apiserver.go.tmpl`）：

```go
authorizer, err := demoauthz.NewAuthorizer() // demoauthz = <module>/internal/apiserver/authz
if err != nil {
	panic(err) // 策略装载失败启动即崩（fail-fast；真实业务在此记日志并退出）
}
router.Use(bundle.New(
	bundle.Normalized(),
	bundle.Authn(demoAuthn{}),   // 认证：接你的用户系统（范本 demo_authn.go.tmpl）
	bundle.Authz(authorizer),
).Gin()...)
```

端到端三态实测（装配层评估 §12，`TestTree_AuthzClosedLoop`）：`POST /v1/greets` 无 token **401**（authn 层拒绝）→ viewer **403**（策略行只有 get/list）→ admin **200**（六元组含 post）；403→200 的放行必经别名反查，三态全部由「生成物数据 + contrib 行为」产生，handler 无权限判断。语义要点：只装 p 行不装 g 行恒拒绝（默认拒绝语义，非错误）。更多细节见[Bald 授权使用](Bald%20授权使用.md)。

## 9. 故障排查

| 症状 | 原因与处理 |
|---|---|
| `Gate1 (spec schema): meta.name is required ...` | AppSpec 缺必填字段；报错逐条列出（含字段路径），按条补齐 |
| `components[0].kind "db" is not supported (whitelist: heartbeat)` | kind 白名单当前仅 `heartbeat`；db/redis 等经业务 Provider 接入（见[Provider 接入 - contract.AppProvider 与段 Provider](Provider%20接入%20-%20contract.AppProvider%20与段%20Provider.md)） |
| `Gate2 (contract keys ...): contract keys unknown to bconf (typo? ...)` | `AppSpec.bootstrap` 里有契约外的键；改键名或删除 |
| `config: unknown contract keys (typo? or declare in AllowBusinessSections): <keys>` | 运行期 StrictContract 拦到未知键（配置文件/env/flag 均在检查范围）；改键名，业务自持段经 `appkit.StrictContract("<段名>")` 白名单放行 |
| `unresolved capabilities: capability "db" (required by audit.store)` | `capability.requires` 声明的能力不在 `provides`；补齐声明 |
| `<path> already exists, refusing to overwrite (pass --force to allow)` | 生成物已存在；确认手改已备份后加 `--force` |
| 输出 `kept:` 但文件没更新 | 该文件是生成一次（`seed/`、`apiserver.go`、域骨架）——`--force` 也不覆盖，直接手改 |
| `Gate1 (domains.service vs proto): service "..." not found ...` | `domains[].service` 与实际 proto 失配；按报错对齐声明或 proto（§6） |
| `go install` 报 sumdb 404 | 新 tag 校验和收录延迟，数小时后重试（§2） |

## 10. 下一步

- [快速入门](./quickstart/README.md)：五分钟跑通最小服务。
- [新增业务域 - Domain 与 gen domain](新增业务域%20-%20Domain%20与%20gen%20domain.md)：域包三文件结构与 gen domain 全解。
- [Provider 接入 - contract.AppProvider 与段 Provider](Provider%20接入%20-%20contract.AppProvider%20与段%20Provider.md)：接数据库等第三方后端。
- [机制层 mech - Effect 与期望态](机制层%20mech%20-%20Effect%20与期望态.md)：能力/组件/热更新的机制层原语。
- [Bald 授权使用](Bald%20授权使用.md)：授权模型、casbin 桥与策略管理。
