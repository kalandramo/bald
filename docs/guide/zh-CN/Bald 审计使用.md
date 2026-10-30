# Bald 审计使用

面向使用者的操作手册：默认行为是什么、启用后端要做哪几步、埋点怎么挂、
运行期热切怎么配、报错了怎么修。设计论证（为什么旁路三层防线、为什么双轨
装配）见 `docs/devel/zh-CN/Bald 审计设计.md`，本文只讲操作。

## 心智模型：一句话版

**审计生效 = 埋点 + 后端两件事。后端经 audit 段 Provider 装配：`std.Standard()`
预置默认 Provider（`log` 项零代码零配置）；store/stream = 引一个依赖 + 自声明
audit 段 Provider（`audit.Compose` 组装）+ `backends` 加一项。请求级 auditor
经构造器显式注入 bundle/中间件（进程级全局已删除）；运行期热切走 R1-2
协调器（同一个键）。**

| | 契约轨（启动期 Provider） | 协调器轨（R1-2 热切） |
| --- | --- | --- |
| 时机 | 启动期一次性（阶段 B，首个请求前） | 运行期收敛（配置变更再触发） |
| 失效模式 | fail-fast：配错启动即失败 | 容错：失败仅记日志，下次协调补齐 |
| 键 | `audit.backends` | `audit.backends`（同一个键，一份期望） |
| 生命周期 | 框架管（产物落资源面 + cleanup 逆序回放） | 业务管（组件 Mount/Unmount） |
| 适合 | 启动即正确，大多数项目 | 多后端热切、变更自身被审计 |

## 0. 什么都不做：默认行为

`main.go` 不写任何审计代码、yaml 不配 `audit` 段：

- audit 段缺省不装配；请求级审计中间件缺省 `NopAuditor`——显式注入是
  唯一通道（进程级全局 `SetAuditor`/`GetAuditor` 已删除），挂了中间件也
  不产生任何副作用——审计零成本可关闭。
- 协调器/组件热插拔的管理面审计事件经单一解析取 sink（显式注入 >
  契约产物 > Nop，永不 nil），此形态下进 nop（不可见但不报错）。

最小启用只需 yaml 一行（`log` 内置于 std 默认 Provider，零注册零依赖）：

```yaml
audit:
  backends: [log]        # 事件进结构化日志（info 级；Result=error 升 error 级）
```

`log` 项装配出的 LoggerAuditor 落资源面，作框架自身事件的兜底 sink；
**请求级审计仍需把 auditor 显式注入 bundle/中间件**（见 §3c）。

## 1. 启用 store / stream：自声明 audit Provider

以 store 为例（stream 替换 module 与分派分支即可）。

**步骤 1：引依赖**（桥接后端是独立 module，用的人付依赖代价）：

```bash
go get github.com/kalandramo/bald/contrib/audit-gorm
```

**步骤 2：自声明 audit 段 Provider**（连接实例由业务注入，不进配置——
「配置驱动参数，代码声明能力」；`Provider` 即 `Option`，直接作 `MustServe`
变参项，末者胜覆盖 std 默认——**`log` 项也要在你的 Build 里映射**）：

```go
type auditProvider struct {
    db  *gorm.DB              // 连接实例业务自持
    rdb redis.UniversalClient // stream 后端用，可选
}

func (auditProvider) Segments() []string { return []string{"audit"} }
func (p auditProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

func (auditProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
    return []appkit.Node{{Key: "audit", Phase: appkit.PhaseBootstrap,
        Priority: appkit.PriorityAudit, TeardownAfter: []appkit.ResourceKey{"*"}}}, nil
}

// Build 把 backends 列表映射为后端构造器，经 audit.Compose 组装
// （列表序 = 广播序；失败逆序回滚的语义收敛在 Compose）。
func (p auditProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
    acfg := s.Config.GetAudit()
    backs := make([]audit.NamedBackend, 0, len(acfg.GetBackends()))
    for _, typ := range acfg.GetBackends() {
        switch typ {
        case appkit.AuditTypeLog: // "log"：核心 LoggerAuditor，零依赖
            backs = append(backs, audit.NamedBackend{Name: typ,
                New: func(context.Context) (audit.Auditor, func(context.Context) error, error) {
                    return audit.NewLoggerAuditor(), nil, nil
                }})
        case storecontract.TypeStore: // "store"：contract 包返回现成的 AuditProvider 函数值
            sp := storecontract.NewStoreProvider(p.db)
            backs = append(backs, audit.NamedBackend{Name: typ, New: func(ctx context.Context) (audit.Auditor, func(context.Context) error, error) {
                return sp(ctx, acfg, s.App) // 契约构造器吃 (ctx, cfg, res)
            }})
        default:
            return nil, nil, fmt.Errorf("audit: unsupported backend type %q", typ)
        }
    }
    a, cleanup, err := audit.Compose(ctx, backs...)
    if err != nil {
        return nil, nil, err
    }
    return a, cleanup, nil
}
```

```go
appkit.MustServe(ctx, std.Standard(), auditProvider{db: db}, ...)
```

没有 `init()` 自注册——contract 包的 `NewStoreProvider(db)` /
`NewStreamProvider(rdb)`（返回 `appkit.AuditProvider` 函数值）只在你的 Build
分派里被调用时才生效。Build 在阶段 B 执行、排在 database/cache 客户端之后
（store 需 `*gorm.DB`、stream 需 redis，经 `storegorm.SQL.Resource(app)` /
`rediscontract.Redis.Resource(app)` 先就绪）；
产物落资源面，cleanup 由框架挂停机 Effect 逆序回放（stream 尾批 flush
最后执行）。

**步骤 3：yaml 声明**（backends 加一项，可多后端并存）：

```yaml
audit:
  backends: [log, store]   # 列表序 = 装配序 = 广播序；至少一项，重复项 fail-fast
  store:
    migrate: true          # 启动时自动迁移默认审计表 audit_records
  stream:                  # 仅 backends 含 stream 时被消费
    stream: audit.events   # Redis Stream 名，缺省 audit.events
    buffer: 1024           # 内存缓冲，缺省 1024，满则降级 fallback
```

## 2. 三后端速查

| | `log`（核心内置） | `store`（contrib） | `stream`（contrib） |
| --- | --- | --- | --- |
| module | —（根模块内） | `contrib/audit-gorm` | `contrib/audit-stream` |
| 接入 | 零注册（std 内置） | 应用 audit Provider 内 `NewStoreProvider(db)` + Compose | 应用 audit Provider 内 `NewStreamProvider(rdb)` + Compose |
| 参数子段 | 无 | `store.migrate` | `stream.stream` / `stream.buffer` |
| 降级 | 无需（日志契约无错误返回） | 落库失败/panic → fallback 双写 | 缓冲满/发布失败 → fallback 双写 |
| 停机 | 无 cleanup | 无 cleanup | `Close()`：停后台 goroutine + drain 尾批（不丢已入队事件） |
| 直用构造 | `audit.NewLoggerAuditor()` | `auditgorm.New(db, opts...)` | `auditstream.New(rdb, opts...)` |
| 表/流 | — | `audit_records` 表（`WithRecordMapper` 换） | `audit.events` 流（`WithStream` 换） |

直用 Option（不经契约时）：store 的 `WithRecordMapper(fn)`（自定义表结构）、
`WithFallback(a)`；stream 的 `WithStream(name)`、`WithBuffer(n)`、`WithFallback(a)`。
fallback 缺省 `LoggerAuditor`（降级进日志），传 `audit.NopAuditor()` 关闭。

## 3. 埋点：事件从哪来

### 3a. 自动埋点（挂中间件即得，业务零代码）

| 来源 | Object / Action | 触发点 |
| --- | --- | --- |
| 请求审计（gin `AuditMiddleware` / grpc `AuditInterceptor`） | P9 归一化资源/动作（缺省裸 path/method） | 每请求一条，handler 返回后 |
| 认证失败（authn 层显式留痕） | `"authn"` / `"authenticate"` | 401 abort 前，Result 恒 deny |
| 协调器（R1-2 每次收敛） | `"reconciler"` / `"reconcile"` | 成功/失败都可观测 |
| 组件热插拔（mount/unmount） | `"component"` / `"mount"` `"unmount"` | 管理面动作留痕 |

请求审计的 Result 推导：gin 侧 `401/403 → deny`、`>=500 → error`、其余
`allow`；grpc 侧 `PermissionDenied → deny`、error → `error`、nil → `allow`。
Meta 自动携带 `request_id / trace_id / status / client_ip / user_agent`。

### 3b. 挂中间件（bundle 推荐，链序固化）

```go
b := bundle.New(
    bundle.Authn(authenticator),
    bundle.Authz(authorizer),
    bundle.Audit(auditor),   // 与 Metrics 至少一项才挂审计层
    bundle.Normalized(),     // P9 归一化默认开启（object/action 与 RBAC 同源）
)
router.Use(b.Gin()...)
grpc.NewServer(grpc.ChainUnaryInterceptor(b.GRPCInterceptors()...))
```

散装路径（链序自理，Audit 必须在 Authn 内侧、Authz 外侧）：

```go
router.Use(ginmw.AuditMiddleware(
    ginmw.AuditWithObjectResolver(authz.DefaultHTTPObject),
    ginmw.AuditWithActionResolver(authz.DefaultHTTPAction),
))
```

### 3c. 关键：auditor 的显式注入（唯一通道）

gin/grpc 审计中间件与 authn 失败审计在**构造时刻**绑定 auditor：显式
`AuditWithAuditor(a)` / `AuthnWithAuditor(a)` / `bundle.Audit(a)` 是唯一
注入通道，缺省 `NopAuditor`——进程级全局 `SetAuditor`/`GetAuditor` 已删除，
不存在「快照全局」的缺省路径。而契约轨装配发生在阶段 B、协调器热切发生在
运行期——都晚于 main 里构造的中间件。

两种姿势按场景选：

```go
// 姿势 1（静态）：已持实例直接注入——生命周期自己管
bundle.Audit(auditgorm.New(db))

// 姿势 2（应用自持审计总线，动态转发，契约轨/热切轨通吃，推荐）：
// 注入中间件的是转发器，每次 Record 读应用自持的「当前生效 auditor」
bundle.Audit(securityaudit.Global())
// 装配与热切对已挂中间件即时生效；范本见 bald-admin
// backend/internal/security/audit（SetCurrent/Current/GlobalAuditor）
```

忘了注入（或没走转发器）则请求审计静默进 nop——事件丢弃、不报错。
框架内部消费者不受此边界影响：协调器/组件审计事件每次经单一解析取 sink
（显式注入 > 契约 audit 段产物 > Nop）。

### 3d. 业务自定义埋点

直接调你持有的 `Auditor`（bundle.Audit 注入的同一实例，或应用自持总线的
`Current()`；`AuditEvent` 八字段：Time/Subject/TenantID/Object/
Action/Result/Error/Meta）：

```go
aud.Record(ctx, audit.AuditEvent{
    Subject: "u-alice",
    Object:  "secret",
    Action:  "export",
    Result:  audit.ResultAllow,
    Meta:    map[string]any{"rows": 42},
})
// Time 可不填：store/stream 后端在记录时兜底为当前时刻
```

## 4. 运行期热切（协调器轨）

契约轨不支持热更新（启动期一次性装配）。运行期切换后端走 R1-2 协调器
（`mech.Reconcile`），读**同一个键** `audit.backends`（yaml 列表或逗号分隔串
均可，如 `audit: {backends: "store,stream"}` 对协调器可见）；触发由框架负责：
配置变更（`WatchConfigFile(true)` 或远程源变更）自动再收敛，失败仅记日志、
下次补齐：

```go
import "github.com/kalandramo/bald/pkg/appkit/mech"

appkit.MustServe(ctx, std.Standard(), myAuditProvider(db, rdb),
    mech.Reconcile("audit.backends", func(ctx context.Context, r *appkit.ReconcileCtx) error {
        want := r.StringSlice("audit.backends")            // 期望态
        add, remove := mech.DiffStrings(want, r.Mounted()) // mounted = 当前生效后端名
        for _, name := range add {
            if err := r.Mount(ctx, name, backendComponent(name)); err != nil {
                return err // 失败不回滚，下次协调补齐
            }
        }
        for _, name := range remove {
            if err := r.Unmount(ctx, name); err != nil {
                return err
            }
        }
        return nil // 生效集重建由后端组件 Start/Dispose 钩子刷新应用自持总线
    }),
    appkit.WatchConfigFile(true), // 本地文件热更新（fsnotify）
)
```

后端组件（`appkit.Component`）Mount 时把自身审计器注册进应用自持总线并
`SetCurrent(audit.NewMultiAuditor(生效集...))`，Unmount 时注销并收尾（stream
停后台 goroutine + drain 尾批）——总线切换对已挂中间件即时生效（§3c 姿势 2）。
语义：每次协调自身发审计事件（`{reconciler, reconcile}`）。完整生产范例见
bald-admin `backend/cmd/bald-admin/app/audit.go` 的 `reconcileAudit` 与
`backend/internal/security/audit`（SetCurrent/Current 总线）。

## 5. 自定义后端

实现 `audit.Auditor` 接口（契约：Record 非阻塞或快速失败，绝不向上游
抛错；自身写入失败内部降级）：

```go
type kafkaAuditor struct{ w *kafka.Writer }

func (k *kafkaAuditor) Record(ctx context.Context, ev audit.AuditEvent) {
    // 非阻塞发送；失败记日志，不返回错误
}

// 直用：bundle.Audit(&kafkaAuditor{w}) 显式注入
// 契约可达：audit 段 Provider 的 Build 分派里映射 "kafka" 项
case "kafka":
    backs = append(backs, audit.NamedBackend{Name: "kafka",
        New: func(context.Context) (audit.Auditor, func(context.Context) error, error) {
            k := newKafkaAuditor() // 参数可读 acfg 的既有子段，或纯代码构造
            return k, func(context.Context) error { return k.Close() }, nil
        }})
```

多后端组合无需自己写广播：`audit.NewMultiAuditor(a, b, c)`（过滤 nil、
全空返 Nop、构造序 = 广播序）；`audit.Compose` 组装 backends 列表时已按
同一语义收敛（单后端直返、失败逆序回滚、cleanup 聚合逆序回放）。

## 6. 报错解读：四类 fail-fast 及修复

**段存在但列表空**（含旧配置升级场景）：

```text
appkit: audit.backends is required when audit section is present (expect "log", "store" or "stream")
```

修复：声明至少一项。**bconf v0.6.0 旧配置 `audit: {type: store}` 升级到
v0.7.0 后 `type` 键被丢弃、剩空段触发本报错**——迁移为
`audit: {backends: [store]}`（Go API：`GetType()` 消失、`GetBackends()` 新增）。

**配了 store/stream 但只有 std 默认 Provider**（没自声明 audit Provider）：

```text
appkit: audit backend "store" is not available in std (std ships the built-in "log" backend only; declare an audit Provider consuming the audit segment to assemble store/stream backends)
```

修复：照第 1 节步骤 2 自声明 audit Provider。`backends: [log]` 永远不会报这条。

**列表含重复项**（Compose 组装期）：

```text
appkit: build audit: audit backend "store": duplicate backend
```

修复：去掉重复项（多后端广播不需要重复声明）。

**Provider 内部构造失败**（如 stream 注入了 nil redis 客户端、store 迁移失败）：

```text
appkit: build audit: audit backend "stream": audit-stream: nil redis client
```

修复：检查注入的连接实例；已构造的其他后端会被逆序回滚，无泄漏。

## 7. 边界与注意事项

- **auditor 显式注入是唯一通道**（§3c）：静态注入 or 应用自持总线转发，
  二选一；忘了注入则请求审计静默失效（事件进 nop）。
- **审计永不阻断业务**：调用方 recover + 后端内部 recover + fallback 双写
  三层防线；自定义后端必须遵守 Record 非阻塞契约。
- **Time 兜底**：不填 Time 的事件由 store/stream 后端在记录时兜底当前时刻
  （毫秒级偏差）；LoggerAuditor 不输出 Time（日志自带时间戳）。
- **stream 的 Close 时机**：停机路径调用（契约轨已自动挂 Effect，逆序回放
  最后执行）；Close 之后的 Record 事件滞留缓冲、无人消费——运行期不要
  Close（协调轨由后端组件 Dispose 收尾）。
- **热切 sink 由应用自持**（B8 后无全局）：契约轨产物作框架自身事件兜底
  sink；请求级 sink 要跟随热切必须走应用自持总线（§3c 姿势 2）。
- **store 表结构兼容约束**：默认 `AuditRecord` 与 go-bald-admin 既有表对齐，
  换表结构必须走 `WithRecordMapper`（映射返回值直接交 gorm Create）。
- **审计表不走租户读隔离**：TenantID 仅作列存储，查询方按需过滤——审计是
  全量事实，不随租户过滤丢行。

## FAQ

**Q：审计和日志什么区别，配了 log 后端算有审计吗？** 算——结构化固定字段
（subject/object/action/result）进日志，可被采集转存。但日志会轮转，合规
场景推荐 store/stream（可转存、可查询）。

**Q：多后端顺序有意义吗？** 有：列表序 = 装配序 = 广播序（`[log, store]`
则事件先进日志再落库）。单项失败逆序回滚，顺序影响的是 cleanup 释放序。

**Q：deny 事件怎么捕获到的？** 链序契约：Audit 夹在 Authn 与 Authz 之间
——Authz 拒绝时 handler 不执行，Audit 在外层看到 403/PermissionDenied 记
deny；认证失败更早，由 Authn 层显式留痕（Subject 留空）。

**Q：审计事件与指标 `bald_audit_events_total` 重复吗？** 同源不重复：一次
请求一条审计事件（细节全）+ 指标增量（聚合用），维度与用途正交。

**Q：热切后中间件没跟上来？** §3c：中间件构造时刻绑定 auditor（无全局可
快照）——热切场景用应用自持总线转发（姿势 2），让中间件每次记录时读
`Current()`。
