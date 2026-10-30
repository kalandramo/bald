// provider.go 实现段驱动的 Provider 装配抽象（v2：Plan/Build 分离）。
//
// 核心抽象：
//
//	type Provider interface {
//	    Option                        // Provider 值可直接作为 Option 传入 MustServe/NewApp
//	    Segments() []string           // 声明消费/归属的契约段路径
//	    Plan(ctx, PlanScope) ([]Node, error)          // 纯函数：声明阶段/优先级/依赖/停机约束
//	    Build(ctx, ApplyScope, Node) (any, Cleanup, error) // 唯一允许副作用的入口
//	}
//	func Providers(ps ...Provider) Option
//
// v2 把「计划」与「构建」拆开：Plan 只读契约与声明，返回带 Phase/Priority/
// DependsOn/TeardownAfter 的节点；引擎以节点为唯一排序源（不再维护
// stageBRank/cleanupOrder 手工表），Build 按节点顺序逐项执行。计划表达不了
// 的部分（Domain Wire、程序化 Registrar、命令式组件、std server）以 Opaque
// 节点进计划——如实标出，不假装可分析。
//
// 装配引擎（assemble，bootstrap.go）按 Provider 计划执行，不手写逐域调用：
//
//  1. 解析声明：收集全部 Provider，按**精确段路径**做末者胜（后声明的 Provider
//     接管同一路径——std.Standard() 的默认后端由此可被用户 Provider 覆盖；
//     不同路径并存——server.http 与 server.asynq 可同时由不同 Provider 供给）；
//     段归属重复在计划中以 override 警告显式化（B3：v0.26 告警、v0.27 报错）。
//  2. 节点归一：每个 Provider 的 Plan 产出一个节点（Phase/Priority/Key/…）；
//     引擎校验节点阶段与段族一致（config→Configure、server→Serve、其余→
//     Bootstrap），按节点对同 Key 冲突做 I4 裁决（唯一 Override + Reason 胜出，
//     其余失败）。
//  3. 段存在性 gate：段缺失 = Provider 跳过（合法）；config 段无条件构建。
//  4. 排序：orderNodes（阶段 → Kahn 拓扑 → (Priority, Key)）是唯一排序实现。
//  5. 构建：阶段 B / 迟后阶段逐节点 Build；产物入库（storeProduct）、cleanup
//     入节点槽位。
//  6. 停机：teardownOrder 由「Apply 逆序 + TeardownAfter」推出停机序（audit
//     等节点可声明 ["*"] 排到最后），Effect 按停机序的逆序注册。
//  7. L3 覆盖检查（segment_coverage.go）：段实际出现 − 已消费 − 白名单，非空
//     即启动期报错。
//
// logger 与 app 两个段**不在 Provider 构建循环里**：它们的消费是无条件的，
// 且 logger 承载两阶段重建与热更——logger Provider 的 Plan 节点进计划，
// Build 经 loggerSource 专用通道调用（见 bootstrap.go）。
package appkit

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// Cleanup 是 Provider 产物的停机释放钩子（Effect 回放执行；nil 表示无资源）。
// 两种既有清理形状（无参 func() 与 func(context.Context) error）统一为带 ctx
// 形态——与 Effect 账本的 undo 签名同构，回放路径零适配。
type Cleanup func(context.Context) error

// noopCleanup 把旧式无参清理闭包适配为 Cleanup（Provider 实现侧的便利件）。
func noopCleanup(cleanup func()) Cleanup {
	if cleanup == nil {
		return nil
	}
	return func(context.Context) error { cleanup(); return nil }
}

// PlanScope 是 Plan 的输入（纯数据；Provider 不得改写 Decl）。
type PlanScope struct {
	// Config 已定形的契约（只读）。阶段 A 的 logger Plan 可为 nil。
	Config *bootstrapv1.BootstrapConfig
	// Decl 全部装配期声明（只读）。
	Decl *Decl
	// Role 应用角色（WithRole 声明；单角色应用为空）。
	Role string
	// Settings 是合并后的配置快照（只读，含业务段）——`biz.<path>` 段的
	// 期望态摘要取自这里。Compile/--plan 为文件 + env 基线；真实启动与热更为
	// 完整合并（含 flag）。计划期不得据此做 I/O。
	Settings map[string]any
}

// ApplyScope 是 Build 的输入（PlanScope + 资源容器与当前节点）。
type ApplyScope struct {
	Config *bootstrapv1.BootstrapConfig
	Decl   *Decl
	Role   string
	// App 资源容器：阶段 B 起非 nil，且前序节点产物已填充。
	App *App
	// Node 正在应用的节点（Plan 返回的那一个）。
	Node Node
}

// Provider 消费契约段并构建该段产物（v2）。
//
// Segments 声明**契约段路径**：顶层段名（"database"、"audit"）或子段路径
// （"database.sql"、"server.http"）。路径按精确匹配做末者胜：后声明的
// Provider 接管同一路径（覆盖 std 默认后端），不同路径并存。
//
// Plan 必须是纯函数：不写文件、不发网络、不改全局、不构造实例。返回的节点
// 必须包含 Key 与 Phase；Segments 缺省取 Segments()；Kind/Origin/Reload 缺省
// 由引擎填充。M1 支持每个 Provider 恰好一个节点。
//
// Build 仅在「至少一条声明路径实际出现于契约」时被调用（段缺失 = 跳过，
// 合法语义）；config 段无条件构建。Build 失败 = fail-fast，启动中止。
// （方法名沿用 v1 的 Build：Option 已占用 Apply(*Decl)，Go 无方法重载。）
//
// Provider 嵌入 Option（Apply 把自身追加进声明）——Provider 值可直接作为
// MustServe/NewApp 的变参项，与 std.Standard()、业务域 Option 并列：
//
//	appkit.MustServe(ctx, std.Standard(), storegorm.AppProvider(), ...)
type Provider interface {
	Option
	// Segments 声明本 Provider 消费的契约段路径（非空；空路径装配期报错）。
	Segments() []string
	// Plan 返回本 Provider 的资源节点（M1：恰好一个）。
	Plan(ctx context.Context, s PlanScope) ([]Node, error)
	// Build 构建节点产物。产物形状按顶层段约定（见 storeProduct）：registry
	// 段为 registry.Registrar，六资源域为 map[string]any（键=子段名），
	// audit 段为 audit.Auditor，server 段为 []transport.Server，config 段为
	// []config.Layer；无产物可返回 nil。
	Build(ctx context.Context, s ApplyScope, n Node) (any, Cleanup, error)
}

// Providers 把多个 Provider 打包为一个 Option（单个 Provider 本身也是
// Option，二者按可读性选用）。
func Providers(ps ...Provider) Option {
	return optionFunc(func(d *Decl) {
		for _, p := range ps {
			if p != nil {
				d.Providers = append(d.Providers, p)
			}
		}
	})
}

// 优先级常量：同阶段 ready 集合的显式排序键（越小越先）。数值是 Provider
// 声明的一部分（不再是引擎手工表）；第三方可自选任意值，默认 0。
const (
	PriorityLogger   = 10 // 阶段 A/B 日志引导（先于其它阶段 B 资源）
	PriorityRegistry = 20
	PriorityTracer   = 30
	PriorityMetrics  = 40
	PriorityDatabase = 50
	PriorityCache    = 60
	PriorityStorage  = 70
	PriorityAudit    = 80 // 审计依赖 database/cache/storage 客户端就绪
	PriorityAI       = 90
	PriorityWorkflow = 100
	PriorityBroker   = 110
)

// WithUnimplementedSegments 显式豁免「契约已声明、框架尚无消费者」的段
// （L3 覆盖检查的白名单出口）。
//
// L3 默认 fail-closed：契约里出现的顶层段若无任何 Provider 消费，启动期
// 直接报错。契约先行的工位（先写配置、后等实现）经本 Option 显式承认
// 「我知道该段暂不生效」，多次调用追加。条目为顶层段名（"script"）或
// server 子段路径（"server.asynq"），且必须是 BootstrapConfig 的真实字段
// 路径——拼错的豁免默认静默失效会让 L4 名存实亡，故计划期直接报错。
func WithUnimplementedSegments(names ...string) Option {
	return optionFunc(func(d *Decl) { d.UnimplementedSegments = append(d.UnimplementedSegments, names...) })
}

// WithOpaqueBudget 声明 Opaque 节点数预算：计划期超过预算即产出
// opaque-budget 警告（不阻断装配）。0（缺省）= 不设预算，但
// Plan.OpaqueCount 恒可见——CI 可直接按计数设门。Opaque 的典型来源：
// Domain Wire、程序化 Registrar、命令式组件、std server（注册回调装进去的
// 路由/service 不可分析）；契约派生的 std logger/audit 不计入。
func WithOpaqueBudget(n int) Option {
	return optionFunc(func(d *Decl) { d.OpaqueBudget = n })
}

// segmentPathKnown 判定白名单条目是否为 BootstrapConfig 的真实字段路径
// （逐级消息型字段）。空路径 / 未知字段 / 末端非消息型均判未知。
func segmentPathKnown(path string) bool {
	if path == "" {
		return false
	}
	m := (&bootstrapv1.BootstrapConfig{}).ProtoReflect()
	for _, name := range strings.Split(path, ".") {
		fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
		if fd == nil || fd.Kind() != protoreflect.MessageKind {
			return false
		}
		m = m.Get(fd).Message()
	}
	return true
}

// ---------------------------------------------------------------------------
// 段路径工具
// ---------------------------------------------------------------------------

// segmentTop 返回段路径的顶层段名（"database.sql" → "database"）。
func segmentTop(path string) string {
	if i := strings.IndexByte(path, '.'); i >= 0 {
		return path[:i]
	}
	return path
}

// segmentPresent 按路径判定契约段是否实际出现：逐级以 protobuf 反射下钻，
// 消息型字段非 nil 即 present（与 presentTopLevelSegments 同一判定语义）。
// cfg 为 nil 或中途缺字段 → false。
func segmentPresent(cfg *bootstrapv1.BootstrapConfig, path string) bool {
	if cfg == nil || path == "" {
		return false
	}
	m := cfg.ProtoReflect()
	for _, name := range strings.Split(path, ".") {
		if !m.IsValid() {
			return false
		}
		fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
		if fd == nil || fd.Kind() != protoreflect.MessageKind {
			return false // 顶层段与中间层均为消息型；标量不构成段路径
		}
		v := m.Get(fd)
		if !v.Message().IsValid() {
			return false
		}
		m = v.Message()
	}
	return true
}

// presentChildSegments 枚举 msg 已出现的直接子段（消息型字段非 nil），
// 按字段声明序返回段名。供 server 子段覆盖检查使用（替代 bootstrap
// serverSections 手工枚举表——段名由契约反射给出，新子段自动纳入）。
func presentChildSegments(msg protoreflect.Message) []string {
	var out []string
	fields := msg.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.MessageKind {
			continue
		}
		if msg.Get(fd).Message().IsValid() {
			out = append(out, fd.TextName())
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 装配计划：解析、分阶段、排序
// ---------------------------------------------------------------------------

// providerPhase 是 Provider 的构建时机（按其声明路径的顶层段归类）。
type providerPhase int

const (
	// phaseConstruct 构造期构建（config 段：层产出要进 kitOpts，先于 NewApp）。
	phaseConstruct providerPhase = iota
	// phaseStageB 阶段 B（Run 期框架 BeforeStart 钩子内、业务钩子之前）。
	phaseStageB
	// phaseLate 迟后阶段（业务 BeforeStart 钩子之后；server 段——服务器构造
	// 消费业务建好的运行期资源，wiring_order_test 钉住该时序）。
	phaseLate
	// phaseLogger logger 段专用：不进循环，由引擎按两阶段+热更协议调用
	// （见 bootstrap.go 的 loggerSource 适配）。
	phaseLogger
)

// phaseByTop 按顶层段归类构建时机。除表内三类（config/server/logger）外，
// 一切段——含未来新增与第三方段——缺省在阶段 B 构建。
func phaseByTop(top string) providerPhase {
	switch top {
	case "config":
		return phaseConstruct
	case "server":
		return phaseLate
	case "logger":
		return phaseLogger
	default:
		return phaseStageB
	}
}

// providerEntry 把 Provider 与其 Plan 节点绑定：节点是执行与排序的唯一依据。
type providerEntry struct {
	prov Provider
	node Node
}

// providerPlan 是装配期解析完成的 Provider 计划：路径末者胜后的活跃集合，
// Plan 节点经冲突裁决（I4）与统一排序，按阶段分组。
type providerPlan struct {
	// byPath 记录每条声明路径的最终归属（末者胜；值 = active 下标+1，0 = 无归属）。
	byPath map[string]int
	// active 是末者胜后的活跃 Provider（保声明序；含被程序化 Registrar 接管
	// 而跳过构建的纯 registry Provider——L3 覆盖检查仍视其段为已消费）。
	active []Provider

	// entries 是存活节点（冲突裁决后），按全局 apply 序排列（不含 logger）。
	entries []providerEntry
	// construct/stageB/late 是 entries 的阶段视图（阶段内 apply 序）。
	construct []providerEntry
	stageB    []providerEntry
	late      []providerEntry

	// loggerProv/loggerNode 是日志装配的专用通道（恒非 nil：零声明回退内置
	// StandardLoggerProvider）。logger 不在 entries 里——它走两阶段 + 热更协议。
	loggerProv Provider
	loggerNode Node

	// applyNodes 是停机推导的输入：logger 节点 + 阶段 B 节点（apply 序）。
	applyNodes []Node
	// teardown 是停机序（先执行者在前）：Apply 逆序 + TeardownAfter 约束（I5）。
	teardown []ResourceKey
	// effectName 是节点停机 Effect 名（键 = 节点 Key；与旧 Effect 名逐字一致）。
	effectName map[ResourceKey]string

	// domainNodes 是声明域节点 → 所属域（planFrom 填充）：装配期据此为
	// ReloadReconcile 的声明节点注册域协调钩子（Domain.Reconcile）。
	domainNodes map[ResourceKey]Domain

	// unimplemented 是白名单集合（查 O(1)）。
	unimplemented map[string]bool
	// warnings 是计划期诊断（同 Key 覆盖等），由 Compile 并入 Plan.Warnings。
	warnings []Diagnostic
}

// newProviderPlan 解析 Decl.Providers：路径末者胜 → 段存在性 gate →
// Provider.Plan → 节点冲突裁决 → 统一排序。校验：空段路径、跨阶段段族、
// 每 Provider 恰一节点、节点阶段与段族一致。
func newProviderPlan(ctx context.Context, d *Decl, cfg *bootstrapv1.BootstrapConfig, settings map[string]any) (*providerPlan, error) {
	plan := &providerPlan{
		byPath:        map[string]int{},
		unimplemented: map[string]bool{},
		effectName:    map[ResourceKey]string{},
	}
	for _, name := range d.UnimplementedSegments {
		if name == "" {
			return nil, fmt.Errorf("appkit: WithUnimplementedSegments: empty segment path (want a BootstrapConfig field path like \"script\" or \"server.asynq\")")
		}
		if !segmentPathKnown(name) {
			return nil, fmt.Errorf("appkit: WithUnimplementedSegments(%q): unknown bootstrap segment path (want a BootstrapConfig field path like \"script\" or \"server.asynq\")", name)
		}
		plan.unimplemented[name] = true
	}
	if d.OpaqueBudget < 0 {
		return nil, fmt.Errorf("appkit: WithOpaqueBudget(%d): budget must be >= 0 (0 = unmanaged)", d.OpaqueBudget)
	}

	// 1. 路径末者胜：收集每条路径的末声明者（后声明覆盖先声明——std 默认
	// 后端可被用户覆盖）。
	lastOwner := map[string]int{}
	for i, p := range d.Providers {
		segs := p.Segments()
		if len(segs) == 0 {
			return nil, fmt.Errorf("appkit: provider %T declares no segments (Segments must not be empty)", p)
		}
		for _, s := range segs {
			if s == "" {
				return nil, fmt.Errorf("appkit: provider %T declares an empty segment path", p)
			}
			lastOwner[s] = i
		}
	}
	owned := map[int]bool{}
	for _, i := range lastOwner {
		owned[i] = true
	}

	// 2. 活跃集合 + Plan 纯推导。段存在性 gate 在此一次完成：config 段无条件
	// （源可独立于契约 config 段存在）、logger 恒规划（两阶段协议）、其余段
	// 缺失即跳过（合法，L2）。
	scope := PlanScope{Config: cfg, Decl: d, Role: d.Role, Settings: settings}
	var (
		allEntries []providerEntry
		haveLogger bool
		// registry 是单选域：契约同时声明 registry.etcd 与 registry.consul 且
		// 两者 Provider 都在时，后建者会静默覆盖先建者——计划期直接报错，
		// 不留给运行期。
		registryOwners []string
	)
	for i, p := range d.Providers {
		if !owned[i] {
			continue
		}
		segs := p.Segments()
		plan.active = append(plan.active, p)
		for _, s := range segs {
			plan.byPath[s] = len(plan.active) // 1-based；后写覆盖前写 = 末者胜
		}
		ph, err := providerPhaseFamily(p)
		if err != nil {
			return nil, err
		}
		// 显式 Registrar 实例接管 registry 段：纯 registry Provider 整体跳过
		//（程序化接管 = 刻意覆盖）。
		if d.Registrar != nil && providerAllTop(p, "registry") {
			continue
		}
		if ph != phaseConstruct && ph != phaseLogger && !providerTriggered(cfg, p) {
			continue
		}
		planned, err := p.Plan(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("appkit: provider %v: plan: %w", segs, err)
		}
		if len(planned) != 1 {
			return nil, fmt.Errorf("appkit: provider %v returned %d plan nodes; M1 requires exactly one", segs, len(planned))
		}
		node, err := plan.normalizeNode(p, planned[0])
		if err != nil {
			return nil, err
		}
		if ph == phaseLogger {
			if haveLogger {
				return nil, fmt.Errorf("appkit: multiple logger providers declared (%v); only one may consume the logger segment", segs)
			}
			haveLogger = true
		}
		if segmentTop(segs[0]) == "registry" {
			registryOwners = append(registryOwners, segs[0])
		}
		allEntries = append(allEntries, providerEntry{prov: p, node: node})
	}

	if len(registryOwners) > 1 {
		return nil, fmt.Errorf("appkit: multiple registry providers triggered (%v); registry is single-select "+
			"(one process registers with one registry center) — remove the extra registry segment or the extra provider", registryOwners)
	}

	// 3. 零 logger 声明回退内置 Provider（阶段 A 引导 logger 的持有者）：
	// 计划与执行同源，logger 节点恒存在。
	if !haveLogger {
		p := StandardLoggerProvider()
		planned, err := p.Plan(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("appkit: builtin logger provider: plan: %w", err)
		}
		if len(planned) != 1 {
			return nil, fmt.Errorf("appkit: builtin logger provider returned %d plan nodes; want 1", len(planned))
		}
		node, err := plan.normalizeNode(p, planned[0])
		if err != nil {
			return nil, err
		}
		allEntries = append(allEntries, providerEntry{prov: p, node: node})
	}

	// 4. 同 Key 冲突裁决（I4）→ 统一排序（阶段 → 拓扑 → (Priority, Key)）。
	nodes := make([]Node, len(allEntries))
	for i, e := range allEntries {
		nodes[i] = e.node
	}
	survivors, warns, err := resolveNodeConflicts(nodes)
	if err != nil {
		return nil, err
	}
	plan.warnings = warns
	surv := make([]Node, 0, len(survivors))
	byIndex := make(map[ResourceKey]providerEntry, len(survivors))
	for _, i := range survivors {
		surv = append(surv, nodes[i])
		byIndex[nodes[i].Key] = allEntries[i]
	}
	ordered, err := topoOrder(surv)
	if err != nil {
		return nil, err
	}

	// 5. 依 apply 序重建 entries，并抽出 logger 专用通道。
	for _, n := range ordered {
		e := byIndex[n.Key]
		e.node = n
		if segmentTop(e.node.Segments[0]) == "logger" {
			plan.loggerProv = e.prov
			plan.loggerNode = e.node
			continue
		}
		plan.entries = append(plan.entries, e)
	}
	for _, e := range plan.entries {
		switch e.node.Phase {
		case PhaseConfigure:
			plan.construct = append(plan.construct, e)
		case PhaseBootstrap:
			plan.stageB = append(plan.stageB, e)
		case PhaseServe:
			plan.late = append(plan.late, e)
		}
	}

	// 6. 停机序：logger 先于阶段 B 应用（阶段 A 引导），故 apply 序为
	// [logger, stageB...]；停机序 = 逆序 + TeardownAfter。
	plan.applyNodes = append(plan.applyNodes, plan.loggerNode)
	for _, e := range plan.stageB {
		plan.applyNodes = append(plan.applyNodes, e.node)
	}
	order, err := teardownOrder(plan.applyNodes)
	if err != nil {
		return nil, err
	}
	plan.teardown = order
	for _, n := range plan.applyNodes {
		plan.effectName[n.Key] = n.EffectName
	}
	return plan, nil
}

// normalizeNode 补齐 Plan 节点的缺省字段并校验其与 Provider 声明的一致性。
// 缺省：Segments=Segments()、Kind=首个段顶层名、Origin=provider、
// EffectName="appkit:"+Key；Phase 必须与段族一致（config→configure、
// server→serve、其余→bootstrap）。
func (p *providerPlan) normalizeNode(prov Provider, n Node) (Node, error) {
	declared := sortedUnique(prov.Segments())
	if n.Key == "" {
		return Node{}, fmt.Errorf("appkit: provider %v plan returned a node with empty key", declared)
	}
	if len(n.Segments) == 0 {
		n.Segments = declared
	}
	n.Segments = sortedUnique(n.Segments)
	for _, s := range n.Segments {
		if !containsString(declared, s) {
			return Node{}, fmt.Errorf("appkit: provider %v plan node %q consumes undeclared segment %q", declared, n.Key, s)
		}
	}
	if n.Kind == "" {
		n.Kind = segmentTop(n.Segments[0])
	}
	if n.Origin == "" {
		n.Origin = OriginProvider
	}
	if n.EffectName == "" {
		n.EffectName = "appkit:" + string(n.Key)
	}
	switch n.Phase {
	case PhaseConfigure, PhaseBootstrap, PhaseServe:
	default:
		return Node{}, fmt.Errorf("appkit: provider %v plan node %q declares unsupported phase %s (want configure, bootstrap or serve)", declared, n.Key, n.Phase)
	}
	ph := phaseByTop(segmentTop(n.Segments[0]))
	if want := phaseFor(ph); n.Phase != want {
		return Node{}, fmt.Errorf("appkit: provider %v plan node %q phase %s does not match its segment family (want %s)", declared, n.Key, n.Phase, want)
	}
	// 协调器只在阶段 B 注册（bootstrap.go 按 plan.stageB 登记）；logger 走
	// 专用热更协议。config/server 段节点声明 reconcile 只会得到一个永远
	// Missing 的漂移记录——计划期拦下，不留给运行期告警。
	if n.Reload == ReloadReconcile && ph != phaseStageB && ph != phaseLogger {
		return Node{}, fmt.Errorf("appkit: provider %v plan node %q declares reload=reconcile, but %s-phase providers have no hot-reload path "+
			"(config/server nodes are rebuilt only on restart)", declared, n.Key, segmentTop(n.Segments[0]))
	}
	return n, nil
}

// providerPhaseFamily 取 Provider 全部声明路径的共同构建时机；跨族报错
// （一次 Build 无法同时服务两种调用时机与两份 scope 约定）。
func providerPhaseFamily(p Provider) (providerPhase, error) {
	segs := p.Segments()
	ph := phaseByTop(segmentTop(segs[0]))
	for _, s := range segs[1:] {
		if got := phaseByTop(segmentTop(s)); got != ph {
			return 0, fmt.Errorf("appkit: provider %v declares segments across build phases — split it into one Provider per phase", segs)
		}
	}
	return ph, nil
}

// providerAllTop 报告 Provider 的全部声明路径都属于 top 段。
func providerAllTop(p Provider, top string) bool {
	segs := p.Segments()
	if len(segs) == 0 {
		return false
	}
	for _, s := range segs {
		if segmentTop(s) != top {
			return false
		}
	}
	return true
}

// providerTriggered 报告 Provider 至少一条声明路径出现于契约。
func providerTriggered(cfg *bootstrapv1.BootstrapConfig, p Provider) bool {
	for _, s := range p.Segments() {
		if segmentPresent(cfg, s) {
			return true
		}
	}
	return false
}

func containsString(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}

// ownsPath 报告活跃集合中是否有 Provider 最终拥有 path。
func (p *providerPlan) ownsPath(path string) bool { return p.byPath[path] > 0 }

// triggeredPaths 收集「活跃 Provider 声明路径中、实际出现于契约」的路径集合
// （供 L3 覆盖检查静态判定，不依赖构建结果）。
func (p *providerPlan) triggeredPaths(cfg *bootstrapv1.BootstrapConfig) map[string]bool {
	out := map[string]bool{}
	for _, prov := range p.active {
		for _, s := range prov.Segments() {
			if segmentPresent(cfg, s) {
				out[s] = true
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 构建循环
// ---------------------------------------------------------------------------

// buildResult 是单次 Build 的落账（cleanup 槽位由调用方持有）。
type buildResult struct {
	prov    Provider
	node    Node
	product any
	cleanup Cleanup
}

// runProviderNodes 按计划序构建节点（段存在性 gate 已在计划期完成）。sink
// 非 nil 时**逐项即时落账**（构建一个、入库一个）：阶段 B 的后段 Provider
// 依赖前段产物（audit 经 App.Database 取连接），延迟入库会破坏该依赖。落账
// 项的 cleanup 已入引擎槽位，构建失败由 Run 失败路径的 UndoEffects 回放，
// 不在此重复回滚；sink 为 nil（构造期，账本未建立）时失败逆序回滚已建项。
func runProviderNodes(ctx context.Context, entries []providerEntry, s ApplyScope, sink func(buildResult) error) ([]buildResult, error) {
	var built []buildResult
	for _, e := range entries {
		s.Node = e.node
		product, cleanup, err := e.prov.Build(ctx, s, e.node)
		if err != nil {
			if sink == nil {
				rollbackBuilds(built, ctx)
			}
			return nil, fmt.Errorf("appkit: provider %v: %w", e.node.Segments, err)
		}
		r := buildResult{prov: e.prov, node: e.node, product: product, cleanup: cleanup}
		if sink == nil {
			built = append(built, r)
			continue
		}
		if err := sink(r); err != nil {
			if cleanup != nil {
				_ = cleanup(ctx) // 本项未入账，就地释放（前序项由账本回放）
			}
			return nil, err
		}
	}
	return built, nil
}

// rollbackBuilds 逆序兑现已成功项的 cleanup（跨 Provider 构建失败回滚）。
func rollbackBuilds(built []buildResult, ctx context.Context) {
	for i := len(built) - 1; i >= 0; i-- {
		if c := built[i].cleanup; c != nil {
			_ = c(ctx)
		}
	}
}
