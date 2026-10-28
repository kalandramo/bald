# Bald 装配计划与 doctor

**一句话**：装配是一个纯函数算出来的计划——节点、依赖、段覆盖、警告与 hash。`--plan` 打印它，`--plan-format=json` 交给机器，`bald doctor` 把逃逸舱（Opaque 节点、白名单段、覆盖、build tag）集中成一份诊断。三者与真实启动走同一次 `appkit.Compile`，可在启动前审阅，也可进 CI。

## 1. 计划从哪来

```go
p, err := appkit.Compile(ctx, opts...) // 纯函数：不 Build、不跑钩子、不写文件
```

输入是**同一份**装配声明（`MustServe` 的变参项：Provider、Domain、能力 Option、`WithUnimplementedSegments`、`WithOpaqueBudget`）加上**同一份**契约（`--config` 预装载结果）。输出 `*appkit.Plan`：

| 字段 | 含义 |
|---|---|
| `App` / `Role` | 契约元数据与多角色标记 |
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
input: d5f5f80e...
nodes:
  configure  app                    priority=-1  app
                                                 segments=app
  bootstrap  logger                 priority=10  provider, reload=reconcile
                                                 segments=logger
  bootstrap  database.sql           priority=50  provider, reload=restart
                                                 segments=database.sql
  wire       domain.greet.routes    priority=10  domain, opaque
                                                 segments=server.http
                                                 dependsOn=domain.greet
  serve      server                 priority=0   std, opaque
                                                 segments=server.grpc,server.http
opaque: 5 node(s)
segments:
  server.http          consumers=domain.greet.routes,server
warnings:
  warn override [logger]: segment "logger" is declared by ...
plan hash: 5a7fcee4...
```

读法：

- **`phase`**：`configure` → `bootstrap` → `wire` → `serve` → `drain` → `teardown`。跨相位不接受 `DependsOn` 反向边（编译期报错），停机按 `TeardownAfter` 逆序。
- **`opaque`**：该节点没有声明式依赖拓扑，按 legacy 顺序执行。域在迁移到 `Domain.Declare` 之前整块是 Opaque。
- **`segments=`**：节点读取的契约段路径；`segments:` 汇总里 `consumers=` 为空且未白名单的段会在启动期报错。
- **`reload=`**：节点级重载策略，决定配置热更时协调还是重报（见 [机制层 mech](./机制层%20mech%20-%20Effect%20与期望态.md)）。

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
input: d5f5f80e...
plan:  5a7fcee4...

nodes: 8 (opaque 5)
  phase bootstrap  3
  phase configure  1
  phase serve      2
  phase wire       2
  opaque: domain.greet, domain.greet.routes, domain.greet.services, registry, server
  override: registry (programmatic takeover)

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
| `nodes` / `phase` | 计划规模与各相位分布 |
| `opaque: ...` | 逃逸舱逐个列名——白盒化 legacy 部分；`WithOpaqueBudget(n)` 超预算时在 `warnings` 出现 `opaque-budget` |
| `override: key (reason)` | 显式覆盖的节点与其理由 |
| `segments` 分组 | `consumed` 有消费者；`whitelisted` 经 `WithUnimplementedSegments` 豁免；`framework` 框架无条件消费；`unconsumed` 非空即启动失败 |
| `build tags` | 构建期 `-tags`（`debug.ReadBuildInfo` 读取），用于解释 build tag 分叉 |
| `warnings` | 与 `--plan` 同源的全部诊断（含 `override` / `whitelisted` / `opaque-budget`） |

### 3.3 只读边界

`doctor` 只做「声明 + 契约 → `Compile`」，不执行 `Provider.Build`、不跑钩子、不写文件、不联网。两点诚实说明：

- 被 import 的包的 `init()` 仍会执行（例如示例里 web 路由的 gin 注册日志）——这是 Go 运行期语义，不是 doctor 的副作用。
- 生成物清单校验属代码生成器职责（运行期二进制不 import 生成器），报告末尾给 `bald gen app --check` / `bald gen domain --check` 的出口提示，不代跑。

## 4. 退出码与 CI

| 命令 | 成功 | 失败 |
|---|---|---|
| `--plan` / `--plan-format=json` | 打印计划，退出 0 | `Compile` 错误，非 0（stderr） |
| `doctor` | 打印诊断，退出 0 | 同上 |
| `gen app --check` / `gen domain --check` | `clean: N files`，退出 0 | 列出 drift 文件，非 0 |

CI 可组合成「计划 hash 基线 + 生成物零 drift」：`--plan --plan-format=json | jq -r .hash` 与提交的基线比较；生成树跑两个 `--check`。

## 5. 声明式等级现状（对外口径）

对外表述统一为「**声明式装配**」并标注等级，而不是笼统的「声明式编程」。当前状态（判据与目标见 [设计草案 §3.1](../../devel/zh-CN/Bald%20声明式装配设计-修正草案.md)）：

| 等级 | 定义 | 现状 | 证据 / 缺口 |
|---|---|---|---|
| **L1** 声明式配置 | 配置形状由 schema 定义，未知键/未消费段可检测 | 达成 | 契约 proto + StrictContract + 段覆盖检查 |
| **L2** 可计划拓扑 | 装配拓扑由纯函数算出并打印 | 达成 | `appkit.Compile` + `--plan` + `Plan.Hash()` |
| **L3** 顺序无关组合 | 结果只由依赖图与显式优先级决定 | 达成 | 节点 `Phase`/`Priority`/`DependsOn` 取代手工排序表；置换测试 |
| **L4** 收敛运行 | 声明资源可协调，漂移可观测 | 部分 | 节点级 `ReloadPolicy` + `App.Drift` + `Reconcile`；覆盖声明式组件子集，域迁移到 `Domain.Declare` 前仍按 Wire 执行 |
| **L5** 纯函数生成 | 生成物是 `(AppSpec, proto)` 的函数，可 `--check` | 达成（生成物维度） | 双跑字节一致 + 清单 `--check` 零写入；运行期装配仍是 L3/L4 组合 |

缺口以 `OpaqueCount` 与 `doctor` 的 `opaque:` 清单量化：数字越小，搬进声明式拓扑的部分越多。

## 6. 常见问题

- **`--plan` 打印了但服务没起来**：这是设计行为——`--plan` 以 0 退出，不进入 Run。
- **`doctor` 报段覆盖错误**：与启动同源，先按 [配置系统排错](./Bald%20配置系统.md) 处理（引入 Provider / 白名单 / 删段）。
- **`plan hash` 每次不同**：`Compile` 要求声明侧是纯函数——自定义 Provider 若在 `Plan` 里读环境变量、随机数或时间，会破坏确定性；把不稳定的值放到 `Build` 阶段。
- **`build tags` 显示 `(none)`**：`go build` 未传 `-tags`；需要按 tag 分叉时用 `go build -tags v2` 构建后再看。
