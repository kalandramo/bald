# 新增业务域 - Domain 与 gen domain

面向要给 bald 服务新增一个业务域的人：域声明值怎么写、三个槽位各在什么时点被引擎消费、`bald gen domain` 重写哪些生成物、报错了怎么修。设计论证（为什么域清单必须由生成文件承载、为什么 appkit 不新增注册表式 API）见 `docs/devel/zh-CN/装配层评估.md` §4.3/§4.6，本文只讲操作。主线一句话（批 3 收口判据①原文）：**演示域 = proto + greet.go + domain.go 共 3 手写文件，gen domain 重写清单，装配改动 0**。

## 心智模型：一句话版

**域是一等 Option：每个域包导出一个 `appkit.Domain` 声明值，变参直传 `appkit.MustServe`；域清单的承载者是生成文件 `domains.gen.go`（Go 无运行期包发现，导入清单必须有承载者），所以新增域只手写 proto 与域包两个文件，清单交给 `bald gen domain` 重写——装配文件一行不动，`init()` 自注册禁止。**

| | 你手写 | 生成器重写 | 装配（main.go） |
| --- | --- | --- | --- |
| 触碰 | `api/proto/<域>/v1/<域>.proto`、域包 `<域>v1/{<域>.go, domain.go}` | `domains.gen.go`×2、`handler/{grpc,gin,convert}/<域>.gen.go`、`authz/alias.gen.go` | **0 处** |
| 频率 | 每域一次 | 域清单或域 proto 变化后重跑 | 不变 |

## 1. Domain 声明值：Declare + 三个兼容钩子

`appkit.Domain` 定义于 `pkg/appkit/domain.go`：Name 必填且全应用唯一；四个字段全部可选（nil = 该域不声明该面/该钩子）。

| 槽位 | 签名 | 引擎消费时点 |
| --- | --- | --- |
| `Declare` | `func(ctx context.Context, s PlanScope) ([]Node, error)` | **计划期纯函数**（不构造实例）：返回的节点进入装配计划且非 Opaque，声明域自有资源、依赖与重载策略。Phase 必须落在 wire 或其后（configure/bootstrap 归框架 Provider），Key 必填且全应用唯一 |
| `Wire` | `func(ctx context.Context, app *App) error` | 域装配钩子（业务 BeforeStart 之后、server 构建之前），带 ctx；此时资源面已就绪（`storegorm.SQL.Resource(app)` 等类型化句柄可取阶段 B 产物），业务钩子建立的运行期资源也可消费 |
| `Routes` | `func(h http.Handler) error` | Wire 之后、server 构建之前；参数是 WithHTTP 声明的业务 handler（gin 场景即 `*gin.Engine`，路由器耦合域自持断言）；gateway 转码面占用 server.http 端口时整体让位（不调用——同端口单面语义） |
| `Services` | `func(s *grpc.Server)` | 并入 server 构建的注册回调（业务注册在前、域按声明序在后）；调用发生在迟后阶段 server Provider 构建时——彼时 Wire 已完成，域端口就绪 |

**Declare 与兼容钩子的关系**（Domain v2 迁移期形态，设计 §4.6）：`Declare` 只声明拓扑，构造仍由 Wire/Routes/Services 完成。声明了 `Declare` 的域，引擎不再逐槽位合成 Opaque 节点，而是把它们塌缩成**一个** `domain.<name>.wire` Opaque 节点——`--plan` 能看到 non-opaque 的 routes/services 节点与依赖，`bald doctor` 的 `opaque` 清单则如实指出构造仍不透明。省略 `Declare` 的域保持既有逐槽位投影（`domain.<name>` / `.routes` / `.services` 三个 Opaque 节点）。

```go
// 节选 _example/bald/greetv1/domain.go
var Domain = appkit.Domain{
    Name:     "greet",
    Declare:  declare,   // 计划可检视：routes/services 节点 + dependsOn domain.greet.wire
    Wire:     wire,
    Routes:   registerRoutes,
    Services: registerService,
}
```

完整装配时序（`domain.go` 包注释钉住，wiring_order 测试族断言）：框架钩子（契约装载 + 阶段 B Provider 循环 + 段覆盖检查）→ 业务 BeforeStart → 逐域 Wire → Routes（多域按声明序）→ 迟后阶段（server Provider 构建时调 Services）。任一域 Wire/Routes 失败 fail-fast（错误带域名），已建资源经 Run 的回滚账本回收。

声明期校验（构造期 fail-fast，先于阶段 A 一切副作用）：Name 为空或重复报错；声明了 Routes 而应用未声明 HTTP 面（WithHTTP/WithGatewayRegister 至少其一）报错；声明了 Services 而契约 server.grpc 段缺失报错。报错原文见 §5。

没有的槽位同样是设计决定：迁移不做槽位（appkit 不 import 存储栈——需要迁移的域在 Wire 内经类型化句柄 `storegorm.SQL.Resource(app)` 取连接自行执行）；ConfigKeys 不做槽位（业务配置段只进 Settings 快照、不进契约，与 L3 段覆盖检查无交互）。

## 2. 手写三个文件（可对账范本：_example/bald/greetv1）

greet 域是 bald 仓里的可运行范本（`_example/bald/greetv1/`，e2e 护航），逐文件摘编对账。

**文件一：`api/proto/<域>/v1/<域>.proto`（契约）**——package / go_package、service、google.api.http 注解、buf.validate 规则。骨架可先由 `bald gen proto <域>` 生成（见 [用 bald CLI 起步新服务](./用%20bald%20CLI%20起步新服务.md) §8），注解随后手写维护。greet 的范本（`_example/bald/proto/greet.proto`）摘编：

```proto
service GreetService {
  rpc Greet(GreetRequest) returns (GreetResponse) {
    option (google.api.http) = { post: "/v1/greet" body: "*" };
  }
  rpc GreetGet(GreetGetRequest) returns (GreetResponse) {
    option (google.api.http) = { get: "/v1/greet/{name}" };
  }
}
```

注解是生成器的派生源：gen domain 从这里派生 REST 路由表与授权动作（§4）。

**文件二：域包 `<域>v1/<域>.go`（biz/model + 传输适配）**——域配置读取、能力构造、gRPC 与 REST 两面共用同一 biz（`greetv1/greet.go` 摘编）：

```go
// 域配置段：从 Settings 快照的 biz.greet.* 子树读取（业务段只进 Settings、
// 不进契约，与 L3 覆盖检查无交互）。顶层 biz: 是业务配置的统一命名空间，
// 域用 biz.<域名> 路径把子树声明进可协调节点的期望态（设计 Q8）。
func readOwnConfig(app *appkit.App) domainConfig { /* app.Settings()["biz"]["greet"] */ }

// 域端口：biz 节点构造并持有（能力策略住域，不住装配层）。端口用原子指针
// 整份交换——热更协调与请求读取并发，读方永远看到完整快照。
var port atomic.Pointer[greeter]

// wire 同时是 biz 节点的执行体与热更协调体：按新快照重建端口，幂等。
func wire(_ context.Context, app *appkit.App) error {
    cfg := readOwnConfig(app)
    port.Store(&greeter{greeting: cfg.greeting})
    return nil
}

// gRPC 面：嵌入 UnimplementedGreetServiceServer（protoc-gen-go-grpc 默认开启
// require_unimplemented_servers），service 实现的端口由 Wire 构造。
func registerService(s *grpc.Server) { baldv1.RegisterGreetServiceServer(s, &greetService{}) }

// REST 面：参数是 WithHTTP 声明的 handler；路由器耦合域自持断言。
func registerRoutes(h http.Handler) error {
    e, ok := h.(*gin.Engine)
    if !ok {
        return fmt.Errorf("greetv1: HTTP face is %T, want *gin.Engine (the domain owns its router coupling)", h)
    }
    v1 := e.Group("/v1", mid.CORS(mid.DefaultCORS()))
    v1.POST("/greet", ...) // web.HandleJSONRequest + berrors.BadRequest("EMPTY_NAME")
    return nil
}
```

**文件三：域包 `<域>v1/domain.go`（域声明值）**——全文就一个值。这是 bald-admin 域化（tenantd/userd 等样板域）定型的模板语义，_example 的 greetv1 是 bald 仓范本（`greetv1/domain.go`）：

```go
var Domain = appkit.Domain{
    Name:      "greet",
    Declare:   declare,      // 声明节点：biz 挂 biz.greet + ReloadReconcile，routes 挂 server.http
    ApplyNode: applyNode,    // 按计划序执行：biz 构造端口，routes 挂 REST 路由
    Reconcile: reconcileNode,// 配置热更：biz 节点按新快照重建端口
    Services:  registerService, // 延迟注册：server 构建时注册 gRPC service
}
```

声明里最关键的一条：`domain.greet.biz` 的 `Segments: []string{"biz.greet"}` + `Reload: appkit.ReloadReconcile`。计划期从合并 Settings 快照对该子树算期望态摘要，`biz.greet` 一变即按节点 Key 调 `Reconcile`——业务配置热更不再只有重启一条路。框架段（如 routes 的 `server.http`）与业务段（`biz.<path>`）可同时出现在一个域里。

## 3. 域声明值进装配：清单归生成器

装配层不出现任何具体域名——域清单聚合全部声明值后变参直传（`_example/bald/internal/bootstrap/assemble.go` 的 `opts = append(opts, domainOptionsForRole(role)...)`）：

```go
// domains.gen.go（bald gen domain 生成）：
var domainOptions = []appkit.Option{
    greetv1.Domain,
}
```

清单显式可 grep：import 树里有什么，这里就列什么。新增域只改 appspec.json 的 domains[] 并重跑生成器（§4），装配零改动。

## 4. gen domain：重写域驱动生成物

```bash
bald gen domain --spec appspec.json [--adopt] [--proto-cmd "buf generate"]
```

`--spec` 必填（AppSpec.domains 是域清单唯一真相源）；`--out` 产出根（默认 `.`）；`--manifest` 清单路径（默认 `internal/bootstrap/domains.gen.go`，_example 根级形态传 `domains.gen.go`）；`--apiserver-dir` 默认取 layout.apiserver_dir 否则 apiserver，传 `-` 跳过 apiserver 侧产物；`--adopt` 放行覆盖 owned 产物；`--proto-cmd` 是绑定前置（proto 的 Go 类型）缺失时执行的 proto 生成命令（默认 `buf generate`，在 `--out` 根以 shell 语义执行）；`--no-bind` 跳过注解派生的绑定与路由清单；`--no-routes` 只跳过路由清单；`--rest-bind` 是已退役的兼容开关（等价于不给）。

**产物清单**（命令定义见 `internal/codegen/gen_domain.go`）：

| 产物 | 归属 | 说明 |
| --- | --- | --- |
| `internal/bootstrap/domains.gen.go` | owned | 域清单聚合（与 gen app 共用同一模板——单模板源不漂移） |
| `internal/<apiserver_dir>/domains.gen.go` | owned | 逐域 handler 挂载扇出（清单序 = 域装配次序） |
| `internal/<apiserver_dir>/handler/grpc/<域>.gen.go` | owned | gRPC 注册委托 |
| `internal/<apiserver_dir>/handler/gin/<域>.gen.go` | owned | REST 挂载委托 + google.api.http 注解派生路由表 |
| `internal/<apiserver_dir>/handler/gin/<域>.bind.gen.go` | owned | 注解派生绑定：`<Name>Service` 接口 + `Bind<Name>(gin.IRouter, impl)`（protojson 编解码；**默认生成**，`--no-bind` 跳过） |
| `internal/<apiserver_dir>/routes/<域>.gen.go` | owned | 同批注解的 `appkit.Route` 清单（域引用 `RouteTable: routes.<Name>` 即获计划可见性；`--no-routes` 跳过） |
| `internal/<apiserver_dir>/handler/convert/<域>.gen.go` | owned | 逐 RPC 授权动作投影（与 pkg/authz 默认规则同源） |
| `internal/<apiserver_dir>/authz/alias.gen.go` | owned | 授权别名表（仅列 gRPC/HTTP 两侧归一化不一致的域）+ 默认动作聚合 |
| `<go_package>/domain.gen.go` | seeded | 域声明值骨架：生成一次；域包已导出 Domain 时跳过（kept:），既有域包（如 greetv1）消费不覆盖 |

**绑定生成的前置（Q5 D1，默认开启）**：bind 文件 import proto 的 `go_package` 与 `github.com/kalandramo/bald/transport/web`。`go_package` 目录无 Go 文件时，生成器先在 `--out` 根执行 `--proto-cmd` 并复查产物——命令失败或仍无产物即生成期硬错误，错误点名命令、缺失包与 `--proto-cmd`/`--no-bind` 两条修法。`--check` 零写入，不执行外部命令，缺失时直接报修法。`transport/web` 缺失给 `go get` 修法（生成器不改消费方 `go.mod`）。绑定内容只由注解与 rpc 签名决定，与 proto 生成物内容无关：是否在本机跑过 buf 不改变生成字节。

**三态 writeGuarded 语义**（全部生成命令共用的落盘通道，`internal/codegen/iostreams.go:59`）：owned 每次重写，已存在且无 `--adopt` 一律拒绝（错误信息自带指引）；seeded 生成一次，已存在输出 `kept:` 并跳过（`--adopt` 也不覆盖——此后归你维护）；user 永不读写，仅输出 `skip:` 提示。proto 与域包手写文件属 user 侧；域包骨架是唯一的 seeded 产物——仅当域包尚无导出 Domain（按非测试 .go 文件结构判定）时落盘，Name 先挂上，三槽由你在手写文件里实现后挂进此值。

**两阶段清单**：gen app 先把 `internal/<apiserver_dir>/domains.gen.go` 写成预域形态（对账清单），gen domain 把它重写为逐域挂载扇出——所以**重跑 gen app 之后必须 rerun gen domain**，否则 apiserver 侧停留在预域形态（装配层评估 §11.5「gen domain 两阶段清单」，文档级行为）。

**何时重跑**：新增/删除域（改 domains[]）、改域 proto 的 service 或 http 注解（路由表、绑定与授权投影跟随派生）之后，`bald gen domain --spec appspec.json --adopt`。命令收尾提示即工作流原文：add a domain = hand-write its proto + domain package, declare it in domains[], rerun with --check to verify or --adopt to regenerate (assembly files stay untouched)。

AppSpec.domains 字段参考（protojson 字段名；范本 `_example/bald/appspec.json`）：`name` / `protoPackage` / `service` / `model` / `goPackage`。

**操作序列：新增一个 order 域**（对照 §2 的三个文件与上表产物）：

1. 手写契约 `api/proto/order/v1/order.proto`：service OrderService + google.api.http 注解（骨架可先 `bald gen proto order`）。
2. 手写域包 `orderv1/order.go`：域配置读取（Settings 快照 order.* 子树）、能力构造、gRPC/REST 两面。
3. appspec.json 的 domains[] 追加 `{"name": "order", "protoPackage": "order.v1", "service": "order.v1.OrderService", "model": "orderv1/order.go", "goPackage": "<module>/orderv1"}`。
4. `bald gen domain --spec appspec.json --adopt`：重写清单×2 + handler 三件 + alias.gen.go + 绑定/路由清单；proto Go 类型缺失时按 `--proto-cmd` 编排 proto 阶段（失败即硬错误，`--no-bind` 可跳过绑定）；域包无 Domain 时 seed `orderv1/domain.gen.go` 骨架。
5. 把三槽实现在域包手写文件里挂进声明值（先手写 domain.go 亦可——骨架 seeded 自动 kept: 跳过）。
6. 自检：`git status` 应只见手写文件与生成物变化——装配文件零 diff 即判据①达成。

## 5. 报错速查

**生成期（Gate1 强化：domains.service ↔ 实际 proto，`gen_domain.go:248`）**——扫描根为 api/proto、api/protos、proto（存在即扫，gen/ 生成目录跳过）：

- `Gate1 (domains.service vs proto): domains[0] (greet): service "bald.v1.GreetService" not found in proto package "bald.v1" (… declares: …)`——domains.service 必须是扫描根下某 proto 的 `<package>.<Service>`；报错列出该包实际声明，改声明或改 proto。
- `domains[0] (greet): proto package "bald.v2" not found (scanned roots: …; N proto file(s) declaring packages: …) -- domains.service must be <package>.<Service> of a proto under the scanned roots`——protoPackage 拼错，或 proto 不在扫描根下。
- `domains[0] (greet): service is required (field domains.service; gen domain derives handlers and authorization from it)`。

**运行期（域声明校验与执行）**：

- `appkit: duplicate domain name "greet" (each domain must be declared once)`——域名重复。
- `appkit: domain "greet" declares Routes but no HTTP face is declared (use appkit.WithHTTP or appkit.WithGatewayRegister)`。
- `appkit: domain "greet" declares Services but bootstrap.server.grpc is nil`。
- 域执行失败：`appkit: domain "greet": <原因>`（Wire）/ `appkit: domain "greet" routes: <原因>`——fail-fast，已建资源经回滚账本回收。

## 6. 多域协作规则

- **域间禁止互相 import**。业务能力级跨域访问走两条通道：被依赖域包导出访问器，依赖经 Deps getter 供给——装配面保留各域 Deps getter 实参，域在 Wire 里迟到取用。批 4 W1b 样板域（tenant/user）先行定型这套形态，bald-admin 21 域域化实证（共享依赖清单 BizSet 随之溶解为域 Wire 自取）。
- **基础设施住装配层**：数据库连接、缓存客户端等是装配层自持的抉择，域在 Wire 内经 `storegorm.SQL.Resource(app)`、`rediscontract.Redis.Resource(app)` 迟到取用（阶段 B 产物此时已就绪；资源面语义见 [Provider 接入](./Provider%20接入%20-%20contract.AppProvider%20与段%20Provider.md) §4）；迁移执行同理。
- **业务配置段只进 Settings 快照**：`app.Settings()` 读自己的 `<域>.*` 子树，不进契约、不触发 L3 覆盖检查。

## FAQ

- **新增域要动 main.go 吗？** 不。域清单是生成文件，声明值变参直传；main 装配面的域相关增量只有一次性的形态迁移（批 3 实测两行）。
- **重跑 gen app 后域产物退回预域形态？** 是两阶段设计：rerun gen domain 即恢复扇出形态（§4）。
- **域包已有 Domain 声明，gen domain 会覆盖吗？** 不会——骨架是 seeded 且仅当域包尚无导出 Domain 时落盘；greetv1 这类既有域包消费不覆盖。
- **域的 REST 路由挂在哪？** 挂上 WithHTTP 声明的业务 handler；gateway 转码面占用 server.http 端口时 Routes 整体让位（REST 走转码，同端口单面语义）。
- **三个槽位必须全实现吗？** 不。三槽全可选，nil 即不声明该面；但声明了 Routes 需应用有 HTTP 面、声明了 Services 需契约 server.grpc 段（§1）。

