// provider.go 实现段驱动的 Provider 装配抽象。
//
// 核心抽象只有两个：
//
//	type Provider interface {
//	    Option                          // Provider 值可直接作为 Option 传入 MustServe/NewApp
//	    Segments() []string             // 声明消费的契约段路径（顶层段名或子段路径）
//	    Build(ctx, BuildScope) (any, Cleanup, error)
//	}
//	func Providers(ps ...Provider) Option
//
// 装配引擎（assemble，bootstrap.go）不再手写 12 段逐域调用，改为：
//
//  1. 解析声明：收集全部 Provider，按**精确段路径**做末者胜（后声明的 Provider
//     接管同一路径——std.Standard() 的默认后端由此可被用户 Provider 覆盖；
//     不同路径并存——server.http 与 server.asynq 可同时由不同 Provider 供给）；
//  2. 枚举契约：段存在性由 protobuf 反射给出（segmentPresent）——**段缺失 =
//     Provider 跳过（合法）**，段在而 Build 失败 = fail-fast；
//  3. 分阶段构建（providerPhase）：config 段在构造期构建（层产出要进
//     kitOpts）、server 段延后到业务 BeforeStart 钩子之后（服务器构造时序，
//     wiring_order_test 钉住）、其余段在阶段 B 框架钩子内按 canonical 序构建
//     （logger → registry → tracer → metrics → database → cache → storage →
//     audit → ai → workflow → broker；audit 在 storage 与 ai 之间的既有次序）；
//  4. 产物入库：引擎按顶层段把 Build 产物放进 App 的统一资源面
//     （storeProduct → App.Resource(segment)，B4）——注册中心实例、六域
//     客户端、审计后端与表外段产物均按契约段路径存取（map 产物按
//     "<顶层段>.<子段>" 展开），服务器列表经 storeServerProduct 并入停机序；
//  5. 停机回收：每个 Provider 的 cleanup 在构造期按 canonical 停机序预登记
//     Effect（槽位模式，Build 成功后填充）——Effect 注册序即构建序，
//     逆序回放（audit-restore 仍最后执行）；
//  6. L3 覆盖检查（segment_coverage.go）：全部构建完成后枚举「实际出现的
//     顶层段 − 已消费段 − 白名单」，非空即启动期报错。
//
// logger 与 app 两个段**不在 Provider 循环里**：它们的消费是无条件的（段
// 缺失 = 用默认后端/默认元数据，而非跳过），且 logger 承载两阶段重建与热更
// ——与「段在才构建、段缺即跳过」的 Provider 语义不同构。logger Provider
// 覆盖经专用通道（phaseLogger，见 bootstrap.go 的 loggerSource 适配）。
package appkit

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// Cleanup 是 Provider 产物的停机释放钩子（Effect 回放执行；nil 表示无资源）。
// 旧 12 段流里两种清理形状（无参 func() 与 func(context.Context) error）统一
// 为带 ctx 形态——与 Effect 账本的 undo 签名同构，回放路径零适配。
type Cleanup func(context.Context) error

// noopCleanup 把旧式无参清理闭包适配为 Cleanup（Provider 实现侧的便利件）。
func noopCleanup(cleanup func()) Cleanup {
	if cleanup == nil {
		return nil
	}
	return func(context.Context) error { cleanup(); return nil }
}

// BuildScope 是 Provider 构建期的输入（服务器构造消费 Decl 的能力声明，
// 故整份声明随 scope 传递）。
type BuildScope struct {
	// Config 装配契约。阶段 B / server 阶段为已装载定型的完整契约；config
	// 段构造期与 logger 阶段 A 可为 nil（契约尚未/无需装载）。
	Config *bootstrapv1.BootstrapConfig
	// Decl 全部装配期声明（只读约定：Provider 不得改写）。声明缺省的字段
	// （如未经 WithHealth 的 ProbeLivePath）保持零值，勿假设已回填默认。
	Decl *Decl
	// App 资源容器：阶段 B 起（含 server 阶段）非 nil，且**前序段产物已
	// 填充**（audit 后端经 App.Resource("database.sql") 取连接即依赖此
	// 序）；config 段构造期与 logger 阶段 A 为 nil（App 尚未构造）。
	App *App
}

// Provider 消费契约段并构建该段产物。
//
// Segments 声明**契约段路径**：顶层段名（"database"、"audit"）或子段路径
// （"database.sql"、"server.http"）。路径按精确匹配做末者胜：后声明的
// Provider 接管同一路径（覆盖 std 默认后端），不同路径并存（多协议/多引擎）。
//
// Build 仅在「至少一条声明路径实际出现于契约」时被调用（段缺失 = 跳过，
// 合法语义）；段内子段的存在性与分派由实现自持（可复用 bootstrap 各域
// Registry 作内部机制）。Build 失败 = fail-fast，启动中止。
//
// Provider 嵌入 Option（Apply 把自身追加进声明）——Provider 值可直接作为
// MustServe/NewApp 的变参项，与 std.Standard()、业务域 Option 并列：
//
//	appkit.MustServe(ctx, std.Standard(), storegorm.AppProvider(), ...)
type Provider interface {
	Option
	// Segments 声明本 Provider 消费的契约段路径（非空；空路径装配期报错）。
	Segments() []string
	// Build 构建段产物。返回值：(产物, 停机清理, 错误)。产物形状按顶层段
	// 约定（见 storeProduct）：registry 段为 registry.Registrar，六资源域为
	// map[string]any（键=子段名），audit 段为 audit.Auditor，server 段为
	// []transport.Server，config 段为 []config.Layer；无产物可返回 nil。
	Build(ctx context.Context, s BuildScope) (any, Cleanup, error)
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

// WithUnimplementedSegments 显式豁免「契约已声明、框架尚无消费者」的段
// （L3 覆盖检查的白名单出口）。
//
// L3 默认 fail-closed：契约里出现的顶层段若无任何 Provider 消费，启动期
// 直接报错。契约先行的工位（先写配置、后等实现）经本 Option 显式承认
// 「我知道该段暂不生效」，多次调用追加。条目为顶层段名（"script"）或
// server 子段路径（"server.asynq"）。
func WithUnimplementedSegments(names ...string) Option {
	return optionFunc(func(d *Decl) { d.UnimplementedSegments = append(d.UnimplementedSegments, names...) })
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

// stageBRank 是阶段 B 的 canonical 构建序（= 旧 12 段手工流的调用序；audit
// 依赖 database/cache 客户端就绪，排在二者之后——audit_wiring_order_test
// 钉住）。表外段（第三方）排在其后、按声明序稳定排列。
var stageBRank = map[string]int{
	"logger": 0, "registry": 1, "tracer": 2, "metrics": 3,
	"database": 4, "cache": 5, "storage": 6, "audit": 7,
	"ai": 8, "workflow": 9, "broker": 10,
}

// cleanupOrder 是停机 Effect 的 canonical **注册序**（逆序回放即停机释放序，
// 与旧 12 段流的 Effect 注册序逐条一致）：audit-restore 注册最早 → 回放最后
// （stream 尾批审计事件在指标/span flush 之后收集），logger 恢复居中。
// 表外的第三方 Provider cleanup 在本序全部登记之后追加（回放更早——业务
// 资源先收、框架底座后收）。
var cleanupOrder = []string{
	"audit", "tracer", "metrics", "logger", "registry",
	"database", "cache", "storage", "ai", "workflow", "broker",
}

// effectNameBySegment 是各 canonical 段停机 Effect 的名字（与旧 12 段流的
// Effect 名逐字一致——时序断言与停机日志依赖这些名字）。
var effectNameBySegment = map[string]string{
	"audit":    "appkit:audit-restore",
	"tracer":   "appkit:tracer-shutdown",
	"metrics":  "appkit:metrics-server",
	"logger":   "appkit:bootstrap-logger",
	"registry": "appkit:registrar-client",
	"database": "appkit:database-clients",
	"cache":    "appkit:cache-clients",
	"storage":  "appkit:storage-clients",
	"ai":       "appkit:ai-clients",
	"workflow": "appkit:workflow-clients",
	"broker":   "appkit:broker-clients",
}

// providerPlan 是装配期解析完成的 Provider 计划：路径末者胜后的活跃集合，
// 按阶段分组、阶段内按 canonical 序排列。
type providerPlan struct {
	// byPath 记录每条声明路径的最终归属（末者胜；值 = active 下标+1，0 = 无归属）。
	byPath map[string]int
	// active 是末者胜后的活跃 Provider（保声明序）。
	active []Provider
	// construct/stageB/late 是分阶段后的活跃集合（阶段内 canonical 序）。
	construct []Provider
	stageB    []Provider
	late      []Provider
	// loggerProv 是接管日志装配的 Provider（至多一个）。
	loggerProv Provider
	// unimplemented 是白名单集合（查 O(1)）。
	unimplemented map[string]bool
}

// newProviderPlan 解析 Decl.Providers：收集路径 → 末者胜 → 分阶段排序。
// 校验：空段路径报错；同一 Provider 声明跨阶段路径报错（一次 Build 无法同时
// 服务两种调用时机与两份 scope 约定）。
func newProviderPlan(d *Decl) (*providerPlan, error) {
	plan := &providerPlan{byPath: map[string]int{}, unimplemented: map[string]bool{}}
	for _, name := range d.UnimplementedSegments {
		if name != "" {
			plan.unimplemented[name] = true
		}
	}

	// 收集每条路径的末者（后声明覆盖先声明——std 默认后端可被用户覆盖）。
	lastOwner := map[string]int{} // path → provider 下标
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
	// 活跃 = 至少最终拥有一条路径的 Provider（保声明序去重；路径的最终归属
	// 即其末声明者，必在活跃集合内）。
	owned := map[int]bool{}
	for _, i := range lastOwner {
		owned[i] = true
	}
	for i, p := range d.Providers {
		if !owned[i] {
			continue
		}
		plan.active = append(plan.active, p)
		for _, s := range p.Segments() {
			plan.byPath[s] = len(plan.active) // 1-based；后写覆盖前写 = 末者胜
		}
	}

	// 分阶段 + 阶段内 canonical 稳定排序。
	byPhase := map[providerPhase][]Provider{}
	for _, p := range plan.active {
		ph := phaseByTop(segmentTop(p.Segments()[0]))
		for _, s := range p.Segments() {
			if phaseByTop(segmentTop(s)) != ph {
				return nil, fmt.Errorf("appkit: provider %v declares segments across build phases (%v) — split it into one Provider per phase", p.Segments(), p.Segments())
			}
		}
		byPhase[ph] = append(byPhase[ph], p)
	}
	plan.construct = sortByStageBRank(byPhase[phaseConstruct])
	plan.stageB = sortByStageBRank(byPhase[phaseStageB])
	plan.late = sortByStageBRank(byPhase[phaseLate])
	if logger := byPhase[phaseLogger]; len(logger) > 1 {
		return nil, fmt.Errorf("appkit: %d providers declare logger segments (%v); only one logger provider may be declared", len(logger), pathsOf(logger))
	} else if len(logger) == 1 {
		plan.loggerProv = logger[0]
	}
	return plan, nil
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

// pathsOf 汇总一组 Provider 的全部声明路径（错误信息用）。
func pathsOf(ps []Provider) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Segments()...)
	}
	return out
}

// sortByStageBRank 按 stageBRank 稳定排序（表外段保持声明序排其后；
// 集合个位数大小，插入排序足够且零依赖）。
func sortByStageBRank(ps []Provider) []Provider {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && stageBRankMin(ps[j]) < stageBRankMin(ps[j-1]); j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
	return ps
}

// stageBRankMin 取 Provider 全部声明路径的最小 canonical 序（表外段 = MaxInt）。
func stageBRankMin(p Provider) int {
	min := int(^uint(0) >> 1)
	for _, s := range p.Segments() {
		if r, ok := stageBRank[segmentTop(s)]; ok && r < min {
			min = r
		}
	}
	return min
}

// ---------------------------------------------------------------------------
// 构建循环
// ---------------------------------------------------------------------------

// buildResult 是单次 Build 的落账（cleanup 槽位由调用方持有）。
type buildResult struct {
	prov    Provider
	product any
	cleanup Cleanup
}

// runProviderLoop 按序构建触发的 Provider：presenceGated 时段缺失跳过
// （合法，fail-fast 表 L2）——config 段（构造期）为无条件构建（其源可独立
// 于契约 config 段存在，如本地文件）；段在而 Build 失败 fail-fast。
//
// sink 非 nil 时**逐项即时落账**（构建一个、入库一个）：阶段 B 的后段
// Provider 依赖前段产物（audit 经 App.Database 取连接），延迟入库会破坏该
// 依赖——旧 12 段流每个 build* 都是即建即存，本循环保持同一时序。落账项的
// cleanup 已入引擎槽位/账本，构建失败由 Run 失败路径的 UndoEffects 回放，
// 不在此重复回滚；sink 为 nil（构造期，账本未建立）时失败逆序回滚已建项。
func runProviderLoop(ctx context.Context, ps []Provider, s BuildScope, presenceGated bool, sink func(buildResult) error) ([]buildResult, map[string]bool, error) {
	var (
		built     []buildResult
		triggered = map[string]bool{}
	)
	for _, p := range ps {
		hit := !presenceGated
		for _, seg := range p.Segments() {
			if presenceGated && segmentPresent(s.Config, seg) {
				triggered[seg] = true
				hit = true
			}
		}
		if !hit {
			continue // 段缺失 → 跳过（合法）
		}
		product, cleanup, err := p.Build(ctx, s)
		if err != nil {
			if sink == nil {
				rollbackBuilds(built, ctx)
			}
			return nil, triggered, fmt.Errorf("appkit: provider %v: %w", p.Segments(), err)
		}
		r := buildResult{prov: p, product: product, cleanup: cleanup}
		if sink == nil {
			built = append(built, r)
			continue
		}
		if err := sink(r); err != nil {
			if cleanup != nil {
				_ = cleanup(ctx) // 本项未入账，就地释放（前序项由账本回放）
			}
			return nil, triggered, err
		}
	}
	return built, triggered, nil
}

// rollbackBuilds 逆序兑现已成功项的 cleanup（跨 Provider 构建失败回滚）。
func rollbackBuilds(built []buildResult, ctx context.Context) {
	for i := len(built) - 1; i >= 0; i-- {
		if c := built[i].cleanup; c != nil {
			_ = c(ctx)
		}
	}
}
