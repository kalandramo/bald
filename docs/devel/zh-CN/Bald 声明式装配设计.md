# Bald 声明式装配设计

> 状态：Accepted。L1–L4 达成，L5 在生成物维度达成（§3.1）
> 关联：《Bald Bootstrap 设计》《Bald 配置契约设计》《路由注册与绑定设计》《代码生成工具设计》《框架契约总览》《设计判据》

---

## 摘要

**一句话：bald 的装配是「数据声明 → 可检视计划 → 受控执行 → 可收敛」四段式。**

配置是数据、声明值是一等公民、新增域装配零改动——这只是起点。一个可称为声明式的系统还必须**可计划、顺序无关、可收敛**：能在一个不执行任何代码的纯函数里回答「会起什么、依赖谁、按什么顺序停」；同一组声明换参数顺序不改变行为；配置变更时按期望态收敛而不是重启整个进程。

这套设计做了三件事：

1. **在「声明」和「执行」之间补一层 `Plan` IR**——纯函数、可打印、可 diff、可哈希；Provider/Domain 的 `Build`/`Wire` 作为兼容槽位保留，但必须在计划里显式标记为 `Opaque`（不可计划）。
2. **把顺序和覆盖从代码搬到数据**——用 `DependsOn`/`Priority`/`Key` 推导拓扑序与停机序，不再有手工排序表；重复声明与静默覆盖改为显式 `Override` + 冲突报错。
3. **把收敛从「组件挂载」扩展到「声明资源」**——为节点声明 `ReloadPolicy`，配置变更时重算计划、按 key diff、协调或明确报告「需要重启」；增加 `Drift` 查询。

同时给出 **L1–L5 声明式等级**（§3.1），用于对外诚实表述与验收。

### 当前落地范围

| 能力 | 形态 | 入口 |
|---|---|---|
| 计划 | `Compile` 纯函数产出 `Plan`；`--plan` / `bald doctor` 打印节点、依赖、段覆盖、imperative 槽位、路由、变体 | `appkit.Compile` |
| 顺序 | 拓扑序由 `Priority`/`DependsOn` 推导，停机序由 `TeardownAfter` 推导；声明置换不改 `Plan.Hash()` | 节点字段 |
| 冲突 | 重复资源键报错；覆盖需 `Override` + `Reason` | `Node.Override` |
| 资源面 | `Key[T]` 类型化访问；段路径即资源键 | `appkit.NewKey` |
| 收敛 | 节点 `ReloadPolicy`（`ReloadStatic`/`ReloadRestart`/`ReloadReconcile`）+ `Drift` | `App.Drift` |
| 生成 | `gen app`/`gen domain` 输出仅依赖 `AppSpec + proto`；`--check` 零写入、退出码可判；`_example/bald` 已迁入生成树且 `--check` 零 diff | `bald gen` |
| 逃逸舱 | `Opaque` 节点、白名单段、build tag 接线进入计划 `Warnings` 与 `doctor`；`WithOpaqueBudget` 可设预算 | `Plan.Warnings` |

消费侧现状：示例 greet 域与 bald-admin 全部 20 个域已迁到 `Domain.Declare`/`ApplyNode` 形态；`Opaque` 只剩框架自身的 std server 构造与程序化 registry 接管节点。logger/audit 由契约段派生期望态并支持 `ReloadReconcile`；database/cache/storage 等段仍为 `ReloadRestart`（重建即重启语义）。域节点可挂框架段或业务配置段 `biz.<path>`，热更按新配置快照协调；auth 域（bald-admin）与 greet 域（示例）是两处真实热更消费者。

---

## 1. 背景

### 1.1 设计动机：改造前的形态

下表是设计前的装配形态与它带来的问题。**这些问题均已由本设计解决**，保留此表是为了让后来者理解「为什么需要 `Plan` IR」，而不是记录缺陷。

| # | 问题 | 改造前的形态 | 后果 |
|---|---|---|---|
| P1 | 声明不可检视 | `Decl` 内含 `BeforeStart`/`Wire`/`Undo` 等函数字段 | 无法在不执行的情况下回答「会起什么、按什么顺序、依赖哪些段」 |
| P2 | 顺序仍是一等公民 | 段路径「末者胜」由 Go 实参顺序决定；阶段内名次与停机序是手工表；单槽字段静默后写覆盖 | 同一组声明换参数顺序可能换行为；新增段顺序靠人记 |
| P3 | 收敛范围极窄 | `Reconcile` 只覆盖组件 Mount/Unmount；database/cache/storage 明确不支持热更；无 drift 查询 | 「Kubernetes 同型」的类比大于实际能力 |
| P4 | 资源面类型擦除 | `Resource(segment) (any, bool)` + 消费侧断言 | 组合靠字符串键，重构不可静态检查 |
| P5 | 语义逃逸舱 | `Registrar` 实例接管时契约段被判「已消费」；`WithUnimplementedSegments` 可豁免任意段；build tag + `init()` 注入接线 | 「声明」不构成运行形态的完整描述 |
| P6 | 生成器是有状态同步 | `gen app` → `gen domain` 两阶段、owned/seeded/user 三态、重跑语义；装配文件本身是条件 `append` 块 | 输出不是 `f(声明)` 的纯函数，声明性止于脚手架 |

### 1.2 为什么会这样

- **Go 的约束是真实的**：没有运行期包发现，没有注解处理器，闭包是最短接线路径。用一张资源图替代闭包，短期成本不低。
- **评价标准错位**：早期验收看「写业务的人少改几个文件」（文件数、LOC、分层、起步时间），而不是「系统能否被计划、重放、收敛」。前者达成了，后者没有立项。
- **单点补丁先行**：「声明有、消费者无」的问题（配置段静默、死声明、漏 Close）是逐条加检查修掉的，而不是让「声明 → 消费」成为计划的结构性质。

### 1.3 设计立场

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

| 等级 | 定义 | 判定方式 | 改造前 | 现状 |
|---|---|---|---|---|
| **L1 声明式配置** | 配置形状由 schema 定义，未知键/未消费段可检测 | 契约 proto + strict + 覆盖检查 | **达成** | **达成** |
| **L2 可计划拓扑** | 装配拓扑可由纯函数算出并打印；执行前可审阅 | `Compile` + `--plan` | **部分**（`AppSpec` 是数据，`Decl` 不是） | **达成**（`Compile`/`Plan`/`--plan`/`Plan.Hash`/`doctor`） |
| **L3 顺序无关组合** | 结果只由依赖图与显式优先级决定，与实参/import 顺序无关 | 置换测试 + 无手工排序表 | 未达成 | **达成**（`Phase`/`Priority`/`DependsOn` 取代手工表，置换测试） |
| **L4 收敛运行** | 声明资源可 level-triggered 协调；漂移可观测 | 配置变更/漂移 e2e | 未达成（仅组件子集） | **达成**（节点 `ReloadPolicy` + `Drift` + `Reconcile`；域节点可挂框架段或 `biz.<path>` 业务段） |
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
               │  Build（唯一副作用入口）
┌──────────────▼───────────────┐
│ 3. 构建 Build                │  按拓扑序构建；失败逆序回滚
│    Effect 账本不变            │  Cleanup 由计划推导
└──────────────┬───────────────┘
               │  Run + 配置变更/周期
┌──────────────▼───────────────┐
│ 4. 收敛 Reconcile            │  重算 Plan → 按 Key diff →
│    Drift 报告                │  Reconcile / RestartRequired
└──────────────────────────────┘
```

`Serve(ctx, opts...)` 是 `Compile` → `Build` → `Run` 的固定组合；`--plan` 在第一步后打印并退出，不触发任何副作用。

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
	Kind      string        // server | database | logger | domain | component | hook
	Segments  []string      // 读取的契约段（输入，不构成所有权）；框架段路径或 biz.<path>（业务配置）
	DependsOn []ResourceKey // 资源键依赖（决定 Build 拓扑序）
	// TeardownAfter 仅在「停机序 ≠ Build 逆序」时声明；
	// 取代 cleanupOrder 手工表。
	TeardownAfter []ResourceKey

	Phase    Phase
	Priority int // 同阶段 ready 集合的显式排序键（越小越先）
	Reload   ReloadPolicy
	Override bool   // 显式覆盖同 Key 节点
	Reason   string // Override 的理由（计划中打印）

	// Opaque 表示该节点内部是命令式闭包，计划只能看到边界（期望态无法由
	// 声明派生）。Domain 的 Wire、程序化 Registrar、命令式组件、std
	// server（注册回调装进去的路由/service 不可见）均为 true；
	// 契约派生的 std logger/audit 节点不是 Opaque。
	Opaque bool

	// DesiredHash 是节点期望态的摘要（可协调节点必须提供）。
	DesiredHash string

	Origin Origin // std | provider | domain | app | variant
}

// Plan 是编译结果。Nodes 已按全局拓扑序稳定排序。
type Plan struct {
	App       AppMeta
	Role      string
	InputHash string // 可检视输入的摘要（见 4.3 的 Describer）
	Nodes     []Node
	Segments  []SegmentUse
	Routes    []Route     // 域声明的 REST 路由清单（Domain.RouteTable 汇总；Q5 可见性）
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

### 4.3 Provider v2：Plan/Build 分离

```go
type PlanScope struct {
	Config   *bootstrapv1.BootstrapConfig // 已定形的契约（只读）
	Decl     *Decl                        // 声明（只读约定）
	Role     string
	Settings map[string]any               // 合并后的配置快照（只读，含业务段）
}

type ApplyScope struct {
	Config *bootstrapv1.BootstrapConfig
	Decl   *Decl
	Role   string
	App    *App  // 资源容器：阶段 B 起非 nil，前序节点产物已填充
	Node   Node  // 正在构建的节点
}

type Provider interface {
	Option
	Segments() []string
	// Plan 必须是纯函数：不写文件、不发网络、不改全局、不读环境。
	// 返回的 Node 必须包含 Key/Phase/Reload；DependsOn 可引用其他节点。
	Plan(ctx context.Context, s PlanScope) ([]Node, error)
	// Build 是唯一允许产生副作用的入口；返回产物按 Node.Key 入库。
	Build(ctx context.Context, s ApplyScope, n Node) (any, Cleanup, error)
}
```

方法名是 `Build` 而非 `Apply`（`Option` 已占用 `Apply(*Decl)`，Go 无方法重载）；语义上它与 `Plan` 成对。Provider 只有 v2 一种形态：`std.Standard()` 的三个 Provider 是原生 v2 范本，第三方 Provider 直接实现 `Plan`/`Build`。`Build` 仅在「至少一条声明路径实际出现于契约」时被调用（段缺失 = 跳过）；`config` 段无条件构建。

### 4.4 顺序与冲突：从代码搬到数据

**排序算法（唯一实现，引擎内）**：

1. 按 `Phase` 分组；阶段顺序固定为 `Configure → Bootstrap → Wire → Serve → Drain → Teardown`。
2. 阶段内按 `DependsOn` 做 Kahn 拓扑排序。
3. ready 集合的次序为 `(Priority, Key)` 字典序——**绝不使用声明下标**。
4. 有环 → 编译期报错，错误信息打印环路径。
5. 停机序默认是 Build 全局拓扑序的逆序；`TeardownAfter` 覆盖局部次序。
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

	// 以下为兼容槽位。未声明 ApplyNode 时，引擎把它们归入
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

**声明 + 执行**（已实现）：`ApplyNode` 在域窗口（业务 `BeforeStart` 之后、server 构建之前）按计划序执行声明节点，节点按 Key 区分职责；`app.HTTPHandler()` 提供 `WithHTTP` 声明的业务 handler（路由节点挂载用）。两条边界由计划期 fail-fast 兜住：`ApplyNode` 与 `Wire`/`Routes` 互斥（双写）；声明节点不得消费 `server.grpc`——`grpc.Server` 在域窗口里还不存在，服务注册只能走 `Domain.Services`（server 构建时的延迟回调）。`Services` 因此与 `ApplyNode` 共存合法，且不必为它声明节点（它不在域窗口执行，不存在双写）。声明了 `ApplyNode` 的域不再合成 `domain.<name>.wire`，逃逸舱归零：`_example/bald` 的 greet 域 `nodes: 7 (opaque 2)`（只剩程序化 registry 与 std server）。

声明节点的 `Segments` 有两种合法形态：`BootstrapConfig` 的真实字段路径（如 `server.http`），或业务配置路径 `biz.<path>`（Q8 已落）。拼错的段不会出现在 L3 覆盖检查里，期望态摘要还会退化成 Key/Kind 常量（reconcile 节点永不触发）——计划期直接报错；`biz.` 前缀后必须是非空、无空段的点路径。

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

`Declare`/`ApplyNode` 不是强制的：没声明的域走 `Wire`/`Routes` 兼容槽位，合成一个 Opaque 节点；`bald doctor` 的 `domains:` 行给出 `declared` / `legacy` 计数与名单，用于量化 L3 达成度（见 §7）。参考工程与下游 bald-admin 的域都已改用该形态，域不再贡献逃逸舱，Opaque 只剩框架自身的程序化 registry 与 std server。大量同形域可复用 `domains.StandardDomain`（构造 biz + 委托 hgin 注册），每域只保留自己的构造体与注册体；需要热更的域（如登录）手写 `Declare`/`ApplyNode`/`Reconcile`，把 `biz.login` 声明为 `ReloadReconcile` 节点，`--plan` 会显示 `domain.auth.biz reload=reconcile segments=biz.login`。

### 4.7 执行、回滚与停机

- `Build` 只按 `Plan.Nodes` 的顺序执行；引擎不维护「阶段 B 循环 + 追加钩子」这类隐式拼接。
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
  - **build tag 分叉**：`doctor` 的 `build tags:` 行来自 `debug.ReadBuildInfo`（构建事实）；应用侧语义由变体声明（`WithVariantDecls` / `AppSpec.variants`）承担——条件按 `go/build/constraint` 求值，声明与增量恒进计划（未命中可审计），命中即并入装配（Q7 终态，§8.3）；已声明变体不再走 `ambient`。尚未迁移的手写分叉仍可用 `WithAmbient` 显式登记，进 `Plan.Warnings` 的 `ambient` 诊断——计划不嗅探 build tag，只记录声明者告诉它的事实。重名裁决在计划期完成：同 `Name` 同 `Detail` 合并为一条诊断（与声明序无关），同 `Name` 不同 `Detail` 报错，避免同一身份出现两种描述。
- 计划缓存：按 `(Role, InputHash)` 写入 `.bald/plan-cache.json`，仅作对比基线，不作为执行依据。**已实现**：`appkit.WithPlanCache(path)` 声明路径（生成物 `bootstrap.Options()` 默认声明 `.bald/plan-cache.json`），`Serve` 装配成功后记录、`doctor` 只读对比（无缓存整行省略；缺失/损坏只提示）；写入按 16 条上限去重裁剪，原子 rename。对比的「plan unchanged」判据限定为装配声明投影（节点 + `Plan.Imperative` 槽位 + `Plan.Routes` 路由清单 + `Plan.Variants` 变体声明，报告行显式标注这一范围，不断言整份 `Plan.Hash` 相等）：节点身份对比为空但 effect/keyWatch/reconcile 槽位变了时逐条报增删（`imperative slots changed`）；路由清单变化单列 `routes changed`（逐条 + / -——路由是声明面的一部分，「节点没变」不再掩盖换路径）；变体声明的增删、`tags` 改写与命中态翻转单列 `variants changed`（Q7 对账口径——未命中的变体不产诊断，只有进装配判据才不被「节点没变」掩盖）；节点属性变化报 `plan changed: node attributes (...)`（路由/变体已单列时不再冒用），不谎报未变；诊断（`Plan.Warnings`，含 `ambient`）按身份 diff 单列 `diagnostics changed`；InputHash/段视图各有独立报告行。

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

- `gen --check`：在内存中重新生成，与清单/磁盘比对；有差异打印 unified diff，退出码 1；**不写任何文件**；上次清单里 owned、本次不再生成的文件按 `stale:` 列出（它们仍在树里参与编译，`--adopt` 负责删除）。
- `--adopt` 取代 `--force`：把已有文件纳入清单（记录当前哈希），而不是静默覆盖；并删除上次清单记为 owned、本次不再生成的旧产物（`removed:` 逐条打印；seeded 播种后归用户、user 文件永不删除，清单版本不符时跳过删除）。
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

> **现状**：**可见性**已落——`Domain.RouteTable []appkit.Route` 进 `Plan.Routes`（`--plan`/`doctor` 打印、参与计划哈希且置换不变），同一 `(方法, 路径)` 跨域在计划期报错，gin 挂不上去的形态（同位置通配参数名不一致、catch-all 与更长路径并存、未命名或位置非法的通配符）同样在计划期报错，声明 `Routes` 闭包却无清单的域给 `custom-routes` 警告（手写路由不假装可分析）；示例 greet 域已声明自己的表。**绑定生成默认开启（Q5 D1）**：`gen domain` 从 `google.api.http` 注解生成 `handler/gin/<d>.bind.gen.go`（`<Name>Service` 接口 + `Bind<Name>(gin.IRouter, <Name>Service)`），路由/路径变量/查询与 body 由 `transport/web.BindProtoRequest` 装入 protojson、响应经 `WriteProtoResponse` 编码，实现只需实现接口。两条流水线前置由生成期把关：proto 的 Go 类型缺失时先按 `--proto-cmd`（默认 `buf generate`）在 `--out` 根编排 proto 阶段并复查产物，命令失败或仍无产物即硬错误（点名命令、缺失包、`--proto-cmd`/`--no-bind` 两条修法）；`transport/web` 缺失给 `go get` 修法。`--check` 零写入，不执行外部命令，前置缺失只报修法。`--no-bind` 是显式逃生口（REST 面完全手写／proto 阶段不归本命令管）；`--rest-bind` 退役为兼容开关（deprecated，等价于不给）。注解的 `additional_bindings` 逐条生成别名路由（同一 RPC 只实现一次）；带 REST 注解的流式 RPC 在生成期报错（gin/网关无法转码）；注解里的 `custom` 动词与 `response_body` 不在支持面内，生成期同样报错并点名 RPC 与字段（静默丢弃 `custom` 或忽略 `response_body` 会让 REST 面偏离注解契约——作者应手写该路由）；路径变量的跨段 pattern（如 `{name=projects/*/locations/*}`）与归一化后重名的变量同样生成期报错（gin 无法表达多段变量，静默退化成单段会让匹配语义偏离注解）。绑定读的是 proto 里 `option go_package` 的**字面值**：buf managed mode 的 `go_package_prefix` 若覆盖它，两者必须指向同一个 Go 包（示例 proto 已对齐到 `.../example/bald/api/gen`，与 `api/buf.gen.yaml` 的 `go_package_prefix` 同源）。**计划可见性随绑定一起生成**：同一批注解同时产出 `internal/<apiserver_dir>/routes/<d>.gen.go`（`appkit.Route` 清单，叶子包只 import `appkit`），域声明值引用 `RouteTable: routes.<Name>` 即让 `--plan`/`doctor` 列出注解派生路由、跨域冲突在计划期报错——注解仍是唯一真相源，不重复维护。清单放独立叶子包（只 import `appkit`）：域包引用它不会引入对传输实现的依赖。同一域同时保留手写 `RouteTable` 与生成清单会得到计划期错误（同名重复登记，点名处置方式）。**清单逃逸面**：`--no-routes` 与绑定生成连用只产 handler 绑定、不产路由清单，域保留手写 `RouteTable`（生成执行、清单自持）；与 `--no-bind` 连用是空操作，生成期拒绝。决策依据与两态验收见 §8.4。

---

## 5. 语义不变量

每条不变量对应一个可执行测试，作为 CI 门；不满足即不能宣称对应等级。

| # | 不变量 | 测试 |
|---|---|---|
| I1 | `Compile` 无副作用：不改全局 logger、不写文件、不发网络 | `TestPlan_NoSideEffects`（canary 全局 + fs 只读挂载） |
| I2 | 计划顺序无关：100 组随机 `opts` 置换得到同一 `Hash` | `TestPlan_OrderInvariant` |
| I3 | 计划完整：每个出现的契约段要么有消费者、要么白名单；每个节点要么被依赖、要么是终端 | `TestPlan_SegmentCoverage` |
| I4 | 冲突显式：重复 Key 默认失败；`Override` 必须带 `Reason` | `TestPlan_Conflict` |
| I5 | 停机序 = 逆拓扑（受 `TeardownAfter` 修正） | `TestTeardownOrder_ApplyReverse`、`TestTeardownOrder_Constraints`、`TestTeardownOrder_RandomDAG`（随机 DAG） |
| I6 | 应用失败按已应用节点逆序回滚；重试幂等 | `TestComponent_StartFailureRollback`、`TestMountComponent_MountFailureRollback` |
| I7 | 漂移可观测：移除一个可协调资源后 `Drift` 报 `Missing` | `TestDrift_DetectsMissing` |
| I8 | 生成确定：双跑字节一致；`--check` 零 diff 且 stale owned 可见 | `TestGen_Deterministic`、`TestGen_CheckClean`、`TestGenDomain_Bind_Deterministic`、`TestCheckTree_ReportsStaleOwned` |
| I9 | 公开 API 无未类型化资源访问 | `TestAPI_NoUntypedResource`（AST 守卫） |
| I10 | 逃逸舱可见：白名单段、override、build tag 分叉均出现在 `Plan.Warnings` | `TestPlan_EscapeHatchesReported` |
| I11 | 已声明变体不进 ambient：声明与增量以计划实体出现（来源 `variant:<name>`），同一身份不得再有手写 `WithAmbient` 旁白 | `TestVariantProjection_DeclarationsAlwaysInPlan`、`TestVariantProjection_RejectsAmbientDuplicate`、`TestVariantEffects_ComponentProjection` |
| I12 | 声明/实现对账：编译进来的 `VariantImpl` 集合与按当前二进制构建约束求值的命中集合一致，漂移（实现多于声明、实现编入但条件为假、组件名不对称）启动即失败 | `TestVariantEffects_RejectsImplDrift`、`TestTree_VariantsProjectedAtRuntime` |

---

## 6. 兼容性约定（现行）

装配面没有新旧两套语义。接手时只需知道当前形态；下列是已在公开面上**不存在**或**行为更严**的点：

- **Provider 只有 v2**：`Plan`/`Build` 成对，v1 形态与过渡适配器（`AdaptLegacy`/`LegacyProvider`/`BuildScope`/`PriorityLegacy`/`OriginLegacy`）不存在；第三方 Provider 直接实现 v2。
- **资源访问唯一入口是 `Key[T]`**：`App.Resource(string)` 不存在，`Key[T].Resource(app)` 是唯一写法。
- **重复资源键是编译错误**：同名 Key（除非显式 `Override` + `Reason`）不会静默末者胜。
- **手工 `Registrar` 与 registry 段并存须 `Override`**：不标注即编译错误，避免两套注册源悄悄打架。
- **生成物以清单为准**：`gen --force` 由 `--adopt` 取代；清单缺失时 `gen --check` 判定为不干净。
- **`AppSpec` 快捷方式已删除**：`server`/`audit_backends`/`bundle_normalized` 字段号 2/5/6 与字段名均 `reserved`，传输面与审计后端统一声明在 `bootstrap`；旧 `--spec` 输入出现这些字段时 protojson 直接报 `unknown field`。

### 6.1 与既有成果的关系

- 配置契约（`bconf`）、StrictContract、L3 覆盖检查、Gate1/2/3 **全部保留**，并前移到 `Compile` 阶段：L3 从「启动期检查」变成「计划期检查」，`--plan` 就能看到。
- 效应账本（T1）、能力声明（S1）、组件生命周期（C1）、运行期挂载（A1）**保留**；它们成为 `Build`/`Reconcile` 的实现机制，而不是并列概念。
- `domains.gen.go` 继续作为「Go 无包发现」的桥；它是「域名 → Domain 值 → 计划节点」的注册表。
- `Decl` 保留，但字段分区：`Data`（可计划）与 `Imperative`（闭包，Opaque）。公开面只暴露 Data + 少量显式 Imperative 槽位。

---

## 7. 不变量怎么验证

### 7.1 性质与可执行的验证

| 性质 | 验证 |
|---|---|
| 置换不变 | 100 组随机 `opts` 置换，`Plan.Hash()` 一致 |
| 无手工排序表 | `TestAPI_NoManualOrderingTables`（grep `stageBRank`/`cleanupOrder` 同类标识符 = 0） |
| 段覆盖 | `TestPlan_SegmentCoverage`：参考工程 `--plan` 的 `Segments` 覆盖率 100%，`unknown` = 0 |
| 冲突可见 | `TestPlan_Conflict` + doctor：重复 Key / override / 白名单全部出现在计划与 doctor |
| 资源访问类型化 | `TestAPI_NoUntypedResource`（AST 守卫）：`Resource(string)` 调用点 = 0 |
| 收敛覆盖 | `TestDrift*`：`ReloadReconcile` 节点数 ≥ 组件 + logger + audit |
| 生成确定性 | `TestGen_Deterministic`/`TestGen_CheckClean`（guard 夹具）双跑字节一致；`_example/bald` 的 `appspec.json` 声明 roles/bootstrap/domains/variants，`gen app --check` 与 `gen domain --no-bind --check` 均零差异；手写部分只留在 seeded 钩子（`custom.go`/`apiserver.go`/`cmd/*/app/custom.go`）与域包；自定义接线扩展（§8.5）由 `TestTree_CustomWiringHooks`/`TestTree_CustomWiringReachesPlan` 钉住 |
| `Opaque` 预算 | 参考工程 ≤ 3 个（当前：std server + 程序化 registry），超预算由 `WithOpaqueBudget` 报错 |

### 7.2 验收脚本

落地形态是 `task acceptance`（只跑 `go test`，跨平台；逐条对应本节）：

```bash
# 计划纯净 + 顺序无关 + 路由清单（可见性/冲突/gin 形态校验）
go test ./pkg/appkit -run 'TestPlan_(NoSideEffects|OrderInvariant|SegmentCoverage|Conflict|DomainRoutes|CustomRoutesWarning)|TestRouteShapeConflict'

# 停机序与回滚（按节点数据推导，不是手工表）
go test ./pkg/appkit -run 'TestTeardownOrder_|TestProviderPlan_StdTeardownOrder|TestComponent_StartFailureRollback|TestMountComponent_MountFailureRollback'

# 漂移
go test ./pkg/appkit -run 'TestDrift'

# 计划缓存基线与构建变体（Q7 对账口径：变体声明/命中态变化必须可见；
# 变体声明恒进计划、命中按构建约束求值、增量并入装配——I11/I12）
go test ./pkg/appkit -run 'TestPlanCache_|TestVariantProjection_|TestVariantEffects_|TestOption_OnRoleScoping|TestDoctor_'

# API 守卫（§7.1 两条「= 0」指标的可执行形态）
go test ./pkg/appkit -run 'TestAPI_'   # Resource(string) 调用点、手工排序表标识符

# 生成确定性：生成器自有树（guard 夹具）双跑一致 + --check 零 diff
# （绑定/路由清单默认生成、--no-bind/--no-routes 两态、proto 阶段编排与生成期前置拒绝同受约束；--adopt 清理过期 owned）
go test ./internal/codegen -run 'TestGen_(Deterministic|CheckClean)|TestGenDomain_(Bind|Proto|NoRoutes|CheckDoesNotRunProtoStage)|TestGenApp_Variants|TestTree_Variants|TestPruneStaleOwned|TestCheckTree_|TestTree_CustomWiring'

# 契约层生成物与 proto 同步（本地 buf + protoc-gen-go；工具链缺失时跳过）
(cd bconf && go test . -run TestGeneratedMatchesProto)

# 参考工程的计划口径（Opaque 预算 / 路由可见 / 无逃逸警告 / 段覆盖）
(cd _example/bald && go test ./internal/bootstrap -run TestPlan_ReferenceProject)
# 同一属实在 -tags grpcgw 构建下再跑一遍（I12：实现编入但条件求值为假即失败）
(cd _example/bald && go test -tags grpcgw ./internal/bootstrap -run TestPlan_ReferenceProject)

# 示例生成树与声明零差异（appspec.json 是唯一真相源）
go run ./cmd/bald gen app --spec _example/bald/appspec.json --out _example/bald --check
go run ./cmd/bald gen domain --spec _example/bald/appspec.json --out _example/bald --no-bind --check

# 综合诊断
bald doctor --config configs/bald-demo-api.yaml
```

> `_example/bald` 已迁入生成树：`appspec.json` 是声明真相源，生成物
> （assemble/variants/domains/授权投影 + configs + taskfile）由 `gen app` 与
> `gen domain --no-bind` 写出，`--check` 零差异；手写接线只留在 seeded 钩子
> （`internal/bootstrap/custom.go`、`internal/apiserver/apiserver.go`、
> `cmd/*/app/custom.go`）与域包（`greetv1`、`user`）。示例的 REST 面由域自持
> （`Domain.RouteTable` + `registerRoutes`），故 `gen domain` 走 `--no-bind` 逃生口；
> 域包已声明 `Domain` 时生成器跳过骨架（不覆盖手写域值）。

---

## 8. 风险与决策记录

### 8.1 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| 第三方 Provider 在 `Plan` 里做 I/O | `--plan` 不纯，计划不可信 | API 契约 + `TestPlan_NoSideEffects` canary；文档明确「违反即破坏 --plan」；长期可子进程沙箱 |
| `Plan`/`Build` 两套逻辑漂移 | 计划与实际不一致 | 节点 `DesiredHash` 在 Build 后校验；e2e 比对计划与资源面 |
| 类型化 Key 引入包循环 | 工程结构复杂 | Key 与 `appkit` 同包但只依赖接口；出现循环再抽 `bald/resource` |
| 收敛范围扩大导致语义事故 | 热更不安全的资源被误协调 | 默认 `ReloadStatic`；DB/连接池只 `ReloadRestart` |

### 8.2 设计决策记录（Q1–Q9）

| # | 问题 | 结论 |
|---|---|---|
| Q1 | 是否接受 Provider v2 的破坏？ | 接受；Provider 只有 v2，无过渡适配器 |
| Q2 | `Plan` 纯度如何强制？ | 首版契约 + canary 测试；不引入沙箱 |
| Q3 | `Key[T]` 落 `pkg/appkit` 还是独立 module？ | 先 `pkg/appkit`，若出现循环再抽 `bald/resource` |
| Q4 | 收敛首版覆盖哪些资源？ | logger/audit/组件；DB/cache/storage 仅 `ReloadRestart` |
| Q5 | 路由是否由生成器接管？ | 是，默认生成；`--no-routes` 保留自定义。**可见性已落**：`Domain.RouteTable` → `Plan.Routes`（`--plan`/`doctor` 可见、置换不变、跨域同路由计划期报错、gin 挂不上去的形态——通配参数名不一致、catch-all 与更长路径并存、未命名/位置非法的通配符——同样计划期报错、手写 `Routes` 无清单给 `custom-routes` 警告）；**绑定生成默认开启**（Q5 D1：`gen domain` 注解 → gin 路由 + 服务接口 + protojson 编解码；同批注解另出 `internal/<apiserver_dir>/routes/<d>.gen.go` 的 `appkit.Route` 清单，域引用 `RouteTable: routes.<Name>` 即获计划可见性）；**proto 阶段编排已落**（proto Go 类型缺失时按 `--proto-cmd`（默认 `buf generate`）在 `--out` 根执行并复查产物，失败即硬错误；`--check` 零写入不执行外部命令）；**前置校验已落**（缺 `github.com/kalandramo/bald/transport/web` 给 `go get` 修法，不再产出编译不过的树）；**逃逸面**：`--no-bind` 跳过绑定与清单、`--no-routes` 只跳过清单（域保留手写 `RouteTable`），二者连同生成期拒绝都在 `TestGenDomain_*` 两态里钉住；`--rest-bind` 退役为兼容开关。决策记录见 §8.4。可见性口径：清单一域自持 REST 面，非域路由不在其中 |
| Q6 | `AppSpec` 快捷方式何时移除？ | **已移除**（见 §6）：proto 删除字段 2/5/6 并 `reserved` 号与名；Gate1 投影一致性校验与 `normalizeBootstrap` 归一化随之删除（契约段唯一真相源 = `bootstrap`，未声明时按空配置派生默认键）；`RoleSpec.server` 作为角色级覆盖保留。验收：`TestGate1_RemovedShortcutFieldsRejected`（旧字段 protojson 拒绝）、`TestContractBootstrap`、`bconf` 的 `TestGeneratedMatchesProto` |
| Q7 | build tag 分叉如何进入声明？ | **终态**：`AppSpec.variants` 声明 `when`（`go/build/constraint` 表达式）、`build_tags`（&& 糖，与 `when` 互斥）与 `effects`（组件 + bootstrap 补丁）；声明恒进 `Plan.Variants`（未命中可审计），命中的变体把增量并入真实装配、不产 `ambient`（I11）；`//go:build` 实现经 `variantImpls` 注册，与声明在计划期对账（I12），漂移即启动失败；生成物拆「声明/数据文件不分叉 + 每变体一份 tag 文件」；缓存对账沿用 `variants changed`。未声明为变体的环境注入走 `WithAmbient` 显式登记（产诊断，不进计划节点）。口径见 §8.3 |
| Q8 | 域自有业务配置（`biz` 段）如何进入可协调节点的期望态？ | **已落**（按草案实现）：`Node.Segments` 接受 `biz.<path>`，计划期从合并 Settings 快照按完整点路径取子树、JSON 确定性序列化算 `DesiredHash`（段缺失/有值可区分）；`Domain.Declare` 校验纳入 biz 段，`Domain.Reconcile` 在热更时按新快照协调。快照口径：`Compile`/`--plan` 为文件 + env，真实启动/热更为含 flag 的完整合并 |
| Q9 | AppSpec 表达不了的自定义接线（第三方 Provider/拦截器/探针/CLI 子命令/变体接线）如何进生成物树？ | **已落**：三个 seeded 钩子承载，生成物只调用、从不覆盖——`customOptions(role)`（装配，末尾追加、末者胜）、`customVariantOpts(name)`（变体接线，带 tag 的 init 登记 `variantOpts`）、`customSubcommands()`（CLI 业务子命令）。口径见 §8.5；验收 `TestTree_CustomWiringHooks`/`TestTree_CustomWiringReachesPlan` |

### 8.3 Q7 终态：`AppSpec.variants` 声明 + 增量

结论先行：build tag 分叉的**声明面**（存在、条件、命中后的增量）全在 AppSpec；`//go:build` 只决定**实现代码是否编进来**，两者由 I12 在计划期对账，漂移即启动失败；已声明变体不产 `ambient` 诊断（I11），未声明的手写分叉经 `WithAmbient` 显式暴露。

数据形状（bconf 增量字段，不破坏既有 spec）：

```proto
message Variant {
  string name = 1;                // 稳定身份：计划来源 variant:<name>、文件 variant_<name>.gen.go
  repeated string build_tags = 2; // when 的迁移糖（&& 连接），与 when 互斥
  string description = 3;         // 人读说明（空则给默认说明）
  string when = 4;                // go/build/constraint 表达式，如 "grpcgw && !nacos"
  VariantEffect effects = 5;      // 命中时的装配增量（未命中也进计划，供审计）
}
message VariantEffect {
  repeated ComponentSpec components = 1; // 复用全局组件声明（name/kind/config_prefix/params）
  repeated VariantPatch patches = 2;     // bootstrap 字符串字段改写
}
message VariantPatch { string path = 1; string value = 2; string reason = 3; }
// AppSpec 增加：repeated Variant variants = 11;
```

**命中求值**（V1）：`appkit.EvaluateBuildConstraint` 用 `go/build/constraint` 解析求值，tag 集 = `-tags` ∪ GOOS/GOARCH/release tags（与 `debug.ReadBuildInfo`、`doctor` 的 `build tags` 行同源）——「本地命中、CI 不命中」不会静默发生。`when` 与 `build_tags` 二选一且必须非空（Gate1）；`when` 须单行、可解析。手写声明允许空 `when` = 无条件（展示为 `(always)`），生成物由 Gate1 要求条件非空。

**增量语义**（`VariantEffect`，命中才并入装配，未命中只随声明进计划）：
- `components`：组件名即计划节点身份 `component.<name>`，跨全局与跨变体唯一（Gate1 生成期拒绝 + appkit 计划期拒绝）；`config_prefix` 进计划段消费视图；节点来源写 `variant:<name>`。
- `patches`：点路径改写 bootstrap 字符串字段；字段必须存在且是 `string`；覆盖非空值必须给 `reason`（产 `variant-patch` 诊断，可见不阻断）；两个生效变体把同一路径改成不同值 = 编译错误（I4 同款「不猜谁赢」）。
- `routes` 只存在于手写 `VariantImpl`（挂载是闭包）：声明了 routes 就必须提供 `WrapHTTP`，未声明 routes 不得提供；应用没有 HTTP 面（`WithHTTP`）时声明 routes 直接报错。生成物的 AppSpec 只描述组件与补丁——路由实现必须手写。

**声明/实现对账（I12）**：`VariantImpl` 由 `//go:build` 文件经 `WithVariantImpl` 注册。计划期要求：实现必须在声明面里；实现的变体按当前二进制求值必须命中（`//go:build` 漂移 → `has drifted from the declaration`）；组件按 `Name` 一一对称（少/多都报错）；同一变体实现重复注册报错。声明了组件增量却没有实现编入同样报错。

**生成产物（双文件，清单驱动剪枝）**：
- `internal/bootstrap/variants.gen.go`（不随 tag 分叉）：全部声明 + 增量数据进 `variantOptions()`，并遍历 `variantImpls` 注册实现——任何构建都能审计声明面。
- 每个变体一份 `internal/bootstrap/variant_<name>.gen.go`：`//go:build <when>` 头 + `init()` 向 `variantImpls` 追加 `appkit.VariantImpl`（按 `effects.components` 逐个 `buildComponent`）。
- 两者纳入 `--check`/`--adopt`；声明删除后旧实现文件随 owned 清单剪枝；未声明 `variants` 的树不落任何变体文件。
- Gate1：`name` 必填、匹配 `^[a-z][a-z0-9_]*$`、全应用唯一；条件二选一非空；`build_tags` 逐项单 tag、无空白、不重复；`effects.components.kind` 白名单、`name` 跨全局与变体唯一；`effects.patches.path` 非空、无首尾空白、单行（字段存在性与 `reason` 留给 appkit 对默认契约校验——AppSpec 的 `bootstrap` 段是部分声明）。

**计划与 doctor 口径（I11）**：`Plan.Variants` 恒列全部声明（按 `Name` 排序），`--plan`/`doctor` 的 `variants:` 段列出 `declared`/`active` 并逐条展示增量（`component X (segment Y)` / `patch path=value (reason: …)`）——未命中的变体也能回答「命中会加什么」。已声明变体不产 `ambient`：增量以计划实体出现（节点来源、路由 Domain `variant:<name>`、补丁诊断），与 `WithAmbient` 描述同一身份是硬错误（删掉 WithAmbient 即可）。计划缓存对账口径不变：变体声明、条件、命中态进装配哈希投影，`doctor` 单列 `variants changed`。

**多角色二进制**：角色过滤是装配语义，不是 build tag 的替代——`appkit.OnRole(role, opts...)` 把变体实现贡献的 Option 只落到服务该传输面的角色（示例：网关接线只落 API 角色，worker 契约没有 `server.http`）；nacos 这类与传输面无关的实现不限定角色。

**关键取舍（必须读）**：
1. 条件从「`build_tags` 全部命中」升级为 `go/build/constraint` 表达式（`grpcgw && !nacos`），`build_tags` 保留为 && 糖；构建 tag 列表不再是判据本体。
2. 变体不再只是「可见性」：命中的变体把声明增量并入真实装配，与手写声明同等效力，不再产 `ambient`（I11）。
3. 拆分「声明/数据不分叉 + 每变体一份 tag 文件」：各变体只在自己的 `init()` 里注册，同组 tag 命中多个变体时天然共存、不重复定义符号。
4. 路由实现仍必须手写（闭包不可声明），生成物只保证声明与组件/补丁数据同源；两边由 I12 对账。

**框架测试**：`TestVariantProjection_*`（声明恒进计划/排序/默认说明/`when`+`build_tags` 互斥/空 `when` 无条件/ambient 重名拒绝）、`TestVariantEffects_*`（组件/路由/补丁投影、未命中声明可见、`reason` 要求、跨变体冲突、I12 漂移表、二次应用幂等、置换不变）、`TestOption_OnRoleScoping`（角色限定）、`TestGenApp_VariantsProjectedToPlan`（双产物内容 + `--check` 零差异）、`TestGenApp_VariantsPrunedWhenUndeclared`（声明移除后剪枝）、`TestTree_VariantsProjectedAtRuntime`（同一棵树 `-tags grpcgw` 与不带 tag 两态：增量只在高命中态出现且无 ambient）、`TestGate1_Variants`（各条拒绝）、`TestEvaluateBuildConstraint`（隐式 tag 与非法表达式）。

手写应用与生成物共用同一运行期投影；`WithAmbient` 用于描述未声明为变体的环境注入。

### 8.4 Q5 默认开启 REST 绑定：前置事实与 D1 决策

默认生成要求两个前置：proto 的 Go 产物存在、消费方依赖 `transport/web`。下表是决策时的消费方事实，用于说明为什么这两项可以消除而不成为阻塞：

| 消费方 | 是否走 `bald gen` | `google.api.http` 注解 | 前置依赖 |
|---|---|---|---|
| `_example/bald` | 是（`appspec.json` + manifest；`gen app` + `gen domain --no-bind`） | `greet.proto`：1 `get` / 1 `post` / 1 `body`，无 `custom`、无 `response_body` | `go.mod` 已 require `transport/web` |
| `bald-admin` | 否（无 `appspec.json`/manifest） | 13 个带注解的 proto（全仓 14 个 service）：`get`/`post`/`put`/`delete`/`patch` + `body`，**0 `custom`、0 `response_body`** | `backend/go.mod` 已 require `transport/web v0.1.0`；`backend/api/gen/go` 有 buf 产物 |

结论：

1. **注解表达力不是阻塞项**：两个真实消费方都没有 `custom` 动词或 `response_body`；生成期对这两类字段的拒绝（宁可报错也不静默偏离注解）保持原样。
2. **阻塞是流水线前置，且可以消除**：默认开启后，未跑 proto 生成（`go_package` 目录无 Go 文件）或未依赖 `transport/web` 的 `gen domain` 会失败。前者由生成器自己编排 proto 阶段解决，后者必须由作者补依赖（生成器不该替消费方改 `go.mod`）。
3. **影响面有限**：当前两仓都不从 spec 生成，默认开启不迁移任何既有消费树；受影响的是新生成树与夹具（`TestTree_GeneratedProjectCompiles` 等）。

**owner 决策：D1——bald 编排 proto 阶段，绑定默认生成。**

- **默认生成**：`gen domain` 对每个带 `google.api.http` 注解的域产出 `handler/gin/<d>.bind.gen.go` 与 `routes/<d>.gen.go`。注解是唯一真相源，不生成绑定等于把 REST 面留给手写代码——这是 Q5 要消除的缺口。
- **proto 阶段由生成器编排**：绑定前置缺失（`go_package` 目录无 Go 文件）时，在 `--out` 根以 shell 语义执行 `--proto-cmd`（默认 `buf generate`），跑完复查产物；命令失败或仍无产物即**生成期硬错误**，错误点名命令、缺失包与两条修法（`--proto-cmd` / `--no-bind`）。绑定文件内容只由注解与 rpc 签名决定，与 proto 生成物内容无关，因此「是否在本机执行过 buf」不改变生成字节——确定性不受影响。
- **`--check` 零写入**：不执行外部命令（执行会写盘，违背零写校验），前置缺失直接报可执行修法。CI 里 proto 产物应已存在，`--check` 仍是纯读。
- **`--no-bind` 显式逃生**：跳过注解派生绑定与路由清单，用于「REST 面完全手写」或「proto 阶段不归本命令管」的树。此时不要求 `transport/web` 依赖与 proto Go 产物。
- **`--rest-bind` 退役为兼容开关**：绑定默认生成后它等价于不给，标记 deprecated 但继续解析，旧脚本不破。
- **`--no-routes` 语义随之收敛**：单独使用合法（绑定照常、清单跳过，域保留手写 `RouteTable`）；与 `--no-bind` 连用是空操作，生成期拒绝。
- **两态验收**：默认态（生成绑定 + 清单）与 `--no-bind` 态（仅骨架）都在 `--check` 对账；proto 阶段覆盖「编排成功」「编排失败」「`--check` 不执行」三条路径。

被否决的路线：B（前置缺失自动降级 + 告警）会让同一份声明在不同环境产出不同文件集，破坏生成确定性；C（默认硬失败、要求先跑 buf）与「一条命令产出一棵可编译的树」的流水线目标冲突。D1 把 proto 阶段纳入生成器，才让「默认生成」与「确定性」同时成立。

### 8.5 Q9 自定义接线扩展：三个 seeded 钩子

问题：`assemble.gen.go` 与 `command.go` 是 owned 生成物，`Options()` 只能表达 AppSpec 能声明的东西；第三方 Provider、Registrar、拦截器链、探针数据源、Opaque 预算、业务 CLI 子命令在声明面之外，重生成即被抹掉。两条路可选：(1) 往 proto 里加「接线函数名字符串」字段——Godoc 不可达、拼错只在编译期炸、且守卫要求每个字段有消费者；(2) 固定文件名 + 固定签名的 seeded 钩子——零 proto 变更、生成物只调用、用户文件永不覆盖。选 (2)。

钩子契约（生成为 `Seeded`：播种一次，之后归用户维护；`--adopt` 不覆盖、`--check` 只判存在性）：

| 文件 | 钩子 | 生成物调用点 | 语义 |
|---|---|---|---|
| `internal/bootstrap/custom.go` | `customOptions(role string) []appkit.Option` | `Options(...)` 末尾（`variantOptions()` 之后） | 末尾追加 = 同名单值字段末者胜：可覆盖生成的面/健康检查/拦截器默认值；多角色树 `role` 为当前角色名，单角色为空串 |
| 同上 | `customVariantOpts(name string) []appkit.Option`（读 `variantOpts` 表） | 每个 `variant_<name>.gen.go` 的 `init()` 把返回值填进 `VariantImpl.Opts` | 变体接线仍随 `//go:build` 编译：带 tag 的文件 `init()` 里登记 `variantOpts["grpcgw"] = ...`。实现登记与声明对账（I12）仍由生成物负责，杜绝「用户另登记一份 impl」造成的重复实现判错 |
| `cmd/<app>/app/custom.go` | `customSubcommands() []*cobra.Command` | `appcli.WithSubcommands(customSubcommands()...)` | 业务子命令与默认 serve 并存（每个角色一份，因每个 cmd 目录独立） |

设计约束：

- **生成物确定性不变**：钩子内容不参与 owned 文件字节，`--check` 不会因用户接线判脏；seeded 文件不进 stale owned 清理。
- **不引入 proto 字段**：接线是 Go 代码，不是声明数据；字符串函数名会把编译期错误推迟到运行期，且破坏「每个声明字段有机器可验证消费者」的守卫。
- **不提供生成期枚举**：生成器不解析 `custom.go`，无法按内容改变模板；钩子只能「追加/覆盖 Option」，不能新增生成物或改变 plan 形状——计划的可分析性仍由声明面负责（接线对计划的影响走 Provider 的 `Plan`）。
- **`Opts` 由变体文件经表取出**：带 tag 的 `init()` 先于 `assemble` 的 `Options()` 执行（Go 包级 init 顺序），命中判定仍只由构建约束决定。

示例树的口径：`configs/*.yaml` 由 bootstrap 段派生（owned）、proto 目录统一到 `api/`（`api/buf.yaml` 为 seeded，示例按三插件流水线维护）、`gen domain` 遇域包已声明 `Domain` 时跳过骨架并与手写域值共存（示例走 `--no-bind` 保留域自持 REST 面）。

验收：`TestTree_CustomWiringHooks`（落盘形态与调用点、单/多角色实参派生、变体钩子路由）与 `TestTree_CustomWiringReachesPlan`（替换 seeded 文件为第三方 Provider 后，生成树真实编译且节点进入 `appkit.Compile` 计划）。

---

## 9. 设计问题 → 响应

| 设计问题 | 响应 |
|---|---|
| 声明含闭包、不可检视 | P1：Plan IR + `Opaque` 标记；闭包不被假装成数据 |
| 顺序依赖实参顺序 | P2：排序键 `(Priority, Key)` + 依赖图；置换测试 |
| 单槽静默覆盖 | P2：冲突检测 + `Override` + `Reason` |
| 资源面 `any` + 字符串 | P4：`Key[T]` 是唯一访问面 |
| 收敛只覆盖组件 | P3：节点级 `ReloadPolicy` + `Drift`；保守默认 |
| Registrar 覆盖静默 | P5：计划警告 + `Override` 显式化 |
| build tag 改变接线 | P5：`AppSpec.variants` 声明条件与增量、计划可见（I11），`//go:build` 实现与声明计划期对账（I12，§8.3）；未声明的环境注入经 `WithAmbient` 显式暴露为诊断 |
| codegen 有状态、非幂等 | P6：清单 + `--check` + 确定性；路由生成 |
| 对外称「声明式编程」 | §3.1：表述为「声明式装配（Lx）」，按等级说话 |
| 验收只量生产力 | §7.1：声明性性质由可执行测试钉住 |

---

## 10. 结论

目标不是把 bald 重写成 Terraform，而是让「声明」成为可以被检视、被置换、被 diff、被收敛的对象，并把做不到的部分诚实地标成 `Opaque`。

达到 L3 后，bald 可以合理地说「装配是声明式可组合的」；达到 L4 后，可以说「声明资源可收敛」；在此之前，对外只说「声明式装配（L1/L2）」。这比用「声明式编程」描述一个命令式内核更有利于长期演进，也能避免未消费声明被静默忽略的事故以新的形式重演。
