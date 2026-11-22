# 新增业务域 - Domain 与 gen domain

面向要给 bald 服务新增一个业务域的人：域声明值怎么写、三个槽位各在什么时点被引擎消费、`bald gen domain` 重写哪些生成物、报错了怎么修。设计论证（为什么域清单必须由生成文件承载、为什么 appkit 不新增注册表式 API）见 [Bald 声明式装配设计](../../devel/zh-CN/Bald%20声明式装配设计.md) 的 Domain v2 一节，本文只讲操作。主线一句话：**演示域 = proto + greet.go + domain.go 共 3 手写文件，gen domain 重写清单，装配改动 0**。

## 心智模型：一句话版

**域是一等 Option：每个域包导出一个 `appkit.Domain` 声明值，变参直传 `appkit.MustServe`；域清单的承载者是生成文件 `domains.gen.go`（Go 无运行期包发现，导入清单必须有承载者），所以新增域只手写 proto 与域包两个文件，清单交给 `bald gen domain` 重写——装配文件一行不动，`init()` 自注册禁止。**

| | 你手写 | 生成器重写 | 装配（main.go） |
| --- | --- | --- | --- |
| 触碰 | `api/proto/<域>/v1/<域>.proto`、域包 `<域>v1/{<域>.go, domain.go}` | `domains.gen.go`、`handler/convert/<域>.gen.go`、`authz/alias.gen.go`（+ 默认生成的绑定与路由清单） | **0 处** |
| 频率 | 每域一次 | 域清单或域 proto 变化后重跑 | 不变 |

## 1. Domain 声明值：Declare + 三个兼容钩子

`appkit.Domain` 定义于 `pkg/appkit/domain.go`：Name 必填且全应用唯一；四个字段全部可选（nil = 该域不声明该面/该钩子）。

| 槽位 | 签名 | 引擎消费时点 |
| --- | --- | --- |
| `Declare` | `func(ctx context.Context, s PlanScope) ([]Node, error)` | **计划期纯函数**（不构造实例）：返回的节点进入装配计划且非 Opaque，声明域自有资源、依赖与重载策略。Phase 必须落在 wire 或其后（configure/bootstrap 归框架 Provider），Key 必填且全应用唯一 |
| `Wire` | `func(ctx context.Context, app *App) error` | 域装配钩子（业务 BeforeStart 之后、server 构建之前），带 ctx；此时资源面已就绪（`storegorm.SQL.Resource(app)` 等类型化句柄可取阶段 B 产物），业务钩子建立的运行期资源也可消费 |
| `Routes` | `func(h http.Handler) error` | Wire 之后、server 构建之前；参数是 HTTP 面的业务 handler——`WithHTTP` 直接声明的值，或 `WithHTTPContract` 在契约装载后求值出的值（gin 场景即 `*gin.Engine`，路由器耦合域自持断言）；gateway 转码面占用 server.http 端口时整体让位（不调用——同端口单面语义） |
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

声明期校验（构造期 fail-fast，先于阶段 A 一切副作用）：Name 为空或重复报错；声明了 Routes 而应用没有 HTTP 面报错——面的判据是 `hasHTTPFace`（`WithHTTP` 与 `WithHTTPContract` 任一即算）或接管 `server.http` 的转码 Provider；声明了 Services 而契约 server.grpc 段缺失报错。报错原文见 §5（提示语只举了 `appkit.WithHTTP` 这一种形态，契约钩子形态同样满足判据）。

没有的槽位同样是设计决定：迁移不做槽位（appkit 不 import 存储栈——需要迁移的域在 Wire 内经类型化句柄 `storegorm.SQL.Resource(app)` 取连接自行执行）；ConfigKeys 不做槽位（业务配置段只进 Settings 快照、不进契约，与 L3 段覆盖检查无交互）。

## 2. 手写三个文件（可对账范本：_example/bald/greetv1）

greet 域是 bald 仓里的可运行范本（`_example/bald/greetv1/`，e2e 护航），逐文件摘编对账。

**口径先说清**：「3 个手写文件」是**最小演示域**的计数（proto + 域声明 + 域包实现）。真实业务域会在 3 之外按需增加文件——model、store、handler 按 gin/gRPC 双面拆分等，都属正常形态；判据守的是「装配改动 0」与「清单由生成物承载」，不是文件数恰好 3。

**文件一：`api/proto/<域>/v1/<域>.proto`（契约）**——package / go_package、service、google.api.http 注解、buf.validate 规则。骨架可先由 `bald gen proto <域>` 生成（见 [用 bald CLI 起步新服务](./用%20bald%20CLI%20起步新服务.md) §8），注解随后手写维护。greet 的范本（`_example/bald/api/proto/greet.proto`）摘编：

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

// REST 面：gen domain 已从 google.api.http 注解生成 BindGreet。
// 绑定只做编解码、不跑校验；适配器复用域 Validator，REST 与 gRPC 同规则。
func registerRoutes(h http.Handler) error {
    e, ok := h.(*gin.Engine)
    if !ok {
        return fmt.Errorf("greetv1: HTTP face is %T, want *gin.Engine (the domain owns its router coupling)", h)
    }
    ginhandler.BindGreet(e, validatedService{inner: &greetService{}})
    return nil
}
```

`BindGreet` 与路由清单 `routes.Greet` 同源于注解：不想要绑定就传 `--no-bind` 手写整个 REST 面；只想要手写清单就传 `--no-routes`。

**文件三：域包 `<域>v1/domain.go`（域声明值）**——全文就一个值。这是 bald-admin 域化（tenantd/userd 等样板域）定型的模板语义，_example 的 greetv1 是 bald 仓范本（`greetv1/domain.go`）：

```go
var Domain = appkit.Domain{
    Name:      "greet",
    Declare:   declare,      // 声明节点：biz 挂 biz.greet + ReloadReconcile，routes 挂 server.http
    ApplyNode: applyNode,    // 按计划序执行：biz 构造端口，routes 挂 BindGreet
    Reconcile: reconcileNode,// 配置热更：biz 节点按新快照重建端口
    Services:  registerService, // 延迟注册：server 构建时注册 gRPC service
    RouteTable: routes.Greet,   // 生成清单：--plan/doctor 看见的就是 BindGreet 注册的
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

### 3.1 域依赖接线也归生成器：`domains[].deps`

清单归生成器只解决了「域值谁来引用」。域**依赖**（`Deps` 结构体那一组 getter）过去仍要在装配层逐域手写 `xxxd.Setup(xxxd.Deps{...})`——新增一个域就要动装配文件，§3 的「装配零改动」在这半边并不成立。

`appspec.json` 的 `domains[].deps` 把这段接线也搬进声明面：装配层只提供一个**固定字段集**的取值源 `DomainDepSources`，生成物 `domains.gen.go` 里的 `setupDeclaredDomainDeps` 按声明逐域拼装 `Setup`。

```json
{
  "name": "tenant",
  "goPackage": "example.com/app/internal/apiserver/domains/tenantd",
  "deps": [
    {
      "field": "Store",
      "id": "store.Tenant",
      "expr": "func() *store.Store[authmodel.Tenant] { return src.Store().Tenant }",
      "imports": [
        { "path": "github.com/kalandramo/bald/pkg/store" },
        { "path": "example.com/app/internal/apiserver/model", "alias": "authmodel" }
      ]
    },
    {
      "field": "Authn",
      "id": "authn",
      "expr": "func() authn.Authenticator { return src.Authn() }",
      "imports": [{ "path": "github.com/kalandramo/bald/pkg/authn" }]
    }
  ]
}
```

四个字段的分工：

| 字段 | 作用 | 生成器行为 |
| --- | --- | --- |
| `field` | 域 `Deps` 的结构体字段名 | 原样进 `Setup`；域内唯一，Gate1 校验导出标识符形状 |
| `id` | 稳定依赖标识（`store.Tenant` / `authn` / `authz`） | **不参与生成**——可读性与去重审计用；生成器因此不绑定任何应用特定的 model 命名 |
| `expr` | 完整 Go 闭包表达式，入参固定为 `src` | 纯文本搬运；类型由 `DomainDepSources` 字段类型在编译期校验 |
| `imports` | `expr` 引用到的 import（`path` + 可选 `alias`） | 去重、按 path 排序后进 import 块；同一 path 的 `alias` 必须全场一致——一个文件不能重复 import 同一 path |

`alias` 不是可选项里的锦上添花：Go 的包名未必等于路径末段（`oss/s3`→`s3oss`、`.../model`→`authmodel`、`redis/go-redis/v9`→`goredis`），也可能有同名包需要消歧。别名由声明面给，生成器只照搬。

装配层这边是一次性的、**与域数量无关**的固定形态：

```go
// internal/bootstrap/domain_dep_sources.go（手写；字段集固定，不随域增长）
type DomainDepSources struct {
    Store func() *Repositories
    Authn func() authn.Authenticator
    // …应用自己的取值源
}

// internal/bootstrap/assembly.go——全应用唯一的域接线调用
setupDeclaredDomainDeps(DomainDepSources{
    Store: func() *Repositories { return repos },
    Authn: func() authn.Authenticator { return authenticator },
})
```

三条纪律：

1. 全部字段是**迟到取值函数**而非实例：真实实例在 `BeforeStart` 里才构造，域 `Wire` 晚于它执行，届时读到已就绪实例。闭包内赋值必须用 `=`（`:=` 会新建局部变量，外层仍是 nil）。
2. `DomainDepSources` 的字段集不随域增长：新域复用已有取值源就只改 `appspec.json`；真出现新取值源才加一个字段——一次，之后所有域都能用。
3. 装配层必须**填满**：某域声明了某依赖而字段为 nil 不会编译失败，要到该域 `Wire` 时 nil 解引用才炸，堆栈还指向生成物。下游项目应加一条源码扫描守卫（`bald-admin` 的 `TestDomainWiring_SourcesFullyProvided` 是范本，同时用 `TestDomainWiring_IsDeclarationDriven` 守住「装配层不出现按域接线」）。
4. `deps` 与 `http_only` 正交：没有 proto service 的手写域（如登录域）声明 `http_only: true` 后一样能进清单、一样由 `setupDeclaredDomainDeps` 接线——这是把所有域都收进声明面的关键，否则总会剩下几个留在装配层手写。

## 4. gen domain：重写域驱动生成物

```bash
bald gen domain --spec appspec.json [--adopt] [--proto-cmd "buf generate"]
```

`--spec` 必填（AppSpec.domains 是域清单唯一真相源）；`--out` 产出根（默认 `.`）；`--manifest` 清单路径（默认 `internal/bootstrap/domains.gen.go`，_example 根级形态传 `domains.gen.go`）；`--apiserver-dir` 默认取 layout.apiserver_dir 否则 apiserver，传 `-` 跳过 apiserver 侧产物；`--adopt` 放行覆盖 owned 产物；`--proto-cmd` 是绑定前置（proto 的 Go 类型）缺失时执行的 proto 生成命令（默认 `buf generate`，在 `--out` 根以 shell 语义执行）；`--no-bind` 跳过注解派生的绑定与路由清单；`--no-routes` 只跳过路由清单；`--rest-bind` 是已退役的兼容开关（等价于不给）；`--grpc-skeleton` 为新域起 gRPC 骨架（seeded，一次）；gRPC 契约断言**默认生成**（声明了 `grpcBizPackage` 即产，owned，每次重生成，§4.1），`--no-grpc-contract` 关闭。

**产物清单**（命令定义见 `cli/internal/codegen/gen_domain.go`）：

| 产物 | 归属 | 说明 |
| --- | --- | --- |
| `internal/bootstrap/domains.gen.go` | owned | 域清单聚合（与 gen app 共用同一模板——单模板源不漂移） |
| `internal/<apiserver_dir>/handler/gin/<域>.bind.gen.go` | owned | 注解派生绑定：**每个 service 一份** `<Service 短名>` 接口 + `Bind<短名去 Service 后缀>(gin.IRouter, impl)`（`greet.v1.GreetService` → `GreetService` / `BindGreet`，单 service 域与旧产物同名）。一个域一个文件，`services[]` 的接口都在其中（protojson 编解码；**默认生成**，`--no-bind` 跳过） |
| `internal/<apiserver_dir>/routes/<域>.gen.go` | owned | 同批注解的 `appkit.Route` 清单，**按域聚合**（多 service 的路由合入同一张表；域引用 `RouteTable: routes.<Name>` 即获计划可见性；`--no-routes` 跳过） |
| `internal/<apiserver_dir>/handler/convert/<域>.gen.go` | owned | 逐 RPC 授权动作投影，按域聚合（与 pkg/authz 默认规则同源） |
| `internal/<apiserver_dir>/authz/alias.gen.go` | owned | 授权别名表（仅列 gRPC/HTTP 两侧归一化不一致的域）+ 默认动作聚合；http_only 域不在表内（`DefaultActions` 保持空表） |
| `<go_package>/domain.gen.go` | seeded | 域声明值骨架：生成一次；域包已导出 Domain 时跳过（kept:），既有域包（如 greetv1）消费不覆盖 |
| `internal/<apiserver_dir>/handler/grpc/<域>.grpc.gen.go` | seeded | gRPC service 骨架（`--grpc-skeleton`，需 `grpcBizPackage` + `grpcBizType`）：struct + 构造 + 每个 RPC 的方法签名 + `notFoundOr` 模板，方法体返回 `Unimplemented` 逼你逐个填；生成一次后归你 |
| `internal/<apiserver_dir>/handler/grpc/<域>.grpc_contract.gen.go` | owned | 编译期契约（**默认生成**，需 `grpcBizPackage`；`--no-grpc-contract` 关闭）：逐 RPC 方法集 + `var _ <impl>Contract = (*<impl>)(nil)`；proto 漂移即构建失败（§4.1） |

**绑定生成的前置（Q5 D1，默认开启）**：bind 文件 import proto 的 `go_package` 与 `github.com/kalandramo/bald/transport/web`。`go_package` 目录无 Go 文件时，生成器先在 `--out` 根执行 `--proto-cmd` 并复查产物——命令失败或仍无产物即生成期硬错误，错误点名命令、缺失包与 `--proto-cmd`/`--no-bind` 两条修法。`--check` 零写入，不执行外部命令，缺失时直接报修法。`transport/web` 缺失给 `go get` 修法（生成器不改消费方 `go.mod`）。绑定内容只由注解与 rpc 签名决定，与 proto 生成物内容无关：是否在本机跑过 buf 不改变生成字节。

**三态 writeGuarded 语义**（全部生成命令共用的落盘通道，`cli/internal/codegen/iostreams.go`）：owned 每次重写，已存在且无 `--adopt` 一律拒绝（错误信息自带指引）；seeded 生成一次，已存在输出 `kept:` 并跳过（`--adopt` 也不覆盖——此后归你维护）；user 永不读写，仅输出 `skip:` 提示。proto 与域包手写文件属 user 侧；域包骨架是唯一的 seeded 产物——仅当域包尚无导出 Domain（按非测试 .go 文件结构判定）时落盘，Name 先挂上，三槽由你在手写文件里实现后挂进此值。

**何时重跑**：新增/删除域（改 domains[]）、改域 proto 的 service 或 http 注解（路由表、绑定与授权投影跟随派生）之后，`bald gen domain --spec appspec.json --adopt`。命令收尾提示即工作流原文：add a domain = hand-write its proto + domain package, declare it in domains[], rerun with --check to verify or --adopt to regenerate (assembly files stay untouched)。

AppSpec.domains 字段参考（protojson 字段名；范本 `_example/bald/appspec.json`）：`name` / `goPackage` 必填，`protoPackage` 可选（缺省从 service 全名反推），声明形态三选一（Gate1 强制互斥）：

| 形态 | 声明 | gen domain 派生 |
| --- | --- | --- |
| 单 service | `service: "<package>.<Service>"` | convert / bind / routes / authz 全部按该 service 派生（默认形态） |
| 多 service 聚合 | `services: ["<package>.<Service>", …]` | 同域聚合：一个 bind 文件里每个 service 各一份接口与 Bind 函数，路由清单、convert 动作、authz 默认动作按域合并 |
| 纯 HTTP 域 | `httpOnly: true` | 不产 convert / bind / routes；`authz/alias.gen.go` 仍产但该域不进 `DefaultActions`；seed 策略只留说明注释。REST 面、授权与路由全部由域包的 `appkit.Domain` 自持 |

- `httpOnly` 与 `service` / `services` / `protoPackage` 互斥——纯 HTTP 域没有 proto 契约可派生，同时声明即 Gate1 报错。
- 非 `httpOnly` 域必须给出 `service` 或 `services`（否则生成器无从派生），且两者只能选一个。
- `services[]` 每项须是非空、无首尾空白的 `<package>.<Service>` 全名，域内不得重复；**短名也须唯一**——绑定的 Go 接口按短名命名，重名会撞接口。
- `services[]` 可跨 proto package：省略 `protoPackage` 时每个 service 各自从全名反推包名；声明了 `protoPackage` 则全部 service 必须属于该包。

**操作序列：新增一个 order 域**（对照 §2 的三个文件与上表产物）：

1. 手写契约 `api/proto/order/v1/order.proto`：service OrderService + google.api.http 注解（骨架可先 `bald gen proto order`）。
2. 手写域包 `orderv1/order.go`：域配置读取（Settings 快照 order.* 子树）、能力构造、gRPC/REST 两面。
3. appspec.json 的 domains[] 追加 `{"name": "order", "protoPackage": "order.v1", "service": "order.v1.OrderService", "goPackage": "<module>/orderv1"}`。
4. `bald gen domain --spec appspec.json --adopt`：重写域清单 + 授权投影 + alias.gen.go + 绑定/路由清单；proto Go 类型缺失时按 `--proto-cmd` 编排 proto 阶段（失败即硬错误，`--no-bind` 可跳过绑定）；域包无 Domain 时 seed `orderv1/domain.gen.go` 骨架。
5. 把三槽实现在域包手写文件里挂进声明值（先手写 domain.go 亦可——骨架 seeded 自动 kept: 跳过）。
6. 自检：`git status` 应只见手写文件与生成物变化——装配文件零 diff 即判据①达成。

另外两种声明形态的 appspec 片段：

```jsonc
// 一个域聚合多个 proto service：绑定产物里是 OrgService/BindOrg 与
// OrgMemberService/BindOrgMember，路由表仍只有一张 routes.Org。
{"name": "org", "services": ["org.v1.OrgService", "org.v1.OrgMemberService"], "goPackage": "<module>/internal/apiserver/orgv1"}

// 纯 HTTP 域：没有 proto service，REST handler 全部手写在域包里，
// appkit.Domain.Routes 自持路由；生成器只落域清单与域包骨架。
{"name": "dashboard", "httpOnly": true, "goPackage": "<module>/internal/apiserver/dashboardv1"}
```

### 4.1 gRPC 面：骨架起一次，契约守到底

两个 flag 解决的是**不同阶段**的问题，别混用：

| | `--grpc-skeleton`（seeded） | 契约（owned，默认生成） |
| --- | --- | --- |
| 时机 | 新域从零起实现 | 已有实现长期防漂移 |
| 产物 | struct + 构造 + 方法签名 + `Unimplemented` 方法体 | 逐 RPC 方法集接口 + `var _ 契约 = (*实现)(nil)` |
| 重生成 | 不覆盖（此后归你） | 每次 `--adopt` 重写，`--check` 报差异 |
| 声明前置 | `grpcBizPackage` + `grpcBizType` | `grpcBizPackage` |

为什么需要契约这一层：handler 都嵌入 `Unimplemented<T>Server`（`require_unimplemented_servers` 的默认形态），于是

- proto **新增** RPC → 调用落到 embed 的兜底实现，**运行时**才 Unimplemented，编译不报错；
- proto **改动** RPC 签名 → 旧方法成为无人调用的死方法，新签名由 embed 满足，同样编译通过。

两种情况 `var _ <T>Server = (*impl)(nil)` 都成立。实测：只实现 5 个 RPC 中 1 个、其余全靠嵌入兜底的探针结构体，编译零错误。所以断言 service 接口守不住任何漂移——契约必须逐 RPC 列出**去掉兜底后**的方法集。

契约的方法集直接取 proto 扫描结果，不用绑定派生视图（后者只保留带 `google.api.http` 注解的 RPC，整域零路由时还会返回 nil——拿它当契约来源会静默漏掉未注解的 RPC，恰好是漂移最爱藏的地方）。流式按方向出签名：

```go
Greet(ctx context.Context, req *greetv1.GreetRequest) (*greetv1.GreetResponse, error)             // unary
GreetStream(req *greetv1.GreetRequest, stream greetv1.GreetService_GreetStreamServer) error      // server stream
GreetUpload(stream greetv1.GreetService_GreetUploadServer) error                                 // client stream / bidi
```

**实现命名约定**（无法从 proto 派生，故约定而非猜测）：实现结构体在 `handler/grpc` 包内、私有、名称为 service 短名的 lowerCamel（`TenantService` → `tenantService`）；`--grpc-skeleton` 生成的骨架同名，构造器为 `New<T>` 去冗余后缀（`TenantService` → `NewTenantServer`）。违例时编译报 `undefined: xxxService`，错误自解释。

漂移真的发生时的读数（把 `rpc ProbeDrift(...)` 加进已声明的 service，重跑 `--adopt` 重生成契约）：

```
tenant.grpc_contract.gen.go:26:31: cannot use (*tenantService)(nil) (value of type *tenantService)
  as tenantServiceContract value in variable declaration: *tenantService does not implement
  tenantServiceContract (missing method ProbeDrift)
```

默认生成的理由与绑定同一条：能机械派生的守卫不该靠人记得开——而且**漏开会误判**：
不带该能力跑 `--check`，已有契约文件会被报 `stale`（`--adopt` 会删掉它们）。
逃生口是 `--no-grpc-contract`（面完全手写、实现尚未落地时用）。

```bash
# 提交门禁（默认口径即可，无需额外 flag）
bald gen domain --spec appspec.json --no-bind --check
```

## 5. 报错速查

**生成期（Gate1 强化：domains.service/services ↔ 实际 proto，`gen_domain.go`）**——扫描根为 api/proto、api/protos、proto（存在即扫，gen/ 生成目录跳过）；`httpOnly` 域没有 proto 契约，跳过本项对账：

- `domains[0] (greet): service "bald.v1.GreetService" not found in proto package "bald.v1" (… declares: …)`——`service` 或 `services[]` 的每一项都必须是扫描根下某 proto 的 `<package>.<Service>`；报错列出该包实际声明，改声明或改 proto。
- `domains[0] (greet): proto package "bald.v2" not found (scanned roots: …; N proto file(s) declaring packages: …) -- domains.service/services must name <package>.<Service> of a proto under the scanned roots`——protoPackage 拼错，或 proto 不在扫描根下。
- `domains[0] (greet): service is required (field domains.service/services; gen domain derives handlers and authorization from it)`——非 `httpOnly` 域既没写 `service` 也没写 `services`；补声明，或纯 HTTP 域改设 `httpOnly: true`。

**声明形态（`validateAppSpec`，Gate1）**：

- `domains[0] (dashboard): http_only domains must not declare service, services or proto_package (a pure-HTTP domain has no proto contract to derive from)`——去掉 proto 字段，或取消 `httpOnly`。
- `service and services are mutually exclusive (declare the singular service, the services list, or http_only)`——三种形态只能选一种。
- `domains[0].services[1] "org.v1.OrgService" duplicates domains[0].services[0]` / `services[1] is empty` / `has surrounding whitespace`——`services[]` 逐项清理。
- `domains[0].services[1] "a.v1.GreetService" and "b.v1.GreetService" share the short name "GreetService" (bindings name their Go interface after it; short names must be unique within a domain)`——同域内 service 短名重复会让生成的 Go 接口撞名；改名或拆域。

**运行期（域声明校验与执行）**：

- `appkit: duplicate domain name "greet" (each domain must be declared once)`——域名重复。
- `appkit: domain "greet" declares Routes but no HTTP face is declared (use appkit.WithHTTP, or declare a grpc-gateway provider for server.http)`。
- `appkit: domain "greet" declares Services but bootstrap.server.grpc is nil`。
- 域执行失败：`appkit: domain "greet": <原因>`（Wire）/ `appkit: domain "greet" routes: <原因>`——fail-fast，已建资源经回滚账本回收。

## 6. 多域协作规则

- **域间禁止互相 import**。业务能力级跨域访问走两条通道：被依赖域包导出访问器，依赖经 Deps getter 供给——装配面保留各域 Deps getter 实参，域在 Wire 里迟到取用。批 4 W1b 样板域（tenant/user）先行定型这套形态，bald-admin 21 域域化实证（共享依赖清单 BizSet 随之溶解为域 Wire 自取）。
- **基础设施住装配层**：数据库连接、缓存客户端等是装配层自持的抉择，域在 Wire 内经 `storegorm.SQL.Resource(app)`、`rediscontract.Redis.Resource(app)` 迟到取用（阶段 B 产物此时已就绪；资源面语义见 [Provider 接入](./Provider%20接入%20-%20contract.AppProvider%20与段%20Provider.md) §4）；迁移执行同理。
- **业务配置段只进 Settings 快照**：`app.Settings()` 读自己的 `<域>.*` 子树，不进契约、不触发 L3 覆盖检查。

## FAQ

- **新增域要动 main.go 吗？** 不。域清单是生成文件，声明值变参直传；main 装配面的域相关增量只有一次性的形态迁移（批 3 实测两行）。
- **域的依赖（store/缓存/认证器）要在装配层手写吗？** 不用。写进 `appspec.json` 的 `domains[].deps`（§3.1），生成物直接产出 `Setup` 接线；装配层只填一次 `DomainDepSources`，字段集与域数量无关。
- **域包已有 Domain 声明，gen domain 会覆盖吗？** 不会——骨架是 seeded 且仅当域包尚无导出 Domain 时落盘；greetv1 这类既有域包消费不覆盖。
- **域的 REST 路由挂在哪？** 挂上 HTTP 面的业务 handler——`WithHTTP` 直接给的，或 `WithHTTPContract` 按 `server.http.middleware` 段求值出的（生成脚手架走后者，见[用 bald CLI 起步新服务](用%20bald%20CLI%20起步新服务.md) §4.2）；gateway 转码面占用 server.http 端口时 Routes 整体让位（REST 走转码，同端口单面语义）。
- **proto 加了 RPC，handler 忘了实现，构建能发现吗？** 单靠嵌入 `Unimplemented<T>Server` 发现不了（运行时才 Unimplemented）。声明 `grpcBizPackage` 后契约**默认生成**，把它放进 check 门禁即可：proto 增删改 RPC 后重新生成，`var _ 契约 = (*实现)(nil)` 立刻编译失败（§4.1）。
- **三个槽位必须全实现吗？** 不。三槽全可选，nil 即不声明该面；但声明了 Routes 需应用有 HTTP 面、声明了 Services 需契约 server.grpc 段（§1）。

