# 机制层 mech：Effect 与期望态

本页介绍 `pkg/appkit/mech`——bald 的高级机制面。quickstart 的单入口装配
（`appkit.MustServe`）与各能力包页覆盖日常开发，用不到这里的任何符号；
**用不到本页可以整页跳过**。需要其中某一件——可逆的全局写入、启动期依赖
检查、个别配置 key 的热更、期望态收敛、运行期可逆挂载——再按节取用。
设计论证见 `docs/devel/zh-CN/装配层评估.md` §4.4/§9，本文只讲操作。

## mech 与 appkit 的分层契约

mech 是 appkit 的子包，依赖单向：mech import appkit，appkit 不 import mech
（mech.go 包文档）。分工三句话：

- **构造面归 mech**：`Effect`/`Provides`/`Requires`/`Components`/
  `OnKeyChange`/`Reconcile` 等构造器都在 mech，返回自持 option 类型
  （`Apply` 直写 `appkit.Decl` 的导出字段），与 appkit 包内构造器共用
  同一份声明载体；
- **引擎归 appkit**：消费这些声明的机制引擎（`UndoEffects`/`Resolve`/
  `startComponents`/`dispatchKeyWatch`/`runReconcilers`）留在 appkit，
  行为与 mech 构造器无关；
- **引擎消费的类型也归 appkit**：`Component`、`ReconcileCtx`、
  `ReconcileFunc` 等，mech 经 `appkit.X` 引用。

对业务只有一条含义：import 一行 mech 即启用全部机制，appkit 默认包面不
因此膨胀（评估 §4.4：26 个机制符号 + 17 个注册表符号下沉后，默认可见面
从 147 收敛到 ≈34）。

## Effect 账本：全局写入的可逆化

进程级注册点是「不可逆效应」——对共享环境的写入没有逆操作记录，覆盖即
丢弃旧值。Effect 账本把它变成结构保证：任何全局写入配套登记一条逆操作
（undo），停机或启动失败时**逆序回放**（后登记的先撤销，与依赖建立顺序
对称），系统回到装配前的干净状态：

```go
old := globalReg["tracer"]     // 覆盖前留旧值（任意进程级状态）
globalReg["tracer"] = mine
app := appkit.NewApp(
    mech.Effect("tracer", func(ctx context.Context) error {
        globalReg["tracer"] = old // undo：恢复旧值
        return nil
    }),
    mech.EffectTimeout(5 * time.Second), // 每条 undo 各享独立超时
)
```

谁来回放——三个执行点：

| 场景 | 行为 |
| --- | --- |
| 正常停机 | stopAll 阶段 0 逆序回放，先于 BeforeStop 钩子 |
| 启动失败 | Run 的早失败路径（配置加载、Resolve、beforeStart 失败）直接回放；组件/服务器启动失败经 stopAll 回放 |
| e2e 测试 | `t.Cleanup(func() { app.UndoEffects(ctx) })`，免跑完整 Run |

契约（effect_test.go 固化）：每条 undo 独立超时；panic 被隔离，不拖垮其余
撤销与整机停机；回放幂等——执行后账本清空，`UndoEffects` 重复调用无副作用。

登记有两个时机。装配期走 `mech.Effect`；运行期由框架自己追加：Run 期才
构造的产物（如服务器 cleanup）在构造发生的同一时刻入账——provider 构建
循环逐项即时落账，构建失败由 Run 失败路径回放，构造期（账本未建立）失败
则逆序回滚已建项（provider.go:395-433）。业务通常只接触装配期入口。

## 能力声明与组件生命周期

三件套把「依赖建立顺序」从注释约定变成启动期检查：

- `mech.Provides(caps...)`：声明本进程提供的能力。只声明确定拥有的；同一
  能力重复声明视为装配笔误，Resolve 启动期报错。
- `mech.Requires(component, caps...)`：声明组件依赖的能力。缺失能力在
  Resolve 启动期**聚合**报错（fail-fast），同一 (component, cap) 重复声明
  幂等。
- `mech.Components(comps...)`：注册进程内组件——顺序 Start（注册序=依赖
  建立序）、逆序 Dispose（停机对称）、panic 隔离、独立超时
  （`mech.ComponentTimeout`）。

`mech.ComponentFunc(name, dispose)` 把无启动逻辑的清理函数（otel
TracerProvider.Shutdown、database/sql Close）适配为 `appkit.Component`。

Resolve 挂在 Run 内紧随配置加载之后——依赖缺失在启动期报清晰错误，而不是
运行期 nil panic。

## 配置热更的三层响应：全量 → key 订阅 → 期望态协调

配置 watch 触发时（发生在 config.Load 的变更回调内，合并树已重建完毕），
三类响应按固定顺序共存——先跑全量、再分发 key 订阅、最后跑协调
（wrapKeyWatch），可在同一个 App 上叠加：

1. **全量回调** `appkit.OnConfigChange`：任何变更都触发，业务自己重新
   Unmarshal——既有行为，仍然可用。
2. **key 级订阅** `mech.OnKeyChange(key, fn)`：框架把 diff 下沉——仅当
   订阅 key 的新值与上次观测值不同才触发 `fn(old, new)`。启动时以首次
   加载值为基线（首次变更前不触发），同值刷新不触发；key 为点路径
   （`"http.addr"`）；未开启 watch（本地/远程源均未配置）时永不触发。
   fn 应轻量（改状态、切引用、记日志），不要在其中再触发配置重载或长时间
   阻塞。
3. **期望态协调** `mech.Reconcile(name, fn)`：状态驱动——业务只声明
   「期望是什么样」，框架拿期望态与实际态做差集收敛。与事件模型（前两层）
   的本质差异是**收敛性**：事件回调遇连续变更/部分失败会留中间态，要业务
   自己兜底；协调每次重读两边再 diff，失败不回滚，下次协调自然补齐。

协调函数拿到 `*appkit.ReconcileCtx`：`Settings` 是本次协调开始时的配置
快照（深拷贝只读，一次协调内取值一致），`String`/`StringSlice` 读点路径
期望值，`Mounted`/`Has` 查实际态，`Mount`/`Unmount` 收敛。要求幂等：
任意时刻重复调用都朝同一终态收敛。单个协调器失败仅记日志（附审计事件），
由下次协调补齐，不阻断整机；多个协调器按注册序执行；手动触发用
`app.ReconcileNow(ctx)`。

配套工具 `mech.DiffStrings(want, have)`（util.go:20）返回期望集与实际集
的差集（add/remove，均升序稳定），「期望有实际无→挂载、实际有期望无→
卸载」一句话写完；`mech.ParseStringList` 把配置值解析为字符串列表（薄委托
config.StringList，新代码直接用它）。

## Registry[T]：运行期可逆挂载基座

`mech.Registry[T]` 是并发安全的按名注册表。设计意图是显式装配哲学：实例
由调用方构造（`mech.NewRegistry[T]()`），不做全局预置实例、不做 init()
自注册——引擎/传输/中间件的接线由业务在装配代码里显式完成。

两对语义严格对偶：

- **Register / MustRegister**（装配期、不可逆）：重名返回 error（或直接
  panic），不覆盖——避免静默踩踏；`Get` 按名取（未命中返回零值+false），
  `List` 升序列名。
- **Mount / Unmount**（运行期、可逆）：Mount 同样拒绝同名条目（无论
  Register 还是 Mount 写入的），成功返回**幂等 undo 闭包**；Unmount 按名
  卸载并返回被卸载实例。同名换实现的约定：先 Unmount 旧条目再 Mount 新
  条目（保持配对）。

它不是 bootstrap 那套装配期 fail-fast 注册表（bootstrap/registry.go）——
两套泛型注册表语义不同、不共用，并存是装配层评估 §9 裁定的终态：前者服务
段驱动装配，`Registry[T]` 的 Mount/Unmount 是装配期 Register 的**运行期
对偶**（可逆挂载、undo 幂等），与 `App.MountComponent`/`UnmountComponent`
同属组件热插拔与期望态协调的底层原语。

## 三个常见误区

- **Effect 不是 defer**。undo 只在停机与启动失败回滚时执行——成功常驻的
  写入同样会在停机时被撤销。它服务的是测试隔离与失败回滚；进程存续期的
  状态管理是普通代码的事。
- **Register 与 Mount 都不覆盖**。装配期重名是装配笔误（fail-fast 报错）；
  运行期换实现只有一条路：先 Unmount 旧条目，再 Mount 新条目（保持配对）。
  不存在覆盖式注册——静默踩踏正是这个设计要挡掉的事故。
- **订阅回调要保持轻量**。OnKeyChange 的 fn 与协调器都运行在 config.Load
  的变更回调内；在回调里再触发配置重载/注入类操作，等于在分发链里自触发。
  轻量三件事：改状态、切引用、记日志，剩下的交给下一次协调收敛。

## 逃生舱，不是必经之路

业务默认停在 quickstart 的单入口形态。以上机制相互独立，按问题取用：全局
状态要可逆 → Effect 账本；依赖要启动期查 → Provides/Requires；个别 key
热更 → OnKeyChange；声明式收敛 → Reconcile；运行期换实现 → Registry[T]。
