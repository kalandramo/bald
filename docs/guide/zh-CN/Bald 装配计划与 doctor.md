# Bald 装配计划与 doctor

**一句话**：装配是一个纯函数算出来的计划——节点、依赖、段覆盖、警告与 hash。`--plan` 打印它，`--plan-format=json` 交给机器，`bald doctor` 把逃逸舱（Opaque 节点、白名单段、覆盖、build tag）集中成一份诊断。三者与真实启动走同一次 `appkit.Compile`，可在启动前审阅，也可进 CI。

## 1. 计划从哪来

```go
p, err := appkit.Compile(ctx, opts...) // 纯函数：不 Build、不跑钩子、不写文件
```

输入是**同一份**装配声明（`MustServe` 的变参项：Provider、Domain、能力 Option、`WithUnimplementedSegments`、`WithOpaqueBudget`）加上**同一份**契约（`--config` 预装载结果）。输出 `*appkit.Plan`：

| 字段 | 含义 |
|---|---|
| `App` / `Role` | 契约元数据与角色（`WithRole` 声明；多角色部署按角色分核） |
| `InputHash` | 契约（确定性序列化）的指纹 |
| `Nodes` | 节点集：`Key`、`Kind`、`Phase`、`Priority`、`DependsOn`、`Segments`、`Reload`、`Override`/`Reason`、`Opaque` |
| `OpaqueCount` | 逃逸舱节点数（没有进入声明式拓扑、按 legacy 顺序执行的部分） |
| `Segments` | 每个契约段的消费视图：`Consumers` / `Whitelisted` / `Framework` |
| `Warnings` | 覆盖、白名单、预算等诊断 |
| `Hash()` | 计划本身的 sha256——同一输入必然同一 hash |

排序规则固定：`Phase` 序 → 相位内 `DependsOn` 的 Kahn 拓扑 → `(Priority, Key)` 字典序。**实参顺序与 import 顺序不参与排序**，这是「顺序无关」的落点（置换测试见 `pkg/appkit/plan_test.go`）。

## 2. `--plan`：启动前审阅

```bash
bald-demo --config configs/bald-demo-api.yaml --plan
bald-demo --config configs/bald-demo-api.yaml --plan --plan-format=json
```

打印发生在**任何副作用之前**（阶段 A logger、Provider `Build`、业务钩子），随后以 0 退出；计划编译失败（依赖环、重复 Key 无 Override、未知白名单段等）以非零退出，错误与真实启动一致。示例输出（截取）：

```
app: bald-demo v0.1.0 (env=)
role: api
input: d5f5f80e...
nodes:
  configure  app                  priority=-1  app
                                               segments=app
  bootstrap  logger               priority=10  provider, reload=reconcile
                                               segments=logger
  bootstrap  registry             priority=20  app, opaque, override: programmatic takeover
                                               segments=registry
  bootstrap  database.sql         priority=50  provider, reload=restart
                                               segments=database.sql
  wire       domain.greet.biz     priority=10  domain
  serve      server               priority=0   std, opaque
                                               segments=server.grpc,server.http
  serve      domain.greet.routes  priority=10  domain
                                               segments=server.http
                                               dependsOn=domain.greet.biz
opaque: 2 node(s)
segments:
  server.http          consumers=domain.greet.routes,server
warnings:
  warn override [logger]: segment "logger" is declared by ...
plan hash: 162e34f4...
```

读法：

- **`phase`**：`configure` → `bootstrap` → `wire` → `serve` → `drain` → `teardown`。跨相位不接受 `DependsOn` 反向边（编译期报错），停机按 `TeardownAfter` 逆序。
- **`opaque`**：该节点没有声明式依赖拓扑，按 legacy 顺序执行。域在声明 `Domain.Declare` 之前整块是 Opaque；只声明拓扑、构造仍归 `Wire`/`Routes`/`Services` 时塌缩成一个 `domain.<name>.wire` Opaque 节点；迁到 `Domain.ApplyNode`（按计划序逐节点执行）后不再产生兼容节点，域贡献的逃逸舱归零（设计 §4.6）。
- **`segments=`**：节点读取的契约段路径；`segments:` 汇总里 `consumers=` 为空且未白名单的段会在启动期报错。声明域节点既可挂框架段（`server.http` 这类 `BootstrapConfig` 字段路径），也可挂业务配置段 `biz.<path>`——后者把域自有配置带进 `reload=reconcile` 节点的期望态（计划期从合并 Settings 快照取子树算摘要，配置变更即触发 `Domain.Reconcile`）。`biz.<path>` 同样出现在 `segments:` 汇总（标注消费者），但不参与契约段覆盖检查。
- **`reload=`**：节点级重载策略，决定配置热更时协调还是重报。`reconcile` 节点必须有人能重建它的实际态：Provider 节点由同一 Provider 重跑 `Build`，域声明节点由 `Domain.Reconcile` 承担——声明了 `ReloadReconcile` 却没给钩子（或没给 `Wire`/`ApplyNode`、没声明段）的域在计划期直接报错；Provider 也只有 bootstrap 阶段的节点注册了协调器（logger 走专用热更协议），config/server 段节点声明 `reconcile` 同样计划期报错，不留「永远 Stale」的假节点（见 [机制层 mech](./机制层%20mech%20-%20Effect%20与期望态.md)）。
- **`imperative:`**：不参与拓扑的命令式槽位（`effect` / `keyWatch` / `reconcile` 来自 `mech.*` 声明；`hook` 是 `BeforeStart`/`AfterStart`/`BeforeStop`/`AfterStop` 生命周期钩子，按声明序编号；`serverFunc` 是 `WithExtraServerFunc` 的运行期服务器工厂；`onConfigChange` 是热更全量回调）。闭包不可排序、不可分析，但计划不隐藏它们——配置热更订阅者、停机效果与生命周期钩子在 `--plan` 与 `doctor` 里都有名可查（设计 §6 的 Data/Imperative 分区落点）：

  ```
  imperative:
    effect       cleanup-db
    hook         beforeStart[0]
    keyWatch     server.http
    reconcile    logger
  ```

JSON 形态（`--plan-format=json`）是 `Plan` 的序列化，供脚本比较 hash、做计划基线。

## 3. `bald doctor`：逃逸舱集中诊断

### 3.1 接线

`bald gen app --spec` 生成的角色命令已自带 `doctor` 子命令（`appcli.WithDoctor`），与默认 serve 共用同一份 `bootstrap.Options(...)`。手写入口按同样方式挂三行：

```go
return appcli.Main(use, short, runE,
    appcli.WithPlugins(),
    // 与 serve 同一份装配 Options——报告必须与真实启动同源。
    appcli.WithDoctor(func(ctx context.Context) error {
        return appkit.Doctor(ctx, bootstrap.Options(role)...)
    }),
)
```

### 3.2 输出解读

```bash
bald-demo doctor --config configs/bald-demo-api.yaml
```

```
bald doctor
app: bald-demo v0.1.0 (env=)
role: api
input: d5f5f80e...
plan:  192924bf...
cache: .bald/plan-cache.json (baseline ba88603d903f, recorded 2026-10-28T18:38:41Z)
  plan unchanged: nodes + imperative slots

nodes: 7 (opaque 2)
  phase bootstrap  3
  phase configure  1
  phase serve      2
  phase wire       1
  opaque: registry, server
  override: registry (programmatic takeover)

domains: 1 (declared 1, legacy 0)
  declared: greet

segments: 7 (consumed 7, whitelisted 0, framework 0, unconsumed 0)

build tags: (none)

warnings: 2
  warn override [logger]: ...
  warn programmatic-takeover [registry]: ...

gen: run `bald gen app --check` and `bald gen domain --check` in the generated tree
```

| 行 | 回答什么 |
|---|---|
| `input` / `plan` | 报告是否与真实启动同源（同输入同 hash） |
| `cache` | 相对上次启动基线的差异（见 §4；无缓存声明时整行省略） |
| `nodes` / `phase` | 计划规模与各相位分布 |
| `opaque: ...` | 逃逸舱逐个列名——白盒化 legacy 部分；`WithOpaqueBudget(n)` 超预算时在 `warnings` 出现 `opaque-budget` |
| `domains` | 业务域迁移进度：`declared` = 用 `Domain.Declare` 声明拓扑的域，`legacy` = 整块 Opaque 的域（设计 §4.6 的量化口径） |
| `override: key (reason)` | 显式覆盖的节点与其理由 |
| `imperative` | 命令式槽位清单（`effect` / `keyWatch` / `reconcile` / `hook` / `serverFunc` / `onConfigChange`）——与 `--plan` 同源，闭包不隐身 |
| `segments` 分组 | `consumed` 有消费者；`whitelisted` 经 `WithUnimplementedSegments` 豁免；`framework` 框架无条件消费；`unconsumed` 非空即启动失败 |
| `build tags` | 构建期 `-tags`（`debug.ReadBuildInfo` 读取），用于解释 build tag 分叉 |
| `warnings` | 与 `--plan` 同源的全部诊断（含 `override` / `whitelisted` / `opaque-budget` / `ambient`） |

`build tags` 行只回答「这个二进制用什么 tag 编的」；**为什么某个 tag 改变接线**需要应用自己说清楚：`appkit.WithAmbient(appkit.Ambient{Name: "build-tag:grpcgw", Detail: "..."})` 把这类环境注入声明进计划，`--plan` 与 `doctor` 的 `warnings` 出现 `ambient` 诊断（示例见 `_example/bald/internal/bootstrap/register_grpcgw.go` 的 `init()`）。`Name` 为空在计划期报错。

### 3.3 只读边界

`doctor` 只做「声明 + 契约 → `Compile`」，不执行 `Provider.Build`、不跑钩子、不写文件、不联网。两点诚实说明：

- 被 import 的包的 `init()` 仍会执行（例如示例里 web 路由的 gin 注册日志）——这是 Go 运行期语义，不是 doctor 的副作用。
- 生成物清单校验属代码生成器职责（运行期二进制不 import 生成器），报告末尾给 `bald gen app --check` / `bald gen domain --check` 的出口提示，不代跑。
- 计划缓存只读：`doctor` 读基线做对比，**不写**基线（写只发生在真实启动，见 §4）。

## 4. 计划缓存：跨启动对比基线

`appkit.WithPlanCache(".bald/plan-cache.json")` 声明缓存路径（`bald gen app` 生成的 `bootstrap.Options()` 默认已声明；空路径 = 不落盘）。

| 时点 | 行为 |
|---|---|
| 真实启动（`Serve` 装配成功、`Run` 之前） | 按 `(Role, InputHash)` 记一条基线：整份计划 + `PlanHash` + 记录时间。同键覆盖；最多保留 16 条（超出丢最旧）；临时文件 + rename 原子写 |
| `doctor` | 只读同一文件：优先精确 `(Role, InputHash)`，找不到则取同 Role 最近一条——契约变了也能回答「装配变了什么」 |

`doctor` 的四种缓存行：

```
cache: .bald/plan-cache.json (baseline ba88603d903f, recorded 2026-10-28T18:38:41Z)
  plan unchanged: nodes + imperative slots

cache: .bald/plan-cache.json (baseline db80c27572ef, recorded 2026-10-28T18:33:31Z)
  input changed since baseline: 95fca9009d03 -> d5f5f80ec60d
  plan changed: 3 node(s)
    + domain.greet.routes (domain, static)      # 新增
    - legacy.old (provider, static)             # 移除
    ~ logger (provider, reconcile): desired state  # 期望态/依赖变化

cache: .bald/plan-cache.json (baseline 4c1f0a2e77ab, recorded 2026-10-28T18:37:02Z)
  imperative slots changed: 2                # 节点拓扑没变，effect/keyWatch/reconcile 槽位变了
    - effect: cleanup-a
    + effect: cleanup-b

cache: .bald/plan-cache.json (no baseline for role "worker")   # 该角色还没有基线
cache: .bald/plan-cache.json (unavailable: ...)                # 文件损坏/不可读
```

`plan unchanged` 的判据是「**装配声明**未变」：节点 + 命令式槽位（`Plan.Imperative`）——报告行因此显式限定为 `plan unchanged: nodes + imperative slots`，不断言整份 `Plan.Hash` 相等（告警/段视图变化仍可能移动哈希，严格比对以报告顶部的 `plan:` 行或 CI 的计划哈希基线为准）。节点身份对比为空不等于计划没变——effect/keyWatch/reconcile 槽位不进节点拓扑，却会改变运行期行为，所以单列一组增删行，而不是谎报 `plan unchanged`。节点属性（priority/phase/segments 等）变化同样移动装配哈希但不在身份对比里，doctor 报 `plan changed: node attributes (...)` 而不是假装能逐项列出。`InputHash`、告警与段视图各有独立报告面（`input changed` 行、`warnings`、`segments`），不混进这条判据。

边界（诚实说明）：

- **不是执行依据**：缓存不参与协调、不影响装配；写失败降级为 debug 日志（只读工作目录的环境照常启动），读失败只给一行提示、退出码仍为 0。
- **按角色分核**：同一份缓存文件里 api/worker 各占一条——这也是 `WithRole` 存在的理由（没有它，多角色应用无法区分同名 app 的基线）。
- **本地事实，不是 CI 判据**：`InputHash` 含 env 覆盖后的契约值（如动态地址），基线随机器/端口变化；CI 的稳定判据仍用 `--plan --plan-format=json` 的 `plan hash`（§5）。
- `.bald/` 是本地目录，建议进 `.gitignore`（本仓已忽略）。

## 5. 退出码与 CI

| 命令 | 成功 | 失败 |
|---|---|---|
| `--plan` / `--plan-format=json` | 打印计划，退出 0 | `Compile` 错误，非 0（stderr） |
| `doctor` | 打印诊断，退出 0 | 同上 |
| `gen app --check` / `gen domain --check` | `clean: N files`，退出 0 | 列出 drift 文件，非 0 |

CI 可组合成「计划 hash 基线 + 生成物零 drift」：`--plan --plan-format=json | jq -r .hash` 与提交的基线比较；生成树跑两个 `--check`。

## 6. 声明式等级现状（对外口径）

对外表述统一为「**声明式装配**」并标注等级，而不是笼统的「声明式编程」。当前状态（判据与目标见 [设计草案 §3.1](../../devel/zh-CN/Bald%20声明式装配设计-修正草案.md)）：

| 等级 | 定义 | 现状 | 证据 / 缺口 |
|---|---|---|---|
| **L1** 声明式配置 | 配置形状由 schema 定义，未知键/未消费段可检测 | 达成 | 契约 proto + StrictContract + 段覆盖检查 |
| **L2** 可计划拓扑 | 装配拓扑由纯函数算出并打印 | 达成 | `appkit.Compile` + `--plan` + `Plan.Hash()` |
| **L3** 顺序无关组合 | 结果只由依赖图与显式优先级决定 | 达成 | 节点 `Phase`/`Priority`/`DependsOn` 取代手工排序表；置换测试 |
| **L4** 收敛运行 | 声明资源可协调，漂移可观测 | 达成 | 节点级 `ReloadPolicy` + `App.Drift` + `Reconcile`；Provider 节点与声明域节点（`Domain.Reconcile`）可协调，声明域节点的段可挂框架路径或 `biz.<path>` 业务配置（设计 Q8，2026-10-30）；database/cache/storage 段仍明确不支持热更（`ReloadRestart`） |
| **L5** 纯函数生成 | 生成物是 `(AppSpec, proto)` 的函数，可 `--check` | 达成（生成物维度） | 双跑字节一致 + 清单 `--check` 零写入；运行期装配仍是 L3/L4 组合 |

缺口以 `OpaqueCount` 与 `doctor` 的 `opaque:` 清单量化：数字越小，搬进声明式拓扑的部分越多。

## 7. 常见问题

- **`--plan` 打印了但服务没起来**：这是设计行为——`--plan` 以 0 退出，不进入 Run。
- **`doctor` 报段覆盖错误**：与启动同源，先按 [配置系统排错](./Bald%20配置系统.md) 处理（引入 Provider / 白名单 / 删段）。
- **`plan hash` 每次不同**：`Compile` 要求声明侧是纯函数——自定义 Provider 若在 `Plan` 里读环境变量、随机数或时间，会破坏确定性；把不稳定的值放到 `Build` 阶段。
- **`build tags` 显示 `(none)`**：`go build` 未传 `-tags`；需要按 tag 分叉时用 `go build -tags v2` 构建后再看。
