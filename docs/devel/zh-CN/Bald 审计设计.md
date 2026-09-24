# Bald 审计设计：旁路审计契约、四类埋点来源与三个后端

> Title: 审计域统一设计——pkg/audit 契约 + contrib/audit-{store,stream} 桥接 + 契约/协调器双轨装配
>
> Author(s): bald 团队
>
> 适用包：`pkg/audit`、`pkg/middleware/{gin,grpc}`（审计埋点）、`pkg/middleware/bundle`、`pkg/appkit`（AuditRegistry/协调器/组件审计）、`contrib/audit-{store,stream}`
>
> 关联文档：[Bald 指标设计](./Bald%20指标设计.md)（审计三元组同源 emit `bald_audit_events_total`）、[Bald 日志设计](./Bald%20日志设计.md)（零后端耦合同纪律）、[Bald Bootstrap 设计](./Bald%20Bootstrap%20设计.md)（契约装配链）、[认证与授权抽象设计](./认证与授权抽象设计.md)（P9 归一化同源）、[框架契约总览](./框架契约总览.md)（§9 中间件表）
>
> Last updated: 2026-09-16
>
> Status: Accepted（现状记录：逆向生成自代码实现，所有行为以代码为准；并入自原《审计抽象设计》）

## 摘要

bald 的审计域回答一个问题：**谁在何时对什么资源做了什么、结果如何**。`pkg/audit` 用一个单方法接口（`Auditor.Record`）与一条扁平事件（`AuditEvent` 八字段）定义契约，零第三方依赖；埋点来源四类（请求中间件 / 认证失败 / 协调器 / 组件热插拔）；后端三个（核心内置 `LoggerAuditor`、桥接子模块 `audit-gorm` 落库与 `audit-stream` 异步流）；装配两轨同键（契约面 `audit.backends` **列表多选**启动期一次性装配，多后端组装 MultiAuditor + R1-2 协调器 `audit.backends` 期望态运行期热切——两个执行器读同一份期望，业务选择启用哪个）。

最重要的承诺：**审计是旁路，永远不阻断业务请求**。这个承诺由三层防线兑现——调用方 `recordSafely` recover 兜底、后端内部 recover、落库/发布失败降级 fallback 双写（见「设计 §4」）。

严格比对代码得出的事实（读者先知道）：**`AuditEvent.Time` 的「缺省时取记录时」契约曾被无实现兑现**——四类埋点来源里只有请求中间件填 Time，其余路径落零值，store 后端把零值 `UnixNano()` 落成 `-6795364578871345152`。已于 2026-09-16 修复（store/stream 后端 Record 入口兜底，见「兼容性」的已知边界表）。

## 背景与动机

### 审计不是日志：结构化事实要能转存，不能随日志轮转消失

`log.Error(ctx, "delete secret failed")` 是给人看的；审计是给检索与合规看的：字段固定（subject/object/action/result）、需转存（列式存储/消息总线）、事后可查（谁删的、什么时候、成功没有）。这决定了审计必须是**结构化事件 + 可替换后端**，而不是一行日志——尽管 LoggerAuditor 作为最小后端就是把事件写进日志。

### 认证失败的审计盲区：abort 路径上中间件不再执行

链序是 `Authn → Audit → Authz`：审计中间件在 Authn **内侧**。认证失败（缺 token / 校验失败）时 Authn 层直接 abort，内侧的 Audit 根本不会执行——401 攻击是审计最该留痕的场景，却天然拿不到事件。解法是认证层显式留痕（`gin/authn.go` 的 `auditAuthnFailure`）：

```go
// 认证失败（缺 token / 校验失败）时发送一条 ResultDeny 审计事件后 abort——审计
// 中间件注册在 Authn 内侧，abort 后不再执行，认证失败必须由本层显式留痕（D3）。
func auditAuthnFailure(c *gin.Context, auditor audit.Auditor, reason string) {
    recordSafely(auditor, c.Request.Context(), audit.AuditEvent{
        Object: "authn",
        Action: "authenticate",
        Result: audit.ResultDeny,
        ...
```

gRPC 侧 `authn.go` 对称实现（bundle 测试锁定：两协议的认证失败事件均为 `{Object: authn, Action: authenticate, Result: deny}`，各恰好一条）。

### 管理面动作也要审计：框架自身的重组是最高危操作

运行期协调器收敛（`audit.backends` 期望态变更）与组件热插拔（mount/unmount）改变了进程行为，属于「框架修改自己」——这类动作的审计价值高于普通请求。二者复用同一事件形状（`mount_test.go` / `reconcile_test.go` 锁定），与请求审计进同一后端、同一查询面。

## 设计

### 1. 核心契约（pkg/audit，四文件，零第三方依赖）

```go
type Result string
const (
    ResultAllow Result = "allow" // 授权通过且 handler 成功返回
    ResultDeny  Result = "deny"  // 授权被拒绝（PermissionDenied）
    ResultError Result = "error" // 请求处理出错（含认证失败/内部错误）
)

type AuditEvent struct {
    Time     time.Time        // 事件时刻（UTC）——store/stream 后端在记录时兜底零值（见「兼容性」）
    Subject  string           // 主体（AuthClaims.Subject）；匿名/未认证为空
    TenantID string           // 租户（AuthClaims.TenantID）
    Object   string           // 资源（P9 归一化，如 "secret"）
    Action   string           // 动作（P9 归一化，如 "get" "delete"）
    Result   Result           // allow / deny / error
    Error    string           // Result=error 时填
    Meta     map[string]any   // request_id / trace_id / client_ip 等附加上下文
}

type Auditor interface {
    Record(ctx context.Context, event AuditEvent)
}
```

实现契约写在接口注释里：**Record 必须非阻塞或快速失败，绝不向上游抛错；自身写入失败应内部降级**。`NopAuditor()` 提供零副作用默认。

全局注入点（`global.go`）：`SetAuditor` / `GetAuditor`，RWMutex 保护，`SetAuditor(nil)` 回退 nop。这是「无显式注入」路径的便利面——见决策②。

### 2. 四类埋点来源（谁发事件）

| 来源 | Object / Action | Subject | Time | 触发点 |
|---|---|---|---|---|
| 请求中间件（gin `AuditMiddleware` / grpc `AuditInterceptor`） | P9 归一化资源 / 动作（缺省裸 path/method） | AuthClaims（匿名为空） | **start ✓** | 每请求一条，`c.Next()` / handler 返回后 |
| 认证失败（authn 层显式） | `"authn"` / `"authenticate"` | 空（无 claims） | ✗ 零值 | 401/Unauthenticated abort 前 |
| 协调器（`auditReconcile`） | `"reconciler"` / `"reconcile"` | ctx 身份 | ✗ 零值 | 每次协调（成功/失败都可观测） |
| 组件热插拔（`auditComponent`） | `"component"` / `"mount"` `"unmount"` | ctx 管理面身份 | ✗ 零值 | Mount/Unmount 成功后 |

请求中间件的 **Result 推导规则**（gin 与 grpc 各自实现，规则一致）：

- gin：响应状态码 `401/403 → deny`、`>=500 → error`、其余 `allow`；`c.Errors` 非空强制 `error`。
- grpc：handler 返回 error → `error`；error 是 `PermissionDenied`（原生 berrors 或转码后 status 均识别）→ `deny`；nil → `allow`。

Meta 注入：gin 侧 `request_id / trace_id / status / client_ip / user_agent`（grpc 侧无 client_ip/user_agent）；认证失败 Meta 携带 `reason / path / client_ip / request_id`。

**与指标同源**：审计事件 emit 的同时，中间件同步 emit `bald_audit_events_total`（object/action/result 维度，`metrics.Event` 是 AuditEvent 的精简视图——避免 metrics 包反向依赖 audit）。协议维度走 semconv 指标，两序列正交（详见《Bald 指标设计》）。

### 3. 链序与 bundle 装配

固化链序（bundle `GinMiddlewares` / `GRPCInterceptors`）：

```
Recovery → RequestID → Logging → CORS → Secure → Authn → Audit → Authz → handler
```

Audit 夹在 Authn 与 Authz 之间是刻意的：deny 是审计重点事件，Audit 必须在 Authz **外侧**才能捕获到拒绝结果（`deny` 时 handler 不执行，链内层全部短路）。

bundle 的纪律：**显式注入，不吃全局**。`Audit(auditor)` 或 `Metrics(recorder)` 至少注入一项才挂审计层；注入了 Metrics 但没注入 auditor 时，bundle 显式接 `audit.NopAuditor()`（`bundle.go:220`）而不是让中间件回落到 `audit.GetAuditor()`——bundle 装配的链路不依赖全局状态，全局点只服务非 bundle 路径。`Normalized()` 模式自动为审计挂 P9 归一化 resolver（`authz.DefaultHTTPObject/Action`）。

### 4. 旁路的三层防线

审计永不阻断业务，靠三层各自独立的防线：

1. **调用方 recover**：中间件/authn 层的 `recordSafely` —— `Auditor` panic 仅降级记日志，绝不影响响应（gin/grpc 双侧实现，`mount_test` 验证组件审计 panic 不向调用方传播）。
2. **后端内部 recover**：`StoreAuditor.Record` / `StreamAuditor.Record` 各自 `defer recover`（实现层第二道保险，覆盖不经中间件的直调路径）。
3. **降级 fallback 双写**：落库失败/缓冲满/发布失败时，事件转发 fallback 后端（缺省 `LoggerAuditor`，传 `NopAuditor()` 关闭）——**审计痕迹不因后端故障消失**。

刻意的例外：`MultiAuditor` **不做** recover（`multi.go:7-9` 注释自认）——广播层不设防可让成员 bug 在测试中暴露而非被静默吞掉，与 `log.MultiLogger` 同款取舍；调用方 `recordSafely` 仍在外层兜底。

### 5. 后端：一个内置，两个桥接

| | `LoggerAuditor`（核心内置） | `audit-gorm`（contrib） | `audit-stream`（contrib） |
|---|---|---|---|
| 语义 | 结构化日志（同步） | gorm 落库（同步） | Redis Stream XADD（异步） |
| 依赖 | `bald/log`（根模块内） | gorm | go-redis v9 |
| 降级 | 无需（log 契约无错误返回） | Create 失败/panic → fallback | 缓冲满/发布失败 → fallback |
| 生命周期 | 无 | 无 cleanup | `Close()`：停后台 goroutine + drain 尾批（幂等） |
| 表/流 | — | `AuditRecord` 表（可 `WithRecordMapper` 换） | `audit.events` 流（可 `WithStream` 换），缓冲缺省 1024 |

**LoggerAuditor 的分级输出**：固定字段顺序 `subject/tenant_id/object/action/result`，Meta 逐键追加；`Result=error` 升 Error 级并附 `error` 字段，其余 Info 级。它不输出 Time——日志系统自带时间戳，「记录时刻」由日志侧提供（这也是 Time 契约缺口影响最小的路径）。

**store 的两个刻意决定**：① `AuditRecord` 字段与 go-bald-admin 既有表对齐（防 AutoMigrate 冲突），扩展列 `Category/IPAddress/UserAgent/RequestID/TraceID` 从 Meta 提取；② 审计表**不走租户读隔离**——TenantID 仅作为列存储，由查询方按需过滤（审计是全量事实，不随租户过滤丢行）。`db` 为 nil 时 Record 全走 fallback（旁路不 panic）。

**stream 的异步语义**：`Record` 非阻塞入队（`select default`），后台 goroutine XADD；`Close` 先 `wg.Wait` 再 drain 剩余缓冲（发布失败的降级 fallback）——**已入队事件不丢**。两层 nil 防御：`auditstream.New(nil)` 返回 nil（调用方跳过装配）；contract 层 Build 时 fail-fast。

**contract 模式**（两个后端对称）：根包保持零契约依赖（纯 SDK 实现），`contract` 子包 import bconf + appkit，导出 Provider 构造器闭包绑定连接——**「配置驱动参数，代码声明能力」**：契约段声明类型与参数（`store.migrate`、`stream.stream/buffer`），gorm/redis 连接实例由业务代码注入，不进配置。

### 6. 契约装配链（bconf audit 段 → appkit AuditRegistry）

契约段（`bootstrap/v1/audit.proto`，bconf v0.6.0 起）：`backends` **列表多选**（取值 `log`/`store`/`stream`，列表序=装配序=广播序，如 `backends: [log, store]`）+ `store.migrate` / `stream.stream` / `stream.buffer` 参数子段。空列表/重复项/未注册项均 fail-fast；旧字段 `type` 单选（v0.5.0）已 reserved 废弃，升级迁移 `type: store` → `backends: [store]`。一次性装配，不支持热更新。

```go
ar := appkit.NewAuditRegistry()
ar.MustRegister(storecontract.TypeStore, storecontract.NewStoreProvider(db))
ar.MustRegister(streamcontract.TypeStream, streamcontract.NewStreamProvider(rdb))
app, err := appkit.FromBootstrap(cfg, appkit.WithAuditRegistry(ar), ...)
```

`AuditRegistry` 与 Tracer/Metrics Registry 同模式（显式注册、重名/空名/nil fail-fast），差异是按 `backends` **列表**分发而非单选，以及：**`log` 内置于 Build**——LoggerAuditor 在核心包零依赖，`backends` 含 log 即零注册开箱即用；store/stream 才需要注册。Build 按列表序逐项装配：任一项失败逆序回滚已构造 cleanup（对齐 bootstrap 六域 Build 的回滚模式）；多后端组装 `MultiAuditor`（列表序=广播序）；成功后 cleanup 聚合逆序回放（后构造先释放，错误 `errors.Join` 汇总不吞项）。`buildAudit` 未接线 Registry 时以空表兜底——错误直指缺失的 provider（`provider "store" not registered`），比「no AuditRegistry wired」更可操作。

生命周期（`buildAudit` + Effect）：

- 装配（阶段 B，BeforeStart）：Build 得 (auditor, cleanup)，保存 `prev := audit.GetAuditor()`，`SetAuditor` 全局注入——首个请求前生效。
- 停机（Effect `appkit:audit-restore`，逆序回放**最后**执行）：先 cleanup（stream flush 尾批审计事件——在指标/span flush 之后收集，收集面最全），再 `SetAuditor(prev)` 恢复装配前全局（T1 纪律：全局写入配套逆操作）。
- 段缺省：不装配，全局保持现状（默认 nop）。

### 7. 运行期热切（R1-2 协调器，与装配双轨并存）

一次性装配的对照轨是协调器期望态：配置面 `audit.backends`（如 `"log,store,stream"`，appspec `audit_backends` 字段），协调器按「期望有实际无 → mount、实际有期望无 → unmount」收敛，每次协调自身也发审计事件（Object=`"reconciler"`）。失败不回滚、下次协调继续收敛。go-bald-admin 已验证此轨（运行期热切审计后端）。

两轨关系与「全局路径 vs 构造器注入」同构：契约面管启动期一次性装配（fail-fast），协调器管运行期期望态（收敛容错）。v0.6.0 起契约字段名统一为 `backends`——两轨读**同一个键**，从「两份配置」变为「一份期望、两个执行器」，业务选择启用哪个（未注册协调器则仅契约轨静态装配）；同名后端列表在两轨语义一致（`Backends: [store, stream]` 列表形式与协调器 `ParseStringList` 的 yaml 列表形式互通）。

## 理由与取舍

**决策①：为什么核心零后端耦合？** 与 `pkg/log` 同纪律：任何纯 HTTP/CLI/测试场景 import `pkg/audit` 都不该背上 gorm/go-redis 依赖。落库/消息总线由 contrib 桥接子模块按需接入，依赖图全程可见（contract 单独成包的原因相同）。

**决策②：为什么保留全局 `SetAuditor`/`GetAuditor`？** 兼容「无显式注入」的简单路径（中间件缺省读全局），nop 兜底保证零配置可跑。但我们不把它当唯一方式——bundle 装配显式接 `NopAuditor()` 不吃全局（决策见 §3），全局点是便利，不是纪律。代价诚实列出：全局状态有心智负担，且手工 `SetAuditor` 没有 FromBootstrap 那样的 prev 恢复（T1 保护只覆盖框架装配路径）。

**决策③：为什么旁路三层防线，而不是审计失败快速失败？** 审计是观察系统的副产物；写失败（队列满/DB 挂）若阻断业务请求，等于把「观察成本」放大为「可用性事故」。fallback 双写进一步承诺「后端故障不丢审计痕迹」。反面取舍同样明确：MultiAuditor 刻意不 recover——广播层静默吞 panic 会让成员 bug 隐匿，暴露优先于防御（外层 recordSafely 仍在）。

**决策④：为什么 `log` 内置而 store/stream 走 Registry？** LoggerAuditor 只依赖根模块内的 `bald/log`，appkit import 零负担；而 store/stream 需要业务先持有 gorm/redis 连接——连接实例天然不进配置（凭证与生命周期归属业务代码）。内置 log 使「配一行 yaml 即有审计」成立（`backends: [log]` 零注册即得），Registry 显式注册使「能力声明」依赖图可见。

**决策⑤：为什么一次性装配 + 协调器双轨，而不是统一热更新？** 审计后端重建侵入小（对比 tracer/metrics 的 exporter 重建），本可热更；但审计的期望态变更本身就是管理面动作，走协调器可复用 mount/unmount 的组件生命周期、审计留痕（变更自身被审计）与收敛语义（失败下次补齐）。一次性装配负责「启动即正确」，协调器负责「运行期演化」——职责清晰优于单轨大而全。

**决策⑥：为什么 `AuditRegistry` 留在 appkit 不迁 bootstrap？** `AuditProvider` 返回 `audit.Auditor`，依赖根模块 `pkg/audit`——按六域归位判别（纯契约段构造 → bootstrap；需根模块包或运行期编排 → appkit），与 Registrar/Tracer/Metrics 三 Registry 同判，迁移即循环依赖。

## 兼容性

根模块 API 纯增量：随根模块 v0.6.2 发布，既有 API（`AuditEvent`/`Auditor`/`SetAuditor`）自 v0.6.1 起未变，新增均为加法（Registry/后端/契约段）。

**bconf 契约例外（v0.6.0，2026-09-16）**：`audit.type` 单选废弃（reserved），新增 `audit.backends` 列表——Go API breaking（`GetType()` 消失、`GetBackends()` 新增）。语义变更：单选 → 多选组装 MultiAuditor。迁移指引：`audit: {type: store}` → `audit: {backends: [log, store]}`；旧配置升级后 `type` 键被 DiscardUnknown 丢弃、剩空段启动显式报错（静默失效变显式迁移提示）。下游核实零影响：go-bald-admin 走 `appkit.New` 路径不装契约，`_example` demo 无 audit 段，codegen appspec 骨架是协调器轨读 Settings。

### 已知边界（严格比对代码得出，如实列出）

**`AuditEvent.Time` 的「缺省时取记录时」契约曾无实现兑现——已修复（2026-09-16）**。修复前 `audit.go:36` 注释承诺但无兑现：四类埋点来源里只有请求中间件填 Time（`start`），认证失败/协调器/组件热插拔均落零值。修复采用后端侧兜底（`StoreAuditor.Record`/`StreamAuditor.Record` 入口 `ev.Time.IsZero() → time.Now()`，一处修复覆盖全部来源，兜底后的事件同样进 fallback）：

| 埋点路径 | Time 填充 | LoggerAuditor | store 落库（`UnixNano`） | stream 落值（JSON） |
|---|---|---|---|---|
| 请求中间件（gin/grpc） | `start` ✓ | 不输出（日志自带时间戳） | 正常 | 正常 |
| 认证失败（authn 层） | 后端兜底 now | 不输出 | 兜底 now（修复前为 `-6795364578871345152`） | 兜底 now（修复前为 `"0001-01-01T00:00:00Z"`） |
| 协调器审计 | 后端兜底 now | 不输出 | 兜底 now | 兜底 now |
| 组件 mount/unmount | 后端兜底 now | 不输出 | 兜底 now | 兜底 now |

兜底取「后端记录时刻」而非「事件语义时刻」——认证失败事件的真实时刻与兜底值的偏差是毫秒级（abort 前后），对审计检索无影响；埋点来源侧显式填 Time 的方案（语义更显式）被放弃，因为改动面横跨 authn/reconcile/mount 四处且收益仅是省一次后端兜底。LoggerAuditor 不兜底也不输出 Time——日志系统自带时间戳，这是设计而非缺陷。

**其余边界**：

- `StreamAuditor.Close()` 之后的 `Record`：事件入队但无人消费，滞留至缓冲满后走 fallback——不丢但滞留（Close 应在停机路径调用，正常运行期不 Close）。
- 手工 `SetAuditor` 无 prev 恢复：T1 恢复只覆盖 FromBootstrap 装配路径；业务手工换全局时自行管理旧值。
- `GetAuditor()` 每请求持读锁：RWMutex 读锁开销极低，当前无优化必要（YAGNI）。
- `AuditRecord` 表结构与 go-bald-admin 既有表对齐是**兼容约束**：改默认表结构会破坏共用库的既有部署，结构变化必须走 `WithRecordMapper`。

### 与下游的契约面

bconf `audit` 段、`Auditor` 接口、`AuditEvent` 字段、`AuditRecord` 表结构四者是对外契约，破坏性变更需升版本；`MultiAuditor` 顺序（构造序）与 fallback 双写语义已被测试锁定，同属行为契约。

## 实现与过渡

现状代码即实现（本文为逆向记录）。测试锁定面：

| 测试 | 锁定 |
|---|---|
| `pkg/audit/{audit,logger,multi}_test.go`（9 例） | nop 静默、全局 Set/Get/nil 回退、allow→Info / error→Error 级、广播序、nil 跳过、空构造返 Nop |
| `appkit/audit_registry_test.go`（5 例） | Register 校验（重名/空名/nil）、Build 多后端：空列表/重复项/未注册 fail-fast、log 内置、MultiAuditor 组装序=配置序、构造失败逆序回滚、cleanup 聚合逆序与错误汇总、buildAudit 装配/缺省/prev 保留 |
| `middleware/{gin,grpc}` audit/authn 测试 | Result 推导（deny/error）、认证失败恰好一条 `{authn, authenticate, deny}`、panic 隔离 |
| `middleware/bundle` 测试 | 链序 `authn→authz→handler→audit→metrics`、deny/error 捕获、审计读 authn 注入的 subject/tenant |
| `appkit/{reconcile,mount}_test.go` | 协调事件 `{reconciler, reconcile}` + meta、组件事件 Subject/匿名、panic 不传播 |
| `contrib/audit-{store,stream}` 测试 | 落库/降级/映射、缓冲满/发布失败降级、Close drain 不丢、Time 零值兜底 now |

待办（按优先级）：① ~~Time 零值兜底~~ 已完成（2026-09-16：store/stream Record 入口兜底 + 两个行为测试锁定 `TestXxx_ZeroTimeBackfillsNow`）；② 审计查询工具面（store 侧的检索 API 目前只有裸 gorm 查询，未提供框架级便捷层——出现真实消费者前不做，YAGNI）。

## 附录：FAQ

**Q：审计和日志到底什么区别？** 三个区别：结构（固定字段事件 vs 自由文本）、流向（可转存列式/消息总线 vs 日志轮转）、语义（合规留痕不可丢 vs 排障信息可丢）。LoggerAuditor 把审计写进日志是最小实现，不是终点——生产推荐 store/stream。

**Q：为什么 Audit 必须在 Authz 外侧、Authn 内侧？** 外于 Authz：deny 是审计重点，内于 Authz 会被拒绝路径短路拿不到。内于 Authn：事件需要 AuthClaims 的 subject/tenant；认证失败拿不到 claims 的场景由 Authn 层自己发（D3 盲区补丁，Subject 留空）。

**Q：多后端怎么共存？** 契约装配：`audit.backends` 列表多选（v0.6.0 起，列表序=广播序），配置如 `backends: [log, store, stream]` 一次配齐；运行期热切则由 R1-2 协调器按后端名排序组装 MultiAuditor 重建全局（同一 `audit.backends` 键的运行期执行器）。

**Q：审计事件会不会与指标重复记录？** 同源但不重复：一次请求一条审计事件（细节全）+ 若干指标增量（聚合用）；`bald_audit_events_total` 是审计三元组的指标投影，二者维度与用途正交（见《Bald 指标设计》）。
