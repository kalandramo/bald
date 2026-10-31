// 装配计划 IR。
//
// Plan 是装配的**可检视投影**：Provider 声明、域、组件、能力接管与契约段
// 消费被编译成一组带阶段/优先级/依赖的节点，供 `--plan`、测试与未来的
// `bald doctor` 使用。节点即执行顺序的唯一来源：引擎按计划构建
// Provider，顺序来自 Node 的 Phase/Priority/DependsOn（无手工排序表）。
//
// 纯函数边界：Compile 只读契约与声明，不写文件、不改全局 logger、不构建
// Provider、不执行钩子（I1）。契约装载复用 Serve 的两遍预装载（只读文件/
// env，store 用完即关）。
//
// 文件按关注点拆分：本文件是类型与入口契约；编译/投影在 plan_compile.go，
// 摘要与稳定键在 plan_hash.go，诊断在 plan_diagnostics.go，路由冲突在
// plan_routes.go，拓扑排序在 plan_order.go，段消费视图在 plan_segments.go，
// 输出格式在 plan_explain.go。
package appkit

import "fmt"

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

func (p Phase) String() string {
	switch p {
	case PhaseConfigure:
		return "configure"
	case PhaseBootstrap:
		return "bootstrap"
	case PhaseWire:
		return "wire"
	case PhaseServe:
		return "serve"
	case PhaseDrain:
		return "drain"
	case PhaseTeardown:
		return "teardown"
	default:
		return fmt.Sprintf("phase(%d)", uint8(p))
	}
}

// MarshalText 让计划 JSON 里的阶段是稳定字符串而不是裸数字。
func (p Phase) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

// UnmarshalText 与 MarshalText 对称——计划 JSON 必须可回读（计划缓存依赖
// 这一点，见 plan_cache.go）。
func (p *Phase) UnmarshalText(b []byte) error {
	for _, ph := range []Phase{PhaseConfigure, PhaseBootstrap, PhaseWire, PhaseServe, PhaseDrain, PhaseTeardown} {
		if ph.String() == string(b) {
			*p = ph
			return nil
		}
	}
	return fmt.Errorf("appkit: unknown phase %q", string(b))
}

// ReloadPolicy 声明配置变更时该节点的处理方式。
type ReloadPolicy uint8

const (
	ReloadStatic    ReloadPolicy = iota // 重启生效（默认；诚实缺省）
	ReloadReconcile                     // 引擎可按 want/have 协调
	ReloadRestart                       // 明确需要进程重启
)

func (r ReloadPolicy) String() string {
	switch r {
	case ReloadStatic:
		return "static"
	case ReloadReconcile:
		return "reconcile"
	case ReloadRestart:
		return "restart"
	default:
		return fmt.Sprintf("reload(%d)", uint8(r))
	}
}

// MarshalText 同 Phase：JSON 中输出可读字符串。
func (r ReloadPolicy) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalText 与 MarshalText 对称（计划 JSON 可回读）。
func (r *ReloadPolicy) UnmarshalText(b []byte) error {
	for _, rp := range []ReloadPolicy{ReloadStatic, ReloadReconcile, ReloadRestart} {
		if rp.String() == string(b) {
			*r = rp
			return nil
		}
	}
	return fmt.Errorf("appkit: unknown reload policy %q", string(b))
}

// ResourceKey 是资源的稳定身份（序列化/日志/计划里出现的字符串形态）。
type ResourceKey string

// Origin 标记节点来源（std 后端 / 第三方 Provider / 域 / 应用 / 变体）。
type Origin string

const (
	OriginStd      Origin = "std"
	OriginProvider Origin = "provider"
	OriginDomain   Origin = "domain"
	OriginApp      Origin = "app"
	// OriginVariant 表示节点/路由来自命中变体的声明增量（Node.Variant 给出
	// 变体名）。declared 变体不再产 ambient 诊断——来源在节点上（I11）。
	OriginVariant Origin = "variant"
)

// DiagnosticLevel 是计划诊断的级别。
type DiagnosticLevel string

const (
	DiagInfo  DiagnosticLevel = "info"
	DiagWarn  DiagnosticLevel = "warn"
	DiagError DiagnosticLevel = "error"
)

// Diagnostic 是一条计划诊断（覆盖/白名单/接管等逃逸舱的可见化）。
type Diagnostic struct {
	Level   DiagnosticLevel `json:"level"`
	Code    string          `json:"code"`
	Subject string          `json:"subject,omitempty"`
	Message string          `json:"message"`
}

// Node 是计划中的一个资源节点。
type Node struct {
	Key       ResourceKey   `json:"key"`                 // 如 "database.sql"、"domain.greet"
	Kind      string        `json:"kind"`                // server | database | logger | domain | component | app
	Segments  []string      `json:"segments,omitempty"`  // 读取的契约段（输入，不构成所有权）；框架段路径或 biz.<path>
	DependsOn []ResourceKey `json:"dependsOn,omitempty"` // 资源键依赖（决定 Apply 拓扑序）
	// TeardownAfter 仅在「停机序 ≠ Apply 逆序」时声明（"*" = 全部其它节点）。
	TeardownAfter []ResourceKey `json:"teardownAfter,omitempty"`

	Phase    Phase        `json:"phase"`
	Priority int          `json:"priority"` // 同阶段 ready 集合的显式排序键（越小越先）
	Reload   ReloadPolicy `json:"reload"`
	Override bool         `json:"override,omitempty"` // 显式覆盖同 Key 节点
	Reason   string       `json:"reason,omitempty"`   // Override 的理由

	// Opaque 表示该节点内部是命令式闭包，计划只能看到边界（期望态无法由
	// 声明派生）：Domain Wire、程序化 Registrar、命令式组件、std server
	// （注册回调装进去的路由/service 不可见）均为 true；契约派生的
	// std logger/audit 节点不是 Opaque。
	Opaque bool `json:"opaque,omitempty"`

	// DesiredHash 是可协调节点（ReloadReconcile）期望态的摘要：节点身份 +
	// 声明段的契约内容。重启/静态节点为空；Opaque 只表示内部不可分析，不排除
	// 期望态摘要。
	DesiredHash string `json:"desiredHash,omitempty"`

	// EffectName 是节点停机 Effect 的名（Provider 可声明；缺省 "appkit:"+Key）。
	EffectName string `json:"effectName,omitempty"`

	Origin Origin `json:"origin"`
	// Variant 是节点来源变体名（Origin == OriginVariant 时非空）：命中变体
	// 声明的组件进计划时带来源，不再靠 ambient 诊断旁白（I11）。
	Variant string `json:"variant,omitempty"`
}

// SegmentUse 是一个契约段的消费视图。
type SegmentUse struct {
	Path        string        `json:"path"`
	Consumers   []ResourceKey `json:"consumers,omitempty"` // 资源键
	Whitelisted bool          `json:"whitelisted,omitempty"`
	// Framework 为 true 表示该段由框架自身无条件消费（app/logger 的默认
	// 通道、预填 server 段等），没有用户可见节点。
	Framework bool `json:"framework,omitempty"`
}

// AppMeta 是计划中的应用元数据（与装配期 kitOpts 的取值一致）。
type AppMeta struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Env     string `json:"env,omitempty"`
}

// Imperative 是 Decl 中命令式槽位在计划里的可见化（§6 Data/Imperative 分区）：
// 闭包不参与排序、不可分析，但必须可见——否则 --plan 会漏报配置热更订阅者、
// 停机效果、生命周期钩子、运行期服务器工厂与服务器能力声明。只记录种类与
// 声明名/键/序号，不假装它们是数据节点。
type Imperative struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

// Key 是槽位身份（Kind + Name），供排序与计划差异对比复用。
func (i Imperative) Key() string { return i.Kind + "\x00" + i.Name }

// Plan 是编译结果。Nodes 已按全局拓扑序稳定排序。
type Plan struct {
	App       AppMeta `json:"app"`
	Role      string  `json:"role,omitempty"`
	InputHash string  `json:"inputHash"` // 可检视输入的摘要（契约确定性序列化）
	Nodes     []Node  `json:"nodes"`
	// OpaqueCount 是 Opaque 节点数（逃逸舱预算的计量口径；预算见 WithOpaqueBudget）。
	OpaqueCount int          `json:"opaqueCount"`
	Segments    []SegmentUse `json:"segments"`
	// Routes 是域声明的 REST 路由清单（Domain.RouteTable 汇总；按
	// (Domain, Path, Method, RPC) 排序，保证置换不变性）。它是清单而非实现：
	// 手写 Routes 闭包没有 RouteTable 时计划列不出来，Compile 给
	// custom-routes 警告。
	Routes []Route `json:"routes,omitempty"`
	// Variants 是构建期变体声明（AppSpec.variants 的生成物投影；按 Name
	// 排序）。声明恒进计划——不带 tag 的构建也能看到「声明了哪些变体、命中
	// 会加什么」；Active 表示当前二进制实际满足该变体的构建约束。命中的
	// 增量（Effects）并入节点/路由/契约，带 Origin=variant 来源；已声明的
	// 变体不再产 ambient 诊断（I11）。
	Variants []VariantDecl `json:"variants,omitempty"`
	// Imperative 是命令式槽位清单（effect/keyWatch/reconcile/hook/serverFunc/
	// serverCapability/onConfigChange/identity/auditor；按 (Kind,Name) 排序，
	// 保证置换不变性）。
	Imperative []Imperative `json:"imperative,omitempty"`
	Warnings   []Diagnostic `json:"warnings,omitempty"`
}
