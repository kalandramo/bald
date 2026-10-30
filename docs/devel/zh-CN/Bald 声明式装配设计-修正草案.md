# Bald 声明式装配设计（修正草案）

> 状态：草案 v0.1 · 2026-10-28 · 待 owner 裁决
> 修正对象：《装配层评估》§4 的目标表述、《架构优化路线》§3.6 R1-2 的收敛范围、`std.Standard()` 文档中「声明式调用」的措辞。
> 关联：《装配层评估》《Bald 配置契约设计》《路由注册与绑定设计》《代码生成工具设计》《框架契约总览》。
> 代码基线：`main@4cbd429`（2026-10-28）。

---

## 摘要

**一句话：把 bald 从「声明式门面 + 命令式接线」修正为「数据声明 → 可检视计划 → 受控执行 → 可收敛」四段式。**

当前 bald 已经做到「配置是数据、声明值是一等公民、新增域装配零改动」，但没有做到声明式系统的三项最低能力：**可计划、顺序无关、可收敛**。原因不是实现疏忽，而是设计目标一直用「生产力判据」（文件数、LOC、分层、起步时间）替代「声明性判据」，于是闭包被当作声明、手工排序表被当作实现细节、窄协调器被当作 Kubernetes 类比。

本草案不推翻已有成果，做三件事：

1. **把「声明」和「执行」之间补一层 `Plan` IR**——纯函数、可打印、可 diff、可哈希；Provider/Domain 的 `Build`/`Wire` 保留，但必须在计划里显式标记为 `Opaque`（不可计划），并逐步收缩。
2. **把顺序和覆盖从代码搬到数据**——用 `DependsOn`/`Priority`/`Key` 推导拓扑序与停机序，删除 `stageBRank`/`cleanupOrder` 手工表；重复声明与静默覆盖改为显式 `Override` + 冲突报错。
3. **把收敛从「组件挂载」扩展到「声明资源」**——为节点声明 `ReloadPolicy`，配置变更时重算计划、按 key diff、协调或明确报告「需要重启」；增加 `Drift` 查询。

同时给出 **L1–L5 声明式等级**，用于对外诚实表述与验收：草案撰写时 bald 是 **L1 + 部分 L2**，本设计目标是在 v0.26 达到 **L3**、v0.27 达到 **L4**、v0.28 达到 **L5**。

> **实施状态（2026-10-30，M0–M5 + doctor + Domain v2 + biz 段收敛已落地）**：`Compile`/Plan IR、Provider v2、删除手工排序表、`Key[T]` 类型化资源、节点级 `ReloadPolicy` + `Drift`、生成清单与 `--check`/`--adopt`、`WithOpaqueBudget`、`bald doctor`、计划缓存 `WithPlanCache`（§4.9，按 `(Role, InputHash)` 记基线、doctor 只读对比）均已交付，`WithRole` 补齐角色进计划与 Provider 作用域的缺口、`WithAmbient` 把 build tag / `init()` 注入接线记为计划 `ambient` 警告（Q7 短期形态）；`Domain.Declare`/`Domain.ApplyNode`（§4.6）已落地，示例 greet 域与 bald-admin 全部 20 个域都已迁到该形态（计划期 Opaque 只剩 std server 构造节点与程序化 registry 接管节点；std logger/audit 由契约段派生期望态，2026-10-30 起不再计为逃逸舱），`Domain.Reconcile` 补齐声明的可协调节点（计划期校验 + 热更分发）。对外口径随之更新为「声明式装配（L1–L4 达成，L5 生成物维度达成）」，见 [装配计划与 doctor](../../guide/zh-CN/Bald%20装配计划与%20doctor.md) §5。**收敛范围**：`Opaque` 预算——示例为 **2**、bald-admin 为 **1**，均达标；L4 的收敛覆盖 Provider 节点（logger/audit 的 `ReloadReconcile`）与声明域节点（`Domain.Reconcile`，2026-10-29 落地：计划期校验 + Wire/ApplyNode 成功后落账 + 热更按 Key 调钩子），域节点可挂框架段或业务配置段 `biz.<path>`（Q8，2026-10-30：计划期从 Settings 快照算期望态摘要，热更按新快照协调）；database/cache/storage 等 Provider 段仍明确不支持热更（`ReloadRestart`）。消费侧：`_example/bald` 的 greet 域已用上（`domain.greet.biz` 挂 `biz.greet` + `ReloadReconcile`，配置改动即重建端口）；bald-admin 的 auth 域也已声明 `domain.auth.biz`（`biz.login`，登录防护三件整体快照替换，2026-10-30），其余域仍为静态节点。§6 的 Data/Imperative 分区已起步：`effect`/`keyWatch`/`reconcile` 命令式槽位、`BeforeStart`/`AfterStart`/`BeforeStop`/`AfterStop` 生命周期钩子（按声明序编号）、`WithExtraServerFunc` 服务器工厂、`WithHTTP`/`WithGatewayRegister`/`WithGRPC`/`WithGRPCOptions` 服务器能力面（`serverCapability`）以及 `OnConfigChange` 回调进入 `--plan`/`doctor` 的 `imperative` 段（闭包不隐身，2026-10-30）。能力由框架测试钉住（`TestPlan_BizSegmentDesiredState`、`TestHotReload_ReconcilesBizSegment`）。

---

## 1. 背景

### 1.1 评审发现的问题

| # | 问题 | 现状证据 | 后果 |
|---|---|---|---|
| P1 | 声明不可检视 | `Decl` 内含 `BeforeStart`/`Wire`/`Undo` 等函数字段（`pkg/appkit/decl.go:38`） | 无法在不执行的情况下回答「会起什么、按什么顺序、依赖哪些段」 |
| P2 | 顺序仍是一等公民 | 段路径「末者胜」由 Go 实参顺序决定（`pkg/appkit/provider.go:279`）；`stageBRank`/`cleanupOrder` 手工表（`provider.go:219`/`:230`）；单槽字段静默后写覆盖（`decl.go:63`） | 同一组声明换参数顺序可能换行为；新增段顺序靠人记 |
| P3 | 收敛范围极窄 | `Reconcile` 只覆盖组件 Mount/Unmount（`pkg/appkit/reconcile.go:143`）；database/cache/storage 明确不支持热更；无 drift 查询 | 「Kubernetes 同型」的类比大于实际能力 |
| P4 | 资源面类型擦除 | `Resource(segment) (any, bool)` + 消费侧断言（`pkg/appkit/resource.go:18`） | 组合靠字符串键，重构不可静态检查 |
| P5 | 语义逃逸舱 | `Registrar` 实例接管时契约 registry 段被判「已消费」（`segment_coverage.go:124`）；`WithUnimplementedSegments` 可豁免任意段；build tag + `init()` 注入接线（`_example/bald/internal/bootstrap/register_grpcgw.go:1`） | 「声明」不构成运行形态的完整描述 |
| P6 | 生成器是有状态同步 | `gen app` → `gen domain` 两阶段、owned/seeded/user 三态、重跑语义（`internal/codegen/appspec_gen.go:841`、`gen_domain.go:554`）；`assemble.gen.go` 本身是 `opts = append(...)` 条件块（`appspec_gen.go:314`） | 输出不是 `f(声明)` 的纯函数，声明性止于脚手架 |

### 1.2 为什么会这样

- **Go 的约束是真实的**：没有运行期包发现，没有注解处理器，闭包是最短接线路径。用一张资源图替代闭包，成本和收益在 2024–2026 年的迭代节奏下不划算。
- **评估的目标错位**：四条验收判据（`装配层评估.md:11`）度量的是「写业务的人少改几个文件」，不是「系统能否被计划、重放、收敛」。前者达成了，后者没有立项。
- **单点补丁先行**：`script` 段静默、`gateway` 死声明、`Dispose` 漏 Close 都是「声明有、消费者无」；修复方式是逐条加检查，而不是让「声明 → 消费」成为计划的结构性质。

### 1.3 本草案的态度

不做「为了声明式而声明式」的重写。凡是闭包能解决且不需要被计划/收敛的地方，继续用闭包；但必须把它标成 `Opaque`，让用户和工具都知道「这里不可分析」。声明式能力的扩张以**可验收的不变量**为单位，而不是以文档措辞为单位。

---

## 2. 目标与非目标

### 2.1 目标

| # | 目标 | 可验收形态 |
|---|---|---|
| G1 | 计划可检视 | `appkit.Compile(ctx, opts...) (*Plan, error)` 纯函数；`--plan` 打印节点/依赖/段覆盖 |
| G2 | 顺序无关 | 同一组声明打乱实参顺序，`Plan.Hash()` 不变（100 组随机置换） |
| G3 | 冲突显式 | 重复资源键默认报错；覆盖必须 `Override` + `Reason`，计划中可见 |
| G4 | 资源类型化 | 公开面提供 `Key[T]` + `Key[T].Resource(app)`；`Resource(string)` 降级为 deprecated |
| G5 | 可收敛 | 配置变更后按节点 `ReloadPolicy` 协调/报告；`app.Drift(ctx)` 给差异 |
| G6 | 生成纯函数 | `gen` 输出仅依赖 `AppSpec + proto`；`gen --check` 零写入、退出码可判 |
| G7 | 逃逸舱可见 | 所有 `Opaque` 节点、白名单段、build tag 接线进入计划 `Warnings` 与 `bald doctor` |

### 2.2 非目标

- 不做运行期包自动发现（Go 无此能力；`domains.gen.go` 继续承担清单）。
- 不替换业务域内部的 Go 接线（`Wire` 里构造限流器、仓储仍可命令式；只要求它声明依赖与产物）。
- 不做通用 DI 容器，不引入反射注入框架。
- 不承诺零分配、零反射；`Plan` 只在启动与配置变更时计算。
- 不把 `transport.Server`、`gin`、`gorm` 等具体类型抽象成框架自有 DSL。

---

## 3. 判据与等级

### 3.1 声明式等级（对外表述口径）

| 等级 | 定义 | 判定方式 | 草案时 | 现状（2026-10-29） |
|---|---|---|---|---|
| **L1 声明式配置** | 配置形状由 schema 定义，未知键/未消费段可检测 | 契约 proto + strict + 覆盖检查 | **达成** | **达成** |
| **L2 可计划拓扑** | 装配拓扑可由纯函数算出并打印；执行前可审阅 | `Compile` + `--plan` | **部分**（`AppSpec` 是数据，`Decl` 不是） | **达成**（`Compile`/`Plan`/`--plan`/`Plan.Hash`/`doctor`） |
| **L3 顺序无关组合** | 结果只由依赖图与显式优先级决定，与实参/import 顺序无关 | 置换测试 + 无手工排序表 | 未达成 | **达成**（`Phase`/`Priority`/`DependsOn` 取代手工表，置换测试） |
| **L4 收敛运行** | 声明资源可 level-triggered 协调；漂移可观测 | 配置变更/漂移 e2e | 未达成（仅组件子集） | **达成**（节点 `ReloadPolicy` + `Drift` + `Reconcile`；域节点可挂框架段或 `biz.<path>` 业务段，2026-10-30） |
| **L5 纯函数生成** | 生成物是 `(AppSpec, proto)` 的纯函数，可 `--check` diff | 双跑字节一致 + check 零 diff | 未达成 | **达成（生成物维度）**（清单 + `--check` 零写入 + 双跑一致） |

对外文案从「声明式编程」改为「**声明式装配（Lx）**」，并在 README/指南中给出当前等级。这是本草案唯一要求立即执行的文档变更。

### 3.2 设计判据 D1–D6

后续所有设计取舍按这六条打分，不再用「文件数/LOC」单独论证声明性：

- **D1 可检视**：不执行即可得到结构化的「将要发生什么」。
- **D2 确定性**：相同输入（含声明集合的无序语义）得到相同计划。
- **D3 可组合**：新增单元只声明依赖与产物，不修改引擎。
- **D4 幂等**：重复计划/应用/协调朝同一终态收敛。
- **D5 完整性**：声明必被消费；未消费必须显式豁免并可见。
- **D6 类型化**：公开组合面尽量保留静态类型，字符串键只存在于序列化边界。

---

## 4. 目标模型

### 4.1 四段式

```
┌──────────────────────────────┐
│ 1. 声明 Declare              │  AppSpec（数据） + Contract（数据）
│    代码声明能力/域/Provider   │  + Options（受控写入 Decl）
└──────────────┬───────────────┘
               │  Compile（纯函数，无副作用）
┌──────────────▼───────────────┐
│ 2. 计划 Plan（IR）            │  Node/Edge + 段覆盖 + 警告 + Hash
│    可打印 / 可 diff / 可缓存  │  Opaque 节点显式标记
└──────────────┬───────────────┘
               │  Apply（唯一副作用入口）
┌──────────────▼───────────────┐
│ 3. 执行 Apply                │  按拓扑序构建；失败逆序回滚
│    Effect 账本不变            │  Cleanup 由计划推导
└──────────────┬───────────────┘
               │  Run + 配置变更/周期
┌──────────────▼───────────────┐
│ 4. 收敛 Reconcile            │  重算 Plan → 按 Key diff →
│    Drift 报告                │  Reconcile / RestartRequired
└──────────────────────────────┘
```

`Serve(ctx, opts...)` 变为 `Compile` → `Apply` → `Run` 的固定组合；`--plan` 在第一步后打印并退出，不触发任何副作用。

### 4.2 Plan IR

新增文件 `pkg/appkit/plan.go`（概念草案）：

```go
package appkit

// Phase 是装配阶段（数据，不再是隐式钩子追加序）。
type Phase uint8

const (
	PhaseConfigure Phase = iota // 构造期：契约 → 配置层
	PhaseBootstrap              // 框架资源：registry/tracer/metrics/database/cache/storage/audit/...
	PhaseWire                   // 业务 BeforeStart + 域接线
	PhaseServe                  // server 构造与监听
	PhaseDrain                  // 停机：server Stop
	PhaseTeardown               // 逆序释放
)

// ReloadPolicy 声明配置变更时的处理方式。
type ReloadPolicy uint8

const (
	ReloadStatic    ReloadPolicy = iota // 重启生效（默认；诚实缺省）
	ReloadReconcile                     // 引擎可按 want/have 协调
	ReloadRestart                       // 明确需要进程重启（计划中给出诊断）
)

// ResourceKey 是资源的稳定身份（序列化/日志/计划里出现的字符串形态）。
type ResourceKey string

// Node 是计划中的一个资源节点。
type Node struct {
	Key       ResourceKey   // 如 "database.sql"、"domain.greet"
	Kind      string        // server | database | logger | domain | component | hook | legacy
	Segments  []string      // 读取的契约段（输入，不构成所有权）；框架段路径或 biz.<path>（业务配置）
	DependsOn []ResourceKey // 资源键依赖（决定 Apply 拓扑序）
	// TeardownAfter 仅在「停机序 ≠ Apply 逆序」时声明；
	// 取代 cleanupOrder 手工表。
	TeardownAfter []ResourceKey

	Phase    Phase
	Priority int // 同阶段 ready 集合的显式排序键（越小越先）
	Reload   ReloadPolicy
	Override bool   // 显式覆盖同 Key 节点
	Reason   string // Override 的理由（计划中打印）

	// Opaque 表示该节点内部是命令式闭包，计划只能看到边界（期望态无法由
	// 声明派生）。Provider v1 的 Build、Domain 的 Wire、命令式组件、std
	// server（注册回调装进去的路由/service 不可见）在迁移期均为 true；
	// 契约派生的 std logger/audit 节点不是 Opaque。
	Opaque bool

	// DesiredHash 是节点期望态的摘要（可协调节点必须提供）。
	DesiredHash string

	Origin Origin // std | provider | domain | app | legacy
}

// Plan 是编译结果。Nodes 已按全局拓扑序稳定排序。
type Plan struct {
	App       AppMeta
	Role      string
	InputHash string // 可检视输入的摘要（见 4.3 的 Describer）
	Nodes     []Node
	Segments  []SegmentUse
	Warnings  []Diagnostic
}

type SegmentUse struct {
	Path        string
	Consumers   []ResourceKey // 资源键
	Whitelisted bool          // WithUnimplementedSegments
	// Framework 表示该段由框架自身无条件消费（app/logger 默认通道、
	// bconf 预填的 server 段等），没有用户可见节点。M0 实现补充：
	// 预填段在计划里必须有非空的消费说明，否则 I3 的「无未覆盖段」
	// 断言无法区分「框架消费」与「漏消费」。
	Framework bool
}

func (p *Plan) Hash() string          // 对规范化 JSON 求 sha256
func (p *Plan) Explain(w io.Writer)   // 人读格式
func (p *Plan) JSON() ([]byte, error) // 机读格式（--plan-format=json）
```

**关键约束**：`Plan` 里不放任何 `func`、`any`、指针或时间戳。`DesiredHash` 由 Provider 在 `Plan` 阶段计算；算不出的节点必须 `Opaque: true`。

### 4.3 Provider v2：Plan/Apply 分离

```go
type PlanScope struct {
	Contract *bootstrapv1.BootstrapConfig // 已定形的契约（只读）
	Decl     *Decl                        // 声明（只读约定）
	Role     string
}

type ApplyScope struct {
	PlanScope
	App  *App
	Node Node
}

type Provider interface {
	Option
	Segments() []string
	// Plan 必须是纯函数：不写文件、不发网络、不改全局、不读环境。
	// 返回的 Node 必须包含 Key/Phase/Reload；DependsOn 可引用其他节点。
	Plan(ctx context.Context, s PlanScope) ([]Node, error)
	// Apply 是唯一允许产生副作用的入口；返回产物按 Node.Key 入库。
	Apply(ctx context.Context, s ApplyScope, n Node) (any, Cleanup, error)
}
```

**迁移期适配器**（保证批 1 零用户破坏）：

```go
// AdaptLegacy 把 v1 Provider（只有 Build）包装成 v2：
// 计划中显示为一个 Opaque 节点，Key 由段路径合成，
// Reload 固定 RestartRequired（因为内部不可分析）。
func AdaptLegacy(p ProviderV1) Provider
```

`std.Standard()` 的三个 Provider 首批改为原生 v2，作为范本；第三方 Provider 在 v0.26 内继续走适配器，计划里会明确显示 `legacy` + `Opaque`，不假装可计划。

### 4.4 顺序与冲突：从代码搬到数据

**排序算法（唯一实现，引擎内）**：

1. 按 `Phase` 分组；阶段顺序固定为 `Configure → Bootstrap → Wire → Serve → Drain → Teardown`。
2. 阶段内按 `DependsOn` 做 Kahn 拓扑排序。
3. ready 集合的次序为 `(Priority, Key)` 字典序——**绝不使用声明下标**。
4. 有环 → 编译期报错，错误信息打印环路径。
5. 停机序默认是 Apply 全局拓扑序的逆序；`TeardownAfter` 覆盖局部次序。
6. 删除 `stageBRank`、`cleanupOrder`、`effectNameBySegment` 三张手工表。

**冲突规则**：

- 同一 `Key` 被两个节点声明：
  - 双方都未标 `Override` → **编译失败**，列出两个 `Origin`；
  - 恰好一方标 `Override` → 胜出，生成 `Diagnostic{Level: Warn, Code: "override"}`；
  - 双方都标 `Override` → **编译失败**。
- 多节点读取同一 `Segments` 合法（段是输入，不是所有权）。
- `Registrar(instance)` 产生 `Key("registry")`、`Override: true`、`Reason: "programmatic takeover"`；契约 `registry` 段仍列为被该节点读取，计划给出警告：「registry 段由程序化实例接管，段内后端选择不生效」。
- `WithUnimplementedSegments` 的段在 `Segments` 中 `Whitelisted: true`，并进入 `Warnings`；`bald doctor` 汇总。

**为什么这样能顺序无关**：排序键里没有声明下标；`Key` 全局唯一；依赖缺失/循环都是编译错误。于是「打乱 `opts` 顺序 → 同一份计划」成为可证性质，而不是约定。

### 4.5 资源键与类型化访问

新增 `pkg/appkit/key.go`（或独立 module `bald/resource`，见开放问题 Q3）：

```go
// Key 是类型化的资源句柄。name 是稳定路径（写入 Plan/日志/序列化边界）。
type Key[T any] struct{ name ResourceKey }

func NewKey[T any](name string) Key[T] { return Key[T]{name: ResourceKey(name)} }

// Name 返回非泛型的稳定身份（用于 Node/Plan 这类不可泛型化的结构）。
func (k Key[T]) Name() ResourceKey { return k.name }

// Resource 取节点产物；类型不符或未装配返回零值 + false。
func (k Key[T]) Resource(a *App) (T, bool) {
	var zero T
	v, ok := a.lookup(k.name)
	if !ok {
		return zero, false
	}
	t, ok := v.(T)
	if !ok {
		return zero, false
	}
	return t, true
}

// 旧 API 保留一个发布周期：
// Deprecated: 使用 Key[T]；该方法无法在编译期保证类型。
func (a *App) Resource(segment string) (any, bool)
```

Provider 侧导出自己的 Key，消费侧只依赖 Key 而不是字符串：

```go
// contrib/store-gorm
var SQL = appkit.NewKey[*gormcrud.Client]("database.sql")

// 业务
cli, ok := storegorm.SQL.Resource(app)
```

这不会解决所有类型问题（动态子段仍可能要 map），但把「字符串 + 断言」从公开范式降为边界实现细节，满足 D6。

### 4.6 Domain v2：从「自我接线」到「声明 + 接线」

保留现有三槽位的兼容形态，但增加可计划声明；`Wire` 在计划里变成 `Opaque` 节点：

```go
type Domain struct {
	Name string

	// Declare 是目标形态：域声明的资源、依赖与重载策略。
	// 纯函数；不构造实例。
	Declare func(ctx context.Context, s PlanScope) ([]Node, error)

	// ApplyNode 是目标形态的执行点：引擎在域窗口内按计划序（Phase →
	// Priority → Key）逐条调用 Declare 返回的节点。声明因此从「拓扑」变成
	// 「可执行资源」，域不再产生 Opaque 兼容节点。与 Wire/Routes 互斥
	//（同窗口立即执行，接线会跑两遍——装配期 fail-fast）。
	ApplyNode func(ctx context.Context, app *App, node Node) error

	// 以下为迁移期兼容槽位。未声明 ApplyNode 时，引擎把它们归入
	// Key("domain.<name>.wire") 这个 Opaque 节点。
	Wire     func(ctx context.Context, app *App) error
	Routes   func(h http.Handler) error
	Services func(s *grpc.Server)

	// Reconcile 是域内可协调节点（Declare 返回 ReloadReconcile）的协调钩子：
	// 配置热更时引擎按节点 Key 调用，把域自有资源重建到新契约。
	// 声明 ReloadReconcile 节点却缺钩子 / 缺 Wire/ApplyNode / 缺段都在计划期报错。
	Reconcile func(ctx context.Context, app *App, node Node) error
}
```

**声明 + 执行**（已实现）：`ApplyNode` 在域窗口（业务 `BeforeStart` 之后、server 构建之前）按计划序执行声明节点，节点按 Key 区分职责；`app.HTTPHandler()` 提供 `WithHTTP` 声明的业务 handler（路由节点挂载用）。两条边界由计划期 fail-fast 兜住：`ApplyNode` 与 `Wire`/`Routes` 互斥（双写）；声明节点不得消费 `server.grpc`——`grpc.Server` 在域窗口里还不存在，服务注册只能走 `Domain.Services`（server 构建时的延迟回调）。`Services` 因此与 `ApplyNode` 共存合法，且不必为它声明节点（它不在域窗口执行，不存在双写）。迁移完成的域不再合成 `domain.<name>.wire`，逃逸舱归零：`_example/bald` 的 greet 域迁后 `nodes: 7 (opaque 2)`（只剩程序化 registry 与 std server）。

声明节点的 `Segments` 有两种合法形态：`BootstrapConfig` 的真实字段路径（如 `server.http`），或业务配置路径 `biz.<path>`（Q8 已落，2026-10-30）。拼错的段不会出现在 L3 覆盖检查里，期望态摘要还会退化成 Key/Kind 常量（reconcile 节点永不触发）——计划期直接报错；`biz.` 前缀后必须是非空、无空段的点路径。

`biz.<path>` 的期望态取自计划编译时传入的**合并 Settings 快照**（`PlanScope.Settings`），按完整点路径（`biz` 是配置顶层段）取子树后做 JSON 序列化（map 键排序）再进摘要——同一快照稳定同摘要，子树有值 vs 存在但为空 vs 段缺失三者可区分。快照口径与运行时一致：`Compile`/`--plan` 为文件 + env 基线（无 flag 层），真实启动与热更为完整合并（含 flag）；计划期只读快照，不做 I/O。业务配置因此第一次能成为可协调节点的期望态，`Domain.Reconcile` 在热更时按新快照被调用。

可协调声明节点的契约：启动时 Wire 建立实际态、成功后节点记为已应用（Drift 不误报 Missing）；配置变更按节点 `DesiredHash` diff，钩子成功才落新摘要，失败保留旧值由 Drift 报 Stale。声明节点集合是拓扑，应跨配置稳定——热更中新出现的节点键没有登记面。

目标形态示例（`_example/bald/greetv1` 的真实形态）：

```go
var Domain = appkit.Domain{
	Name:      "greet",
	Declare:   declare,
	ApplyNode: applyNode,
	Reconcile: reconcileNode,  // biz 节点按新快照重建端口（Q8）
	Services:  registerService, // 延迟注册回调：server 构建时执行
}

func declare(_ context.Context, s appkit.PlanScope) ([]appkit.Node, error) {
	nodes := []appkit.Node{{
		Key:      "domain.greet.biz",
		Segments: []string{"biz.greet"}, // 业务配置进期望态（Q8）
		Phase:    appkit.PhaseWire,
		Priority: 10,
		Reload:   appkit.ReloadReconcile,
	}}
	if s.Decl.HTTPHandler != nil { // 网关让位时不声明路由节点
		nodes = append(nodes, appkit.Node{
			Key:       "domain.greet.routes",
			Segments:  []string{"server.http"},
			Phase:     appkit.PhaseServe,
			Priority:  10,
			DependsOn: []appkit.ResourceKey{"domain.greet.biz"},
		})
	}
	return nodes, nil
}

func applyNode(ctx context.Context, app *appkit.App, node appkit.Node) error {
	switch node.Key {
	case "domain.greet.biz":
		return wire(ctx, app) // 读 biz.greet.* 配置 → 构造能力 → 持有端口
	case "domain.greet.routes":
		return registerRoutes(app.HTTPHandler())
	}
	return fmt.Errorf("greetv1: unknown declared node %q", node.Key)
}
```

迁移期不强制所有域改 `Declare`；但 `bald doctor` 会统计 `Opaque` 域数量，作为 L3 达成的量化门槛（见 §7）。已实现：报告的 `domains:` 行给出 `declared` / `legacy` 计数与名单（2026-10-29）；`_example/bald` 的 greet 域已迁到 `ApplyNode` 目标形态（`domains: 1 (declared 1, legacy 0)`、`nodes: 7 (opaque 2)`，逃逸舱只剩框架自身的程序化 registry 与 std server）；bald-admin 的 20 个域已**全部**迁到该形态（`domains: 20 (declared 20, legacy 0)`、`nodes: 47 (opaque 1)`：biz/routes 节点由 `ApplyNode` 执行，服务注册走 `Domain.Services`），域不再贡献逃逸舱，唯一 Opaque 是 std server 构造节点。其中 18 个域拓扑同形（构造 biz + 委托 hgin 注册），声明与执行分派收敛在装配层小工具 `domains.StandardDomain`（`internal/apiserver/domains/standard.go`），每域只保留自己的构造体与注册体。2026-10-30 起 auth 域手写 Declare/ApplyNode/Reconcile，把 `biz.login` 声明为 `ReloadReconcile` 节点——首个真实下游热更（登录限流/熔断/重试整体快照替换，读路径原子 Load，段移除即真正禁用）；`bald-admin --plan` 可见 `domain.auth.biz reload=reconcile segments=biz.login`。

### 4.7 执行、回滚与停机

- `Apply` 只按 `Plan.Nodes` 的顺序执行；引擎不再有「阶段 B 循环 + 追加钩子」的隐式拼接。
- 每个节点的 `Cleanup` 入现有 Effect 账本；失败时按**已应用节点**逆序回滚，未应用节点不触碰。
- 停机序由 `TeardownAfter` + 逆拓扑推导；`audit-restore` 这类「必须最后」的语义通过 `TeardownAfter` 显式声明，不再手写 `cleanupOrder`。
- 现有五阶段 `stopAll` 保留；它成为 `PhaseDrain/PhaseTeardown` 的执行器，而不是顺序来源。

### 4.8 收敛与漂移

```go
// ReloadPolicy 在节点上声明，不再由资源类型硬编码。
// 配置变更时：
//  1. 重算 Plan（纯）；
//  2. 按 Key 比较旧/新 Node（DesiredHash + 依赖）；
//  3. ReloadReconcile → 调用节点的 Reconciler；
//     ReloadRestart  → 发 Diagnostic{"restart required"}；
//     ReloadStatic   → 静默保持，除非依赖变化。
//  4. 结果写审计事件（沿用 reconcile 审计）。

func (a *App) Drift(ctx context.Context) ([]Drift, error)
```

`Drift` 报告三类差异：

- **Missing**：计划有、运行态无（构建失败被回滚、被外部卸载）。
- **Unexpected**：运行态有、计划无（第三方运行期挂载）。
- **Stale**：`DesiredHash` 与观测不一致。

第一版只覆盖 `ReloadReconcile` 节点（logger、audit、组件、声明域节点），`database/cache/storage` 一律 `ReloadRestart`——不假装能热切连接池。这比现在「Reconcile 只覆盖组件、但文档写 Kubernetes 同型」更诚实，也为后续扩展留下数据位。域节点的期望态可来自框架段（`BootstrapConfig` 路径）或业务配置（`biz.<path>`，Q8），两者共用同一条 diff/协调路径。协调器只在阶段 B 注册（logger 走专用热更协议）：Provider 在 config/server 段声明 `ReloadReconcile` 会在计划期报错——那类节点没有重建路径，登记它只会留下永远 Missing 的漂移记录。

### 4.9 Plan 入口

- `appkit.Compile(ctx, opts...) (*Plan, error)`：公开纯函数，供测试、CLI、`doctor` 使用。
- `Serve` 识别 `--plan` / `--plan-format=json`（与 `--config` 同一 pflag 容错路径），打印后 `exit 0`，**在任何副作用之前**。
- `bald doctor`（新子命令）输出：计划摘要、`Opaque` 节点数、域迁移进度（`domains:` 的 `declared`/`legacy` 计数与名单）、白名单段、被覆盖的段、build tag 分叉、`gen --check` 结果。
  - **实现偏差**：`gen --check` 结果以「出口提示」呈现而非代跑——运行期二进制不 import `internal/codegen`（生成器是另一个执行体）；`doctor` 保持只读，CI 里两条命令并列执行。
  - **build tag 分叉**：`doctor` 的 `build tags:` 行来自 `debug.ReadBuildInfo`（构建事实）；应用侧语义由 `WithAmbient` 显式声明，进 `Plan.Warnings` 的 `ambient` 诊断——计划不嗅探 build tag，只记录声明者告诉它的事实（Q7 短期形态）。
- 计划缓存：按 `(Role, InputHash)` 写入 `.bald/plan-cache.json`，仅作对比基线，不作为执行依据。**已实现**：`appkit.WithPlanCache(path)` 声明路径（生成物 `bootstrap.Options()` 默认声明 `.bald/plan-cache.json`），`Serve` 装配成功后记录、`doctor` 只读对比（无缓存整行省略；缺失/损坏只提示）；写入按 16 条上限去重裁剪，原子 rename。对比的「plan unchanged」判据限定为装配声明投影（节点 + `Plan.Imperative` 槽位，报告行显式标注这一范围，不断言整份 `Plan.Hash` 相等）：节点身份对比为空但 effect/keyWatch/reconcile 槽位变了时逐条报增删（`imperative slots changed`），节点属性变化报 `plan changed: node attributes (...)`，不谎报未变；诊断（`Plan.Warnings`，含 `ambient`）按身份 diff 单列 `diagnostics changed`；InputHash/段视图各有独立报告行。

### 4.10 代码生成：从脚手架到纯函数

**输入规范化**：`gen` 只读 `AppSpec` 与 proto；禁止读环境变量、时间、网络、git 状态。

**清单与检查**：

```json
{
  "version": 1,
  "input": {
    "appspec": "sha256:...",
    "proto": "sha256:..."
  },
  "files": [
    {"path": "internal/bootstrap/assemble.gen.go", "sha256": "...", "provenance": "generated"},
    {"path": "internal/apiserver/router.go",       "sha256": "...", "provenance": "seeded"},
    {"path": "greetv1/greet.go",                   "sha256": "...", "provenance": "user"}
  ]
}
```

- `gen --check`：在内存中重新生成，与清单/磁盘比对；有差异打印 unified diff，退出码 1；**不写任何文件**。
- `--adopt` 取代 `--force`：把已有文件纳入清单（记录当前哈希），而不是静默覆盖。
- 三态归属保留，但由清单承载；`gen` 不再依赖「文件是否存在」来猜归属。
- 目标：`gen` 双跑字节一致（模板内 map 迭代排序），作为 CI 门。

**路由生成（L5 的关键一步）**：`gen proto` 已要求 `google.api.http` 注解；`gen domain` 从注解生成 `routes.gen.go`：

```go
// Code generated by "bald gen domain". DO NOT EDIT.
func Routes(h http.Handler) error {
	r := h.(*gin.Engine)
	r.GET("/v1/greets/:id", wrap(GreetService.GetGreet))
	r.POST("/v1/greets", wrap(GreetService.CreateGreet))
	return nil
}
```

业务只实现生成的 interface（`GreetService`），路由与授权策略从 proto 派生。既有自定义路由保留 `--no-routes` 逃逸（计划中标记 `Opaque`）。

---

## 5. 语义不变量

每条不变量对应一个可执行测试，作为 CI 门；不满足即不能宣称对应等级。

| # | 不变量 | 测试 |
|---|---|---|
| I1 | `Compile` 无副作用：不改全局 logger、不写文件、不发网络 | `TestPlan_NoSideEffects`（canary 全局 + fs 只读挂载） |
| I2 | 计划顺序无关：100 组随机 `opts` 置换得到同一 `Hash` | `TestPlan_OrderInvariant` |
| I3 | 计划完整：每个出现的契约段要么有消费者、要么白名单；每个节点要么被依赖、要么是终端 | `TestPlan_SegmentCoverage` |
| I4 | 冲突显式：重复 Key 默认失败；`Override` 必须带 `Reason` | `TestPlan_Conflict` |
| I5 | 停机序 = 逆拓扑（受 `TeardownAfter` 修正） | `TestApply_TeardownOrder`（随机 DAG） |
| I6 | 应用失败按已应用节点逆序回滚；重试幂等 | `TestApply_RollbackOnFailure` |
| I7 | 漂移可观测：移除一个可协调资源后 `Drift` 报 `Missing` | `TestDrift_DetectsMissing` |
| I8 | 生成确定：双跑字节一致；`--check` 零 diff | `TestGen_Deterministic`、`TestGen_CheckClean` |
| I9 | 公开 API 无未类型化资源访问 | `TestAPI_NoUntypedResource`（AST 守卫） |
| I10 | 逃逸舱可见：白名单段、override、build tag 分叉均出现在 `Plan.Warnings` | `TestPlan_EscapeHatchesReported` |

---

## 6. 兼容与迁移

### 6.1 批次

| 批 | 内容 | 破坏面 | 验收 |
|---|---|---|---|
| **M0** | 引入 `Plan` IR（内部由现有 `stageBRank`/`cleanupOrder` 推导）；新增 `Compile` 与 `--plan`；落 I1/I2/I3 测试 | 零 | `_example` 与 bald-admin 计划可打印；置换测试通过（若失败，记录当前缺陷基线） |
| **M1** | Provider v2 `Plan/Apply` + `AdaptLegacy`；std 三 Provider 原生化；删除三张手工表；冲突检测先告警 | Provider 作者（适配器兜底） | I4/I5 通过；手工排序表 grep 为 0 |
| **M2** | `Key[T]` / `Key[T].Resource`；`Resource(string)` deprecate；`Registrar` 覆盖显式化；`WithUnimplementedSegments` 进警告 | 资源访问方式 | I9/I10 通过；bald-admin 迁移 |
| **M3** | 节点级 `ReloadPolicy` + `Drift`；配置变更重算计划；先覆盖 logger/audit/组件，DB 类标 `ReloadRestart` | 热更语义（更保守） | I7 通过；e2e 配置变更 |
| **M4** | codegen 清单 `--check`/`--adopt`；路由从 `google.api.http` 生成；双跑确定性 | 生成物与 `--force` | I8 通过；参考工程 `--check` 零 diff |
| **M5** | 移除 deprecated API 与白名单默认；`Opaque` 节点预算纳管 | 较大 | 公开 API 收敛；L3/L4 宣称 |

### 6.2 Breaking 清单

- **B1** `Provider` 接口 v2（第三方 Provider 需改；提供 `AdaptLegacy` 一个发布周期）。
- **B2** `App.Resource(string)` 降级 deprecated，v0.28 删除。
- **B3** 重复资源键从「静默末者胜」改为编译错误（v0.26 先告警，v0.27 报错）。
- **B4** `Registrar(instance)` 与契约 registry 段同时存在时，v0.26 告警、v0.27 要求 `Override` 标注。
- **B5** `gen --force` 语义由 `--adopt` 取代；清单缺失时 `gen --check` 视为不干净。
- **B6** `AppSpec` 的 `server`/`audit_backends`/`bundle_normalized` 快捷方式在 v0.28 移除（投影门已运行两个版本）。

### 6.3 与既有成果的关系

- 配置契约（`bconf`）、StrictContract、L3 覆盖检查、Gate1/2/3 **全部保留**，并前移到 `Compile` 阶段：L3 从「启动期检查」变成「计划期检查」，`--plan` 就能看到。
- 效应账本（T1）、能力声明（S1）、组件生命周期（C1）、运行期挂载（A1）**保留**；它们成为 `Apply`/`Reconcile` 的实现机制，而不是并列概念。
- `domains.gen.go` 继续作为「Go 无包发现」的桥；它从「Option 清单」升级为「域名 → Domain 值 → 计划节点」的注册表。
- `Decl` 保留，但字段逐步分区：`Data`（可计划）与 `Imperative`（闭包，Opaque）。最终公开面只暴露 Data + 少量显式 Imperative 槽位。

---

## 7. 验收

### 7.1 声明性指标（本草案的主指标）

| 指标 | 目标 |
|---|---|
| 置换不变性 | 100 组随机 `opts` 置换，`Plan.Hash()` 100% 一致 |
| 手工排序表 | `stageBRank` / `cleanupOrder` / 同类 grep 计数 = **0** |
| 段覆盖 | 参考工程 `--plan` 中 `Segments` 覆盖率 100%，`unknown` = 0 |
| 冲突可见 | 重复 Key / override / 白名单 100% 出现在计划与 doctor |
| 资源访问类型化 | 公开 API 中 `Resource(string)` 调用点 = 0（AST 守卫） |
| 收敛覆盖 | `ReloadReconcile` 节点数 ≥ 组件 + logger + audit；`Drift` e2e 通过 |
| 生成确定性 | 生成物维度：`TestGen_Deterministic`/`TestGen_CheckClean`（guard 夹具）零 diff + 双跑字节一致；`_example/bald` 是手写超集对照（角色化 `Options(role)` + 自定义 Provider/拦截器/探针），其 `appspec.json` 未声明 roles，生成物是单角色 `Options()`——adopt 会与手写文件同包重名，故该示例不适用零 diff 判据 |
| `Opaque` 预算 | 每版本下降；v0.28 参考工程 ≤ 3 个（预期：第三方 legacy Provider） |

### 7.2 保留的生产力指标（次指标，不替代声明性指标）

- 新增业务域手写 3 文件、装配改动 0（沿用 §4.6 口径）。
- 端到端计时 ≤ 1800s（沿用现有脚本）。
- bald-admin 装配面行数不回升（基线 1,514 行）。

### 7.3 验收脚本（新增）

```bash
# 计划纯净 + 顺序无关
go test ./pkg/appkit -run 'TestPlan_(NoSideEffects|OrderInvariant|SegmentCoverage|Conflict)'

# 停机序与回滚
go test ./pkg/appkit -run 'TestApply_(TeardownOrder|RollbackOnFailure)'

# 漂移
go test ./pkg/appkit -run TestDrift

# 生成确定性：生成器自有树（guard 夹具）双跑一致 + --check 零 diff
go test ./internal/codegen -run 'TestGen_(Deterministic|CheckClean)'

# 综合诊断
bald doctor --config configs/bald-demo-api.yaml
```

> `_example/bald` 不跑 `gen app --check`：该示例是**手写装配超集**（`Options(role)`
> 角色化装配、storegorm/health/拦截器/网关等自定义接线），`appspec.json` 也没有
> `meta.roles` 与 bootstrap 段，生成物（单角色 `Options()`）与手写文件同包重名，
> adopt 后无法编译。要让示例也进生成物树，需要生成器支持「自定义接线扩展」——
> 这是 §8.2 Q5/Q7 之后的议题，当前不阻塞生成物维度的确定性验收。

---

## 8. 风险与开放问题

### 8.1 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| 第三方 Provider 在 `Plan` 里做 I/O | `--plan` 不纯，计划不可信 | API 契约 + `TestPlan_NoSideEffects` canary；文档明确「违反即破坏 --plan」；长期可子进程沙箱 |
| `Plan`/`Apply` 两套逻辑漂移 | 计划与实际不一致 | 节点 `DesiredHash` 在 Apply 后校验；e2e 比对计划与资源面 |
| 类型化 Key 引入包循环 | 工程结构复杂 | Key 放独立小 module（Q3）；`appkit` 只依赖接口 |
| 收敛范围扩大导致语义事故 | 热更不安全的资源被误协调 | 默认 `ReloadStatic`；DB/连接池首版只 `ReloadRestart` |
| 迁移期双轨（v1/v2 Provider）长期化 | 两套语义并存 | `Opaque` 预算 + 每版本递减；M5 删除适配器 |
| 下游（bald-admin）迁移成本 | 发布节奏 | 与 v0.26 同批发版；提供 `bald migrate` 自动改写简单调用 |

### 8.2 开放问题（需 owner 裁决）

| # | 问题 | 建议 |
|---|---|---|
| Q1 | 是否接受 Provider v2 的破坏？ | 接受；`AdaptLegacy` 兜底一个版本 |
| Q2 | `Plan` 纯度如何强制？ | 首版契约 + canary 测试；不引入沙箱 |
| Q3 | `Key[T]` 落 `pkg/appkit` 还是独立 module？ | 先 `pkg/appkit`，若出现循环再抽 `bald/resource` |
| Q4 | 收敛首版覆盖哪些资源？ | logger/audit/组件；DB/cache/storage 仅 `ReloadRestart` |
| Q5 | 路由是否由生成器接管？ | 是，默认生成；`--no-routes` 保留自定义 |
| Q6 | `AppSpec` 快捷方式何时移除？ | v0.28；投影门已运行两个版本 |
| Q7 | build tag 分叉如何进入声明？ | 短期计划中记录 `Ambient` 并警告——**已落**（`WithAmbient`，2026-10-29：示例 `-tags grpcgw`/`nacos` 经 `init()` 登记，`--plan`/`doctor` 出现 `ambient` 警告）；长期进 AppSpec `variants`（未做） |
| Q8 | 域自有业务配置（`biz` 段）如何进入可协调节点的期望态？ | **已落**（2026-10-30，按草案实现）：`Node.Segments` 接受 `biz.<path>`，计划期从合并 Settings 快照按完整点路径取子树、JSON 确定性序列化算 `DesiredHash`（段缺失/有值可区分）；`Domain.Declare` 校验纳入 biz 段，`Domain.Reconcile` 在热更时按新快照协调。快照口径：`Compile`/`--plan` 为文件 + env，真实启动/热更为含 flag 的完整合并 |

---

## 9. 与现有概念的映射

| 现有 | 目标 | 说明 |
|---|---|---|
| `Provider.Build` | `Provider.Plan` + `Provider.Apply` | 纯/副作用分离；v1 经适配器成为 `Opaque` |
| `Segments()` | `Node.Segments` | 段是输入；覆盖检查按节点读取集合计算 |
| `stageBRank` / `cleanupOrder` | `Phase` + `DependsOn` + `Priority` + `TeardownAfter` | 手工表删除 |
| `storeProduct` 字符串键 | `Node.Key` + `Key[T]` | 类型化访问 |
| `Domain.Wire/Routes/Services` | `Domain.Declare` + 兼容槽位 | 兼容槽位在计划中为 `Opaque` |
| `Reconcile`（组件） | 节点 `ReloadPolicy` + `Drift` | 从组件扩展到声明资源 |
| `AppSpec` 三态生成 | 清单 + `--check` | 归属由哈希清单承载 |
| `WithUnimplementedSegments` | 计划 `Whitelisted` + 警告 | 语义保留，可见性提升 |
| L3 启动期覆盖检查 | `Compile` 期覆盖检查 | 提前到计划阶段 |

---

## 10. 附录：评审问题 → 设计响应

| 评审问题 | 本草案的响应 |
|---|---|
| 声明含闭包、不可检视 | P1：Plan IR + `Opaque` 标记；闭包不被假装成数据 |
| 顺序依赖实参顺序 | P2：排序键 `(Priority, Key)` + 依赖图；置换测试 |
| 单槽静默覆盖 | P2：冲突检测 + `Override` + `Reason` |
| 资源面 `any` + 字符串 | P4：`Key[T]`；旧 API deprecate |
| 收敛只覆盖组件 | P3：节点级 `ReloadPolicy` + `Drift`；保守默认 |
| Registrar 覆盖静默 | P5：计划警告 + `Override` 显式化 |
| build tag 改变接线 | P5：计划 `Ambient` 警告（`WithAmbient`，已落）；长期 AppSpec `variants` |
| codegen 有状态、非幂等 | P6：清单 + `--check` + 确定性；路由生成 |
| 对外称「声明式编程」 | §3.1：改为「声明式装配（Lx）」，按等级表述 |
| 验收只量生产力 | §7：声明性指标为主，生产力指标为辅 |

---

## 11. 结论

这份草案不是要把 bald 重写成 Terraform。它只做一件事：**让「声明」成为可以被检视、被置换、被 diff、被收敛的对象**，并把做不到的部分诚实地标成 `Opaque`。

达成 L3 后，bald 可以合理地说「装配是声明式可组合的」；达成 L4 后，可以说「声明资源可收敛」；在那之前，对外只说「声明式装配（L1/L2）」。这比继续用「声明式编程」描述一个命令式内核更有利于长期演进，也能避免 `script` 段、`gateway` 死声明那类事故以新的形式重演。
