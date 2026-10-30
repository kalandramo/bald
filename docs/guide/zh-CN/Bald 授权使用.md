# Bald 授权使用

面向使用者的操作手册。主线一句话：**生成物产数据、桥产行为**——`bald gen
app`/`bald gen domain` 产出授权的**数据**（默认策略行、object 别名表），
`contrib/authz-casbin` 提供**行为**（装载、判定、热重载）；业务只写装配。
读完本文你能对上号：两个生成物各是什么、两个装载入口各管什么、401/403/
200 三态从哪来、没有生成器怎么装。设计论证见 `docs/devel/zh-CN/装配层
评估.md` §12 与 `pkg/authz` 包文档，本文只讲操作。

## 先分清四层

- **pkg/authz**：授权抽象。`Authorizer.Authorize(ctx, subject, object,
  action)` 返回 (allowed, error)——明确拒绝映射 403，引擎错误映射 500。
  核心零策略引擎耦合，casbin 只存在于 contrib。subject 从 authn.AuthClaims
  取（认证层产出）。
- **传输归一化**（拦截器层）：把请求翻译成 (object, action)。gRPC 走
  `DefaultGRPCObject`/`DefaultGRPCAction`（Service 段去 Service 后缀转
  小写；Delete→delete、List→list、Create/Update/Set/Put→write、其余→get）；
  HTTP 走 `DefaultHTTPObject`/`DefaultHTTPAction`（注解路径去 /v1 取首
  资源段；方法名小写）。同一资源在两个传输面同源，策略不双写。
- **contrib/authz-casbin**：casbin 桥。RBAC **模型**（rbac_model.conf）是
  框架性声明，内嵌为默认；**策略**（p/g 行）是业务数据，由调用方注入。
- **生成物**：seed/policies.go 与 authz/alias.gen.go（下节）。

## 两个生成物：策略行与别名表

gen app 产物 `internal/bootstrap/seed/policies.go`（生成一次归你维护，
重生成跳过）：

- `PolicyRow` 三元组（Role/Object/Action），默认行从 proto 的
  service/method 派生（AppSpec.domains 声明序）；
- object 走 `pkg/authz.DefaultGRPCObject` 同源规则：`GreetService` →
  `"greet"`；
- 动作是**六元组**：REST 动词 get/post/put/delete + gRPC 动作 list/write
  双面对齐。教训（bald-admin bootstrap.go）：只写四条 REST 动词时，
  补 gRPC 面的 List/Create 因缺策略行 403；
- 角色：admin 拿全部六元组，viewer 只读（get/list）。gen domain 产物
  authz/alias.gen.go 的 `DefaultActions` 是本表的对账面。

动作派生与 `DefaultGRPCAction` 同源：

| FullMethod 方法段 | action |
| --- | --- |
| DeleteX | delete |
| ListX | list |
| Create/Update/Set/PutX | write |
| 其余（GetX 等） | get |

gen domain 产物 `internal/<apiserver_dir>/authz/alias.gen.go`：

- `ObjectAliases`：object 归一化别名表，键值方向 gRPC 面命名 → HTTP 面
  命名（`greet → greets`），**仅列两侧归一化结果不一致的域**（bald-admin
  教训：冗余映射会掩盖真实命名问题）；
- `DefaultActions`：全部域逐 RPC 的默认动作聚合（handler/convert 派生），
  供默认策略行对账。

## contrib/authz-casbin：两个装载入口

**NewRows(rows, grants)**（policy.go）——生成物策略装载入口，把行渲染
成缺省 RBAC 模型的 p/g 行。语义四条：

- 空集合法（fail-closed 初始态）：rows/grants 均可为 nil/空，对一切请求
  拒绝——这不是错误，是默认拒绝语义；
- 重复幂等：同一 (role, object, action) 装载两次与一次语义相同，不放大
  权限；
- csv 注入拒绝：字段含逗号/换行返回错误（一条数据裂成两行是越权面），不
  静默截断；
- **g 行是运行期业务数据**：p 行只声明「角色对资源的权限」，模型 matcher
  是 `g(r.sub, p.sub)`——subject 必须经 g 行绑定到角色才可能放行，只装
  p 行不装 g 行**恒拒绝**。g 行（DB 用户/角色绑定）随授权变更经 Reload
  重载。

g 行从哪来：**p 行是生成物，g 行不是**。初始绑定集构造时经 grants 传入；
运行期变更（新用户、换角色）走 Reload——配合 `NewWithAdapter` 指向可变
策略源（DB adapter）才有真实语义。

**NewAlias(inner, aliases)**（alias.go）——object 别名装饰器，消费
`ObjectAliases`（入参防御性拷贝，构造后只读）。为什么双向解析：策略行的
object 命名空间由业务自选
（gen 产物默认 gRPC 命名；bald-admin 的 DB 策略走 HTTP 命名），casbin 模型
是精确匹配（`r.obj == p.obj`），请求面命名与策略行命名空间不一致时恒拒绝
——别名表是业务对「两个名字指同一资源」的声明，任一名字授权即该资源授权，
因此解析双向（正向一条 + 反向索引，键序确定），全部拒绝才保持拒绝。

语义边界（安全相关，勿放宽）：

- 只在**内层明确返回 (false, nil)** 后重试——「拒绝」才换名再问，不是
  「拒绝就放行」；别名命中仍需内层放行；
- 内层返回 error（引擎故障）不重试、原样透传，避免把故障误判成命名不匹配；
- 别名指向自身或空串的条目跳过（不产生自旋）；aliases 为空等价于直接委托
  内层；
- 装饰器转发 `ReloadableAuthorizer`：业务经 `authz.Reload` 热重载策略
  时，包一层别名不应让 Reload 静默失效。

## 端到端三态：401 / 403 / 200

internal/codegen 的 `TestTree_AuthzClosedLoop` 在真实生成的工程里钉住
整条链（gen app + gen domain + 用户槽位接线 + 真实起服打请求）。三态的
区分由链路天然给出：Authn 在前、Authz 在后，中间件顺序错了三态就乱：

| 请求 | 身份 | 状态 | 归因 |
| --- | --- | --- | --- |
| POST /v1/greets | 无 token | 401 | authn 层拒绝（未到授权） |
| POST /v1/greets | viewer | 403 | 策略行缺——viewer 只有 get/list |
| POST /v1/greets | admin | 200 | 六元组含 post |
| GET /v1/greets/world | viewer | 200 | 403→200 必经别名反查（greets→greet） |

还有一条隐含边界：内层返回 error（引擎故障）时拦截器映射 500——故障不
冒充拒绝，也不放行。

最后一行是别名表价值的端到端证明：HTTP 面归一化 object 是 greets（注解
路径首段），策略行走 gRPC 命名 greet——没有别名反查，这个只读请求就是
403。四件接线源文（`internal/codegen/testdata/authz/*.go.tmpl`）即
「生成一次、归你维护」用户槽位的消费示范：bridge.go（seed.Rows → NewRows
+ ObjectAliases → NewAlias）、demo_authn.go（认证桩）、apiserver.go
（bundle.Authn + bundle.Authz 链）、greets_routes.go（域自持受保护端点，
handler 本体不做权限判断）。

## 手写应用：无生成器的最小装配

没有生成器也同一套积木——策略行手写，直接 NewRows + NewAlias：

```go
rows := []authzcasbin.PolicyRow{
    {Role: "admin", Object: "secret", Action: "get"},
    {Role: "admin", Object: "secret", Action: "write"},
    {Role: "viewer", Object: "secret", Action: "get"},
}
grants := []authzcasbin.Grant{{Subject: "u-alice", Role: "admin"}}
az, err := authzcasbin.NewRows(rows, grants)
if err != nil {
    panic(err) // 装载失败启动即崩（fail-fast）
}
az = authzcasbin.NewAlias(az, map[string]string{"secrets": "secret"})
```

注入中间件（bundle 一条链完成归一化 + 认证 + 授权）：

```go
router.Use(bundle.New(
    bundle.Normalized(),     // object/action 双传输同源
    bundle.Authn(myAuthn{}), // 认证：subject 从 AuthClaims 取
    bundle.Authz(az),        // 授权：casbin 桥
).Gin()...)
```

gRPC 面用 `bundle.GRPCInterceptors()`/`GRPCChain()`（或
`grpc.AuthzInterceptor`），同一 Bundle 配置两种传输共用。

动作取值与归一化动作空间对齐（get/list/write/delete/post/put）：手写策略行
同样适用六元组，别只写 REST 四动词——gRPC 面的 List/Create 会因缺 list/write
行 403。

## 排错速查

- **一切请求都 403**：先查 g 行。只装 p 行不装 g 行恒拒绝（matcher 是
  `g(r.sub, p.sub)`）——策略行再全，subject 没绑定角色也过不去。
- **策略行看着对，还是 403**：命名空间不一致。打印两侧对比：请求面 object
  来自归一化（HTTP = 注解路径首段，如 /v1/greets → greets；gRPC = Service
  段去后缀，如 GreetService → greet），策略行是你写进 p 行的值；两侧不同
  就靠别名表补（它只列差异域，两侧一致时**不**入表——别自己加冗余映射
  掩盖命名问题）。
- **Reload 了但策略没变**：`New`/`NewRows` 用字符串 adapter——源是构造
  时快照的文本，Reload 合法但内容不变。要真实重载，用 `NewWithAdapter`
  指向可变策略源（如 DB adapter）；重载失败 casbin 保证旧策略仍在
  （fail-safe），错误透传给调用方重试。触发时机：DB 用户/角色绑定变更后调
  `authz.Reload(az)`（不支持重载的实现返回 (false, nil)，合法无操作）。
- **报错 "empty subject"**：casbin 桥对空 subject 直接报错拒绝——未认证
  本该在 authn 层拦住。看到它说明 bundle 链里 Authn 缺位或顺序不对
  （Normalized → Authn → Authz）。
- **报错 "contains comma or newline"**：字段没清洗。含逗号/换行的
  Role/Object/Action/Subject 被拒绝而不是截断——一条数据裂成两行是越权面，
  这是防线不是 bug，在上游清洗数据。

## 与 bald-admin 的关系

一句带过：bald-admin 的 DB 策略 + HTTP 命名空间是同一座桥的另一种数据源
——rowsToCSV 渲染的 p/g 行与 bald-admin loadPolicyCSV 的装载格式同构，
差别只在策略行存哪、谁生产。
