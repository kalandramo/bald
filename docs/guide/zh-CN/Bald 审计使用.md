# Bald 审计使用

面向使用者的操作手册：默认行为是什么、启用后端要做哪几步、埋点怎么挂、
运行期热切怎么配、报错了怎么修。设计论证（为什么旁路三层防线、为什么双轨
装配）见 `docs/devel/zh-CN/Bald 审计设计.md`，本文只讲操作。

## 心智模型：一句话版

**审计生效 = 埋点 + 后端两件事。后端契约只有一个列表项 `audit.backends`：
`log` 零代码零注册内置；store/stream = 引一个依赖 + 两行注册 + 列表加一项。
启动期装配 fail-fast（契约轨），运行期热切走 R1-2 协调器（同一个键）。**

| | 契约轨（FromBootstrap） | 协调器轨（R1-2） |
| --- | --- | --- |
| 时机 | 启动期一次性（BeforeStart，首个请求前） | 运行期收敛（配置变更再触发） |
| 失效模式 | fail-fast：配错启动即失败 | 容错：失败仅记日志，下次协调补齐 |
| 键 | `audit.backends` | `audit.backends`（同一个键，一份期望） |
| 生命周期 | 框架管（Effect 恢复 prev + cleanup 逆序回放） | 业务管（组件 Mount/Unmount） |
| 适合 | 启动即正确，大多数项目 | 多后端热切、变更自身被审计 |

## 0. 什么都不做：默认行为

`main.go` 不写任何审计代码、yaml 不配 `audit` 段：

- 后端是静默 nop（`audit.GetAuditor()` 返回 `NopAuditor`），挂了中间件也
  不产生任何副作用——审计零成本可关闭。
- 协调器/组件热插拔的管理面审计事件同样进 nop（不可见但不报错）。

最小启用只需 yaml 一行（`log` 内置，零注册零依赖）：

```yaml
audit:
  backends: [log]        # 事件进结构化日志（info 级；Result=error 升 error 级）
```

## 1. 启用 store / stream：通用三步

以 store 为例（stream 替换 module 与类型即可）。

**步骤 1：引依赖**（桥接后端是独立 module，用的人付依赖代价）：

```bash
go get github.com/kalandramo/bald/contrib/audit-gorm
```

**步骤 2：main.go 两行注册**（连接实例由业务注入，不进配置——「配置驱动
参数，代码声明能力」）：

```go
import (
    "github.com/kalandramo/bald/pkg/appkit"
    storecontract "github.com/kalandramo/bald/contrib/audit-gorm/contract"
    streamcontract "github.com/kalandramo/bald/contrib/audit-stream/contract"
)

ar := appkit.NewAuditRegistry()
ar.MustRegister(storecontract.TypeStore, storecontract.NewStoreProvider(db))   // "store"
ar.MustRegister(streamcontract.TypeStream, streamcontract.NewStreamProvider(rdb)) // "stream"，可选
app, err := appkit.FromBootstrap(cfg, appkit.WithAuditRegistry(ar), ...)
```

没有 `init()` 自注册——注册必须显式出现在你的代码里。`log` 不需要注册
（Build 内置）。

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
| 注册 | 零注册 | `storecontract.NewStoreProvider(db)` | `streamcontract.NewStreamProvider(rdb)` |
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

### 3c. 关键：auditor 的绑定时机（快照语义）

gin/grpc 审计中间件与 authn 失败审计在**构造时刻**绑定 auditor（显式
`AuditWithAuditor(a)` 优先，否则快照构造时的全局）。此后 `SetAuditor`
换全局**不影响已构造的中间件**。而契约轨装配发生在 BeforeStart、协调器
热切发生在运行期——都晚于 main 里构造的中间件。

三种姿势按场景选：

```go
// 姿势 1（静态）：已持实例直接注入——生命周期自己管
bundle.Audit(auditgorm.New(db))

// 姿势 2（动态转发，契约轨/热切轨通吃，推荐）：每次记录时读全局
type globalAuditor struct{}
func (globalAuditor) Record(ctx context.Context, ev audit.AuditEvent) {
    audit.GetAuditor().Record(ctx, ev)
}
bundle.Audit(globalAuditor{})   // 装配与热切对已挂中间件即时生效

// 姿势 3（手工装配先于中间件构造）：SetAuditor 之后构造中间件，
// 缺省快照即终值（测试常用：e2e 里 SetAuditor 在 Use 之前）
```

框架内部消费者不受此边界影响：协调器/组件审计事件每次经 `resolveAuditor`
读全局（契约轨与热切轨均即时生效）。

### 3d. 业务自定义埋点

直接调 `Auditor`（`AuditEvent` 八字段：Time/Subject/TenantID/Object/
Action/Result/Error/Meta）：

```go
audit.GetAuditor().Record(ctx, audit.AuditEvent{
    Subject: "u-alice",
    Object:  "secret",
    Action:  "export",
    Result:  audit.ResultAllow,
    Meta:    map[string]any{"rows": 42},
})
// Time 可不填：store/stream 后端在记录时兜底为当前时刻
```

## 4. 运行期热切（协调器轨）

契约轨不支持热更新（启动期一次性装配）。运行期切换后端走 R1-2 协调器，
读**同一个键** `audit.backends`（yaml 列表或逗号分隔串均可，如
`audit: {backends: "store,stream"}` 在 Settings 面对协调器可见）：

```go
app := appkit.FromBootstrap(cfg,
    appkit.WithReconcile("audit.backends", func(ctx context.Context, r *appkit.ReconcileCtx) error {
        want := r.StringSlice("audit.backends")          // 期望态
        add, remove := appkit.DiffStrings(want, mounted(r)) // mounted = 当前生效后端名
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
        audit.SetAuditor(audit.NewMultiAuditor(active()...)) // 按生效集重建全局
        return nil
    }),
    appkit.WithWatchConfig(true), // 配置变更触发再收敛
)
```

语义：每次协调自身发审计事件（`{reconciler, reconcile}`）；失败仅记日志、
下次补齐（进程不挂）。中间件侧要跟随热切，用 §3c 姿势 2 的动态转发。
完整生产范例见 go-bald-admin 的 `reconcileAudit`。

## 5. 自定义后端

实现 `audit.Auditor` 接口（契约：Record 非阻塞或快速失败，绝不向上游
抛错；自身写入失败内部降级）：

```go
type kafkaAuditor struct{ w *kafka.Writer }

func (k *kafkaAuditor) Record(ctx context.Context, ev audit.AuditEvent) {
    // 非阻塞发送；失败记日志，不返回错误
}

// 直用：audit.SetAuditor(&kafkaAuditor{w}) 或 bundle.Audit(&kafkaAuditor{w})
// 契约可达：注册后 backends 加 "kafka"
ar.MustRegister("kafka", func(ctx context.Context, cfg *bootstrapv1.Audit) (audit.Auditor, func(context.Context) error, error) {
    k := newKafkaAuditor() // 参数可读 cfg.GetStore() 等既有子段，或纯代码构造
    return k, func(context.Context) error { return k.Close() }, nil
})
```

多后端组合无需自己写广播：`audit.NewMultiAuditor(a, b, c)`（过滤 nil、
全空返 Nop、构造序 = 广播序）。

## 6. 报错解读：四类 fail-fast 及修复

**段存在但列表空**（含旧配置升级场景）：

```text
appkit: build audit: appkit: audit.backends is required when audit section is present
  (expect "log", "store" or "stream")
```

修复：声明至少一项。**bconf v0.6.0 旧配置 `audit: {type: store}` 升级到
v0.7.0 后 `type` 键被丢弃、剩空段触发本报错**——迁移为
`audit: {backends: [store]}`（Go API：`GetType()` 消失、`GetBackends()` 新增）。

**列表含重复项**：

```text
appkit: build audit: appkit: duplicate backend "store" in audit.backends
```

修复：去掉重复项（多后端广播不需要重复声明）。

**配了 store/stream 但没注册**（漏了步骤 2）：

```text
appkit: build audit: appkit: audit provider "store" not registered
  (import the backend contract package and MustRegister it)
```

修复：照第 1 节步骤 2 补 `MustRegister`。`backends: [log]` 永远不会报这条。

**Provider 内部构造失败**（如 stream 注入了 nil redis 客户端、store 迁移失败）：

```text
appkit: build audit: appkit: build audit backend "stream": audit-stream: nil redis client
```

修复：检查注入的连接实例；已构造的其他后端会被逆序回滚，无泄漏。

## 7. 边界与注意事项

- **中间件构造快照语义**（§3c）：静态注入 or 构造前 `SetAuditor` or 动态
  转发器，三选一；忘了选则契约轨/热切轨对请求审计静默失效（事件进 nop）。
- **审计永不阻断业务**：调用方 recover + 后端内部 recover + fallback 双写
  三层防线；自定义后端必须遵守 Record 非阻塞契约。
- **Time 兜底**：不填 Time 的事件由 store/stream 后端在记录时兜底当前时刻
  （毫秒级偏差）；LoggerAuditor 不输出 Time（日志自带时间戳）。
- **stream 的 Close 时机**：停机路径调用（FromBootstrap 契约轨已自动挂
  Effect）；Close 之后的 Record 事件滞留缓冲、无人消费——运行期不要 Close。
- **手工 `SetAuditor` 无 prev 恢复**：T1 恢复只覆盖 FromBootstrap 装配路径；
  业务手工换全局自行管理旧值。
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

**Q：热切后中间件没跟上来？** §3c：中间件构造时快照 auditor，热切换的是
全局——用动态转发器（姿势 2）让中间件每次记录时读全局。
