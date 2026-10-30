// plan.go 实现装配计划 IR。
//
// Plan 是装配的**可检视投影**：Provider 声明、域、组件、能力接管与契约段
// 消费被编译成一组带阶段/优先级/依赖的节点，供 `--plan`、测试与未来的
// `bald doctor` 使用。M1 起节点即执行顺序的唯一来源：引擎按计划构建
// Provider，顺序来自 Node 的 Phase/Priority/DependsOn（无手工排序表）。
//
// 纯函数边界：Compile 只读契约与声明，不写文件、不改全局 logger、不构建
// Provider、不执行钩子（I1）。契约装载复用 Serve 的两遍预装载（只读文件/
// env，store 用完即关）。
package appkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

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

// Origin 标记节点来源（std 后端 / 第三方 Provider / 域 / 应用 / 迁移期 legacy）。
type Origin string

const (
	OriginStd      Origin = "std"
	OriginProvider Origin = "provider"
	OriginDomain   Origin = "domain"
	OriginApp      Origin = "app"
	OriginLegacy   Origin = "legacy"
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
	Kind      string        `json:"kind"`                // server | database | logger | domain | component | app | legacy
	Segments  []string      `json:"segments,omitempty"`  // 读取的契约段（输入，不构成所有权）；框架段路径或 biz.<path>
	DependsOn []ResourceKey `json:"dependsOn,omitempty"` // 资源键依赖（决定 Apply 拓扑序）
	// TeardownAfter 仅在「停机序 ≠ Apply 逆序」时声明（"*" = 全部其它节点）。
	TeardownAfter []ResourceKey `json:"teardownAfter,omitempty"`

	Phase    Phase        `json:"phase"`
	Priority int          `json:"priority"` // 同阶段 ready 集合的显式排序键（越小越先）
	Reload   ReloadPolicy `json:"reload"`
	Override bool         `json:"override,omitempty"` // 显式覆盖同 Key 节点
	Reason   string       `json:"reason,omitempty"`   // Override 的理由

	// Opaque 表示该节点内部是命令式闭包，计划只能看到边界。
	// v1 Provider（AdaptLegacy）、Domain Wire、Component Start 均为 true。
	Opaque bool `json:"opaque,omitempty"`

	// DesiredHash 是可协调节点（ReloadReconcile）期望态的摘要：节点身份 +
	// 声明段的契约内容。重启/静态节点为空；Opaque 只表示内部不可分析，不排除
	// 期望态摘要。
	DesiredHash string `json:"desiredHash,omitempty"`

	// EffectName 是节点停机 Effect 的名（Provider 可声明；缺省 "appkit:"+Key）。
	EffectName string `json:"effectName,omitempty"`

	Origin Origin `json:"origin"`
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

// Plan 是编译结果。Nodes 已按全局拓扑序稳定排序。
type Plan struct {
	App       AppMeta `json:"app"`
	Role      string  `json:"role,omitempty"`
	InputHash string  `json:"inputHash"` // 可检视输入的摘要（契约确定性序列化）
	Nodes     []Node  `json:"nodes"`
	// OpaqueCount 是 Opaque 节点数（逃逸舱预算的计量口径；预算见 WithOpaqueBudget）。
	OpaqueCount int          `json:"opaqueCount"`
	Segments    []SegmentUse `json:"segments"`
	Warnings    []Diagnostic `json:"warnings,omitempty"`
}

// Compile 编译装配计划：两遍预装载契约（appkit.ConfigFile 指定的文件）→
// 纯计划推导。无副作用（I1）。
//
// 公开入口供测试、CLI 与未来的 bald doctor 使用；不读取 os.Args（--config/
// --plan 的 flag 解析归 Serve）。Serve 的 --plan 分支复用同一条 compileDecl
// 路径，保证预检与真实启动对同一份声明/契约。
func Compile(ctx context.Context, opts ...Option) (*Plan, error) {
	return compileDecl(ctx, newDecl(opts...))
}

// compileDecl 装载契约并编译计划（Serve 与 Compile 共用的唯一计划入口）。
func compileDecl(ctx context.Context, d *Decl) (*Plan, error) {
	cfg := bconf.NewBootstrap()
	ns, settings, err := preloadContract(cfg, d.Name, d.ConfigFile)
	if err != nil {
		return nil, err
	}
	d.configNamespace = ns
	return compilePlan(ctx, d, cfg, settings)
}

// compilePlan 从已定形的声明 + 契约推导计划。除标注的纯校验外不产生副作用。
func compilePlan(ctx context.Context, d *Decl, cfg *bootstrapv1.BootstrapConfig, settings map[string]any) (*Plan, error) {
	if cfg == nil {
		return nil, fmt.Errorf("appkit: compile: bootstrap config is nil")
	}
	// 与 assemble 同源的装配前处理（纯拷贝，不改调用方 Decl）：域 Services 并入
	// gRPC 注册回调——否则「只有域 Services、没有 WithGRPC」的配置会被 L3 覆盖
	// 检查误判为 server 段无消费者（真实启动在 compose 之后判定为已消费）。
	compiled := *d
	composeDomainServices(&compiled)
	d = &compiled

	// 与 assemble 同源的构造期校验：计划预检不得放过真实启动会报的错误。
	// Plan 在此被调用（纯函数，I1）；节点即执行顺序的唯一来源。
	pp, err := newProviderPlan(ctx, d, cfg, settings)
	if err != nil {
		return nil, err
	}
	if err := validateDomains(d, cfg.GetServer().GetGrpc() != nil); err != nil {
		return nil, err
	}
	if err := validateCapabilities(d, cfg); err != nil {
		return nil, err
	}
	if err := checkSegmentCoverage(cfg, d, pp); err != nil {
		return nil, err
	}
	return planFrom(ctx, d, cfg, settings, pp)
}

// planFrom 把已解析的 Provider 计划投影为可检视的 Plan（纯函数）。编译预检与
// 配置变更重算共用本函数：同一份 Decl/契约必得同一计划（I2）。settings 是
// 合并后的配置快照，`biz.<path>` 段的期望态摘要取自它。
func planFrom(ctx context.Context, d *Decl, cfg *bootstrapv1.BootstrapConfig, settings map[string]any, pp *providerPlan) (*Plan, error) {
	p := &Plan{
		App:       appMetaFrom(cfg),
		Role:      d.Role,
		InputHash: inputHash(cfg),
	}

	// app 节点：框架无条件消费 app 段（元数据进 kitOpts），没有 Provider。
	p.Nodes = append(p.Nodes, Node{
		Key:      "app",
		Kind:     "app",
		Segments: []string{"app"},
		Phase:    PhaseConfigure,
		Priority: -1,
		Reload:   ReloadStatic,
		Origin:   OriginApp,
	})

	// Provider 节点（路径末者胜 + 段 gate + Plan 节点冲突裁决 + 统一排序），
	// 以及恒存在的 logger 节点（logger 走两阶段 + 热更专用通道，不在构建循环）。
	for _, e := range pp.entries {
		p.Nodes = append(p.Nodes, e.node)
	}
	p.Nodes = append(p.Nodes, pp.loggerNode)

	// 程序化 Registrar 接管：合成 registry 节点（Override + 理由），并给出
	// 「段内后端选择不生效」的诊断（与执行路径跳过纯 registry Provider 同源）。
	if d.Registrar != nil {
		p.Nodes = append(p.Nodes, Node{
			Key:      "registry",
			Kind:     "registry",
			Segments: []string{"registry"},
			Phase:    PhaseBootstrap,
			Priority: PriorityRegistry,
			Reload:   ReloadStatic,
			Override: true,
			Reason:   "programmatic takeover",
			Opaque:   true,
			Origin:   OriginApp,
		})
		if segmentPresent(cfg, "registry") {
			p.Warnings = append(p.Warnings, Diagnostic{
				Level:   DiagWarn,
				Code:    "programmatic-takeover",
				Subject: "registry",
				Message: "registry segment is taken over by a programmatic Registrar instance; registry backend selection in the contract does not take effect",
			})
		}
	}

	// 业务域：声明了 Declare 时以其返回节点为拓扑（非 Opaque），兼容槽位塌缩为
	// 一个 Opaque 的 domain.<name>.wire 节点；未声明 Declare 的域保持逐槽位
	// Opaque 投影（迁移期双轨，见 domain.go）。
	for _, dom := range d.Domains {
		if dom.Declare != nil {
			nodes, err := declaredDomainNodes(ctx, dom, PlanScope{Config: cfg, Decl: d, Role: d.Role, Settings: settings})
			if err != nil {
				return nil, err
			}
			if pp.domainNodes == nil {
				pp.domainNodes = map[ResourceKey]Domain{}
			}
			for _, n := range nodes {
				pp.domainNodes[n.Key] = dom
			}
			p.Nodes = append(p.Nodes, nodes...)
			if w, ok := domainCompatNode(dom, d); ok {
				p.Nodes = append(p.Nodes, w)
			}
			continue
		}
		wireKey := ResourceKey("domain." + dom.Name)
		p.Nodes = append(p.Nodes, Node{
			Key:    wireKey,
			Kind:   "domain",
			Phase:  PhaseWire,
			Reload: ReloadStatic,
			Opaque: true,
			Origin: OriginDomain,
		})
		if dom.Routes != nil && d.HTTPHandler != nil {
			// gateway 面占用 server.http 时 Routes 让位（wireDomains 的条件同源）。
			p.Nodes = append(p.Nodes, Node{
				Key:       ResourceKey("domain." + dom.Name + ".routes"),
				Kind:      "domain",
				Segments:  []string{"server.http"},
				DependsOn: []ResourceKey{wireKey},
				Phase:     PhaseWire,
				Priority:  10,
				Reload:    ReloadStatic,
				Opaque:    true,
				Origin:    OriginDomain,
			})
		}
		if dom.Services != nil {
			p.Nodes = append(p.Nodes, Node{
				Key:       ResourceKey("domain." + dom.Name + ".services"),
				Kind:      "domain",
				Segments:  []string{"server.grpc"},
				DependsOn: []ResourceKey{wireKey},
				Phase:     PhaseServe,
				Priority:  5,
				Reload:    ReloadStatic,
				Opaque:    true,
				Origin:    OriginDomain,
			})
		}
	}

	// 进程内组件：BeforeStart 链之后、servers Start 之前顺序启动（component.go）。
	for _, c := range d.Components {
		p.Nodes = append(p.Nodes, Node{
			Key:      ResourceKey("component." + c.Name()),
			Kind:     "component",
			Phase:    PhaseServe,
			Priority: 20, // server 构造（late Provider）之后、servers Start 之前
			Reload:   ReloadReconcile,
			Opaque:   true,
			Origin:   OriginApp,
		})
	}

	// 逃逸舱计量：Opaque 节点数恒进计划（预算告警的计量口径，I10）。
	for _, n := range p.Nodes {
		if n.Opaque {
			p.OpaqueCount++
		}
	}

	// 期望态摘要：可协调节点（logger/audit/组件）由契约段内容与节点身份求得，
	// 供配置变更 diff 与 Drift 判定（M3）。其余节点（重启生效/静态）不产生摘要。
	for i := range p.Nodes {
		p.Nodes[i].DesiredHash = nodeDesiredHash(cfg, settings, p.Nodes[i])
	}

	// 逃逸舱可见化：覆盖 / 白名单 / 同 Key 裁决（I4/I10 的 M1 子集）+ 预算 +
	// 环境注入（build tag / init() 接线，WithAmbient 显式声明）。
	p.Warnings = append(p.Warnings, pp.warnings...)
	p.Warnings = append(p.Warnings, overrideWarnings(d)...)
	p.Warnings = append(p.Warnings, whitelistWarnings(pp)...)
	p.Warnings = append(p.Warnings, opaqueBudgetWarnings(d.OpaqueBudget, p.Nodes)...)
	ambient, err := ambientWarnings(d.Ambient)
	if err != nil {
		return nil, err
	}
	p.Warnings = append(p.Warnings, ambient...)

	ordered, orderWarnings, err := orderNodes(p.Nodes)
	if err != nil {
		return nil, err
	}
	p.Nodes = ordered
	p.Warnings = sortAndDedupWarnings(append(p.Warnings, orderWarnings...))
	p.Segments = buildSegmentUses(cfg, d, pp, p.Nodes)
	return p, nil
}

// appMetaFrom 取与 assemble 的 kitOpts 相同的元数据缺省值。
func appMetaFrom(cfg *bootstrapv1.BootstrapConfig) AppMeta {
	a := cfg.GetApp()
	return AppMeta{
		ID:      a.GetId(),
		Name:    orDefault(a.GetName(), "bald-app"),
		Version: orDefault(a.GetVersion(), "v0.0.0"),
		Env:     a.GetEnv(),
	}
}

// nodeDesiredHash 计算可协调节点的期望态摘要：节点身份 + 各声明段的确定性
// 序列化子树。框架段取自 BootstrapConfig；`biz.<path>` 段取自合并后的
// Settings 快照（Q8——域自有配置因此可收敛）。段缺失按 "absent" 参与摘要，
// 保持与「段被删除」可区分。非 ReloadReconcile 节点无摘要（重启/静态无期望态）。
func nodeDesiredHash(cfg *bootstrapv1.BootstrapConfig, settings map[string]any, n Node) string {
	if n.Reload != ReloadReconcile {
		return ""
	}
	h := sha256.New()
	io.WriteString(h, "key="+string(n.Key)+"\nkind="+n.Kind+"\n")
	for _, seg := range sortedUnique(n.Segments) {
		io.WriteString(h, "segment="+seg+"\n")
		// 解析用完整路径（biz 是合并快照里的顶层段，与 yaml 层级一致）。
		if _, ok := bizSegmentPath(seg); ok {
			if v, ok := settingsAtPath(settings, seg); ok {
				writeSettingsSubtree(h, v)
			} else {
				io.WriteString(h, "absent")
			}
			io.WriteString(h, "\n")
			continue
		}
		if msg := segmentMessage(cfg, seg); msg != nil {
			if b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(msg.Interface()); err == nil {
				_, _ = h.Write(b)
			}
		}
		io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// segmentMessage 按点路径以 protobuf 反射取契约段消息（"server.grpc" →
// cfg.server.grpc）；路径不存在或非消息型字段返回 nil。
func segmentMessage(cfg *bootstrapv1.BootstrapConfig, path string) protoreflect.Message {
	if cfg == nil || path == "" {
		return nil
	}
	m := cfg.ProtoReflect()
	for _, name := range strings.Split(path, ".") {
		if !m.IsValid() {
			return nil
		}
		fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
		if fd == nil || fd.Kind() != protoreflect.MessageKind {
			return nil
		}
		m = m.Get(fd).Message()
	}
	if !m.IsValid() {
		return nil
	}
	return m
}

// inputHash 对契约做确定性序列化后求摘要（I2 的可检视输入基线）。
func inputHash(cfg *bootstrapv1.BootstrapConfig) string {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(cfg)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// nodeKey 由归属路径合成稳定键：单路径直接用路径（"database.sql"），多路径
// 用排序后逗号连接（std server 的 "server.grpc,server.http"）。
func nodeKey(paths []string) ResourceKey {
	switch len(paths) {
	case 0:
		return ""
	case 1:
		return ResourceKey(paths[0])
	default:
		return ResourceKey(strings.Join(paths, ","))
	}
}

// sortedUnique 返回排序去重后的段路径（计划 JSON 的稳定性来源）。
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	dup := out[:1]
	for _, s := range out[1:] {
		if s != dup[len(dup)-1] {
			dup = append(dup, s)
		}
	}
	return dup
}

// phaseFor 把 Provider 的构建时机映射为计划阶段。
func phaseFor(ph providerPhase) Phase {
	switch ph {
	case phaseConstruct:
		return PhaseConfigure
	case phaseStageB, phaseLogger:
		return PhaseBootstrap
	case phaseLate:
		return PhaseServe
	default:
		return PhaseBootstrap
	}
}

// ---------------------------------------------------------------------------
// 节点排序
// ---------------------------------------------------------------------------

// overrideWarnings 把「同一段路径被多次声明、末者胜」的隐式覆盖显式化。
// 段级覆盖在 v0.26 仅告警；节点级同 Key 冲突走 resolveNodeConflicts
// （Override + Reason 裁决），本函数让隐式段覆盖在计划里同样可见。
func overrideWarnings(d *Decl) []Diagnostic {
	owners := map[string][]int{}
	for i, p := range d.Providers {
		for _, s := range p.Segments() {
			owners[s] = append(owners[s], i)
		}
	}
	paths := make([]string, 0, len(owners))
	for path, idxs := range owners {
		if len(idxs) > 1 {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	var out []Diagnostic
	for _, path := range paths {
		idxs := owners[path]
		winner := d.Providers[idxs[len(idxs)-1]]
		for _, li := range idxs[:len(idxs)-1] {
			out = append(out, Diagnostic{
				Level:   DiagWarn,
				Code:    "override",
				Subject: path,
				Message: fmt.Sprintf("segment %q is declared by %T and overridden by %T (last declaration wins)",
					path, d.Providers[li], winner),
			})
		}
	}
	return out
}

// whitelistWarnings 把 WithUnimplementedSegments 的逃逸舱做成计划警告。
func whitelistWarnings(pp *providerPlan) []Diagnostic {
	names := make([]string, 0, len(pp.unimplemented))
	for name := range pp.unimplemented {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Diagnostic, 0, len(names))
	for _, name := range names {
		out = append(out, Diagnostic{
			Level:   DiagWarn,
			Code:    "whitelisted",
			Subject: name,
			Message: fmt.Sprintf("segment %q has no consumer and is acknowledged via WithUnimplementedSegments", name),
		})
	}
	return out
}

// opaqueBudgetWarnings 把「Opaque 节点数超预算」显式化（I10：逃逸舱不隐身）。
// budget<=0 表示未声明预算：不告警，但 Plan.OpaqueCount 仍供 CI 设门。
func opaqueBudgetWarnings(budget int, nodes []Node) []Diagnostic {
	if budget <= 0 {
		return nil
	}
	var keys []string
	for _, n := range nodes {
		if n.Opaque {
			keys = append(keys, string(n.Key))
		}
	}
	if len(keys) <= budget {
		return nil
	}
	sort.Strings(keys)
	return []Diagnostic{{
		Level:   DiagWarn,
		Code:    "opaque-budget",
		Subject: "opaque",
		Message: fmt.Sprintf("%d opaque node(s) exceed the budget %d: %s", len(keys), budget, strings.Join(keys, ", ")),
	}}
}

// sortAndDedupWarnings 让警告集合与声明序无关（I2 的哈希稳定性）。
func sortAndDedupWarnings(in []Diagnostic) []Diagnostic {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i].Code != in[j].Code {
			return in[i].Code < in[j].Code
		}
		if in[i].Subject != in[j].Subject {
			return in[i].Subject < in[j].Subject
		}
		return in[i].Message < in[j].Message
	})
	out := in[:1]
	for _, w := range in[1:] {
		last := out[len(out)-1]
		if w.Level == last.Level && w.Code == last.Code && w.Subject == last.Subject && w.Message == last.Message {
			continue
		}
		out = append(out, w)
	}
	return out
}

// resolveNodeConflicts 对同 Key 节点做 I4 裁决：恰有一个 Override（且带
// Reason）者胜出，其余丢弃并记 override 诊断；无 Override 或都声明 Override
// 即编译错误。返回存活下标（保输入序）。
func resolveNodeConflicts(nodes []Node) ([]int, []Diagnostic, error) {
	groups := map[ResourceKey][]int{}
	var order []ResourceKey
	for i, n := range nodes {
		if n.Key == "" {
			return nil, nil, fmt.Errorf("appkit: compile: node with empty key (kind %q)", n.Kind)
		}
		if _, ok := groups[n.Key]; !ok {
			order = append(order, n.Key)
		}
		groups[n.Key] = append(groups[n.Key], i)
	}
	var (
		survivors []int
		warnings  []Diagnostic
	)
	for _, key := range order {
		idxs := groups[key]
		if len(idxs) == 1 {
			survivors = append(survivors, idxs[0])
			continue
		}
		var winners []int
		for _, i := range idxs {
			if nodes[i].Override {
				winners = append(winners, i)
			}
		}
		switch len(winners) {
		case 0:
			return nil, nil, fmt.Errorf("appkit: compile: duplicate node key %q (%s); declare exactly one of them with Override and a Reason to resolve",
				key, nodeLabels(nodes, idxs))
		case 1:
			w := winners[0]
			if nodes[w].Reason == "" {
				return nil, nil, fmt.Errorf("appkit: compile: node %q overrides %d others but declares no Reason", key, len(idxs)-1)
			}
			survivors = append(survivors, w)
			for _, i := range idxs {
				if i == w {
					continue
				}
				warnings = append(warnings, Diagnostic{
					Level:   DiagWarn,
					Code:    "override",
					Subject: string(key),
					Message: fmt.Sprintf("node %q from %s is overridden by %s (reason: %s)",
						key, nodeLabel(nodes[i]), nodeLabel(nodes[w]), nodes[w].Reason),
				})
			}
		default:
			return nil, nil, fmt.Errorf("appkit: compile: node key %q is declared with Override by %d nodes; at most one override winner is allowed", key, len(winners))
		}
	}
	return survivors, warnings, nil
}

// nodeLabel 给诊断一个稳定的节点标识（origin/kind）。
func nodeLabel(n Node) string { return string(n.Origin) + "/" + n.Kind }

// nodeLabels 汇总一组节点的标识（错误信息用）。
func nodeLabels(nodes []Node, idxs []int) string {
	labels := make([]string, 0, len(idxs))
	for _, i := range idxs {
		labels = append(labels, nodeLabel(nodes[i]))
	}
	return strings.Join(labels, ", ")
}

// orderNodes 是唯一的计划排序实现：I4 冲突裁决 → 阶段序 → 阶段内 Kahn 拓扑
// → ready 集合按 (Priority, Key) 选取。绝不使用声明下标（I2）。
func orderNodes(nodes []Node) ([]Node, []Diagnostic, error) {
	survivors, warnings, err := resolveNodeConflicts(nodes)
	if err != nil {
		return nil, nil, err
	}
	surv := make([]Node, 0, len(survivors))
	for _, i := range survivors {
		surv = append(surv, nodes[i])
	}
	ordered, err := topoOrder(surv)
	if err != nil {
		return nil, warnings, err
	}
	return ordered, warnings, nil
}

// topoOrder 对键唯一的节点做阶段序 + 阶段内 Kahn 拓扑。
func topoOrder(nodes []Node) ([]Node, error) {
	byKey := make(map[ResourceKey]Node, len(nodes))
	for _, n := range nodes {
		if n.Key == "" {
			return nil, fmt.Errorf("appkit: compile: node with empty key (kind %q)", n.Kind)
		}
		if prev, ok := byKey[n.Key]; ok {
			return nil, fmt.Errorf("appkit: compile: duplicate node key %q (kind %q and %q); domain and component names must be unique",
				n.Key, prev.Kind, n.Kind)
		}
		byKey[n.Key] = n
	}
	phases := []Phase{PhaseConfigure, PhaseBootstrap, PhaseWire, PhaseServe, PhaseDrain, PhaseTeardown}
	out := make([]Node, 0, len(nodes))
	for _, ph := range phases {
		group := make([]Node, 0, len(nodes))
		for _, n := range nodes {
			if n.Phase == ph {
				group = append(group, n)
			}
		}
		if len(group) == 0 {
			continue
		}
		ordered, err := topoPhase(group, byKey, ph)
		if err != nil {
			return nil, err
		}
		out = append(out, ordered...)
	}
	return out, nil
}

// topoPhase 对单阶段节点做 Kahn 拓扑；跨阶段依赖按「已应用」处理，指向未来
// 阶段的依赖是编译错误。ready 集合的次序为 (Priority, Key)。
func topoPhase(group []Node, all map[ResourceKey]Node, ph Phase) ([]Node, error) {
	inGroup := make(map[ResourceKey]bool, len(group))
	indeg := make(map[ResourceKey]int, len(group))
	dependents := make(map[ResourceKey][]ResourceKey, len(group))
	for _, n := range group {
		inGroup[n.Key] = true
		indeg[n.Key] = 0
	}
	for _, n := range group {
		for _, dep := range n.DependsOn {
			depNode, ok := all[dep]
			if !ok {
				return nil, fmt.Errorf("appkit: compile: node %q depends on unknown node %q", n.Key, dep)
			}
			if !inGroup[dep] {
				if depNode.Phase > ph {
					return nil, fmt.Errorf("appkit: compile: node %q (phase %s) depends on later-phase node %q (phase %s)",
						n.Key, ph, dep, depNode.Phase)
				}
				continue // 更早阶段已应用
			}
			indeg[n.Key]++
			dependents[dep] = append(dependents[dep], n.Key)
		}
	}
	sort.Slice(group, func(i, j int) bool {
		if group[i].Priority != group[j].Priority {
			return group[i].Priority < group[j].Priority
		}
		return group[i].Key < group[j].Key
	})
	out := make([]Node, 0, len(group))
	done := make(map[ResourceKey]bool, len(group))
	for len(out) < len(group) {
		picked := false
		for _, n := range group {
			if done[n.Key] || indeg[n.Key] != 0 {
				continue
			}
			out = append(out, n)
			done[n.Key] = true
			for _, dk := range dependents[n.Key] {
				indeg[dk]--
			}
			picked = true
			break
		}
		if !picked {
			var stuck []string
			for _, n := range group {
				if !done[n.Key] {
					stuck = append(stuck, string(n.Key))
				}
			}
			sort.Strings(stuck)
			return nil, fmt.Errorf("appkit: compile: dependency cycle among nodes in phase %s: %s", ph, strings.Join(stuck, ", "))
		}
	}
	return out, nil
}

// buildSegmentUses 汇总每个出现段/白名单段的消费节点（I3 的检查面）。
// Framework=true 表示框架无条件消费（app/logger 默认通道、预填 server 段），
// 没有用户可见节点。
func buildSegmentUses(cfg *bootstrapv1.BootstrapConfig, d *Decl, pp *providerPlan, nodes []Node) []SegmentUse {
	exact := map[string][]ResourceKey{}
	for _, n := range nodes {
		for _, s := range n.Segments {
			exact[s] = append(exact[s], n.Key)
		}
	}
	covered := consumedSegments(cfg, d, pp)
	byPath := map[string]SegmentUse{}
	add := func(su SegmentUse) {
		if su.Consumers != nil {
			su.Consumers = sortedUniqueKeys(su.Consumers)
		}
		byPath[su.Path] = su
	}

	for _, top := range presentTopLevelSegments(cfg) {
		cons := topConsumers(exact, top)
		wl := pp.unimplemented[top]
		add(SegmentUse{
			Path:        top,
			Consumers:   cons,
			Whitelisted: wl,
			Framework:   !wl && len(cons) == 0 && covered[top],
		})
	}
	if srv := cfg.GetServer(); srv != nil {
		serverFramework := byPath["server"].Framework
		for _, child := range presentChildSegments(srv.ProtoReflect()) {
			path := "server." + child
			if _, ok := byPath[path]; ok {
				continue
			}
			cons := sortedUniqueKeys(exact[path])
			wl := pp.unimplemented[path] || pp.unimplemented["server"]
			add(SegmentUse{
				Path:        path,
				Consumers:   cons,
				Whitelisted: wl,
				Framework:   builtinServerSegments[child] && !wl && len(cons) == 0 && serverFramework,
			})
		}
	}
	// 白名单段即使未出现在契约里也要可见（逃逸舱不隐身）。
	for name := range pp.unimplemented {
		if _, ok := byPath[name]; ok {
			continue
		}
		add(SegmentUse{Path: name, Whitelisted: true})
	}

	out := make([]SegmentUse, 0, len(byPath))
	for _, su := range byPath {
		out = append(out, su)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// topConsumers 汇总顶层段及其全部子段的消费节点（读取子段即读取顶层段）。
func topConsumers(exact map[string][]ResourceKey, top string) []ResourceKey {
	var out []ResourceKey
	prefix := top + "."
	for path, keys := range exact {
		if path != top && !strings.HasPrefix(path, prefix) {
			continue
		}
		out = append(out, keys...)
	}
	return sortedUniqueKeys(out)
}

// sortedUniqueKeys 排序去重资源键。
func sortedUniqueKeys(in []ResourceKey) []ResourceKey {
	if len(in) == 0 {
		return nil
	}
	out := append([]ResourceKey(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	dup := out[:1]
	for _, k := range out[1:] {
		if k != dup[len(dup)-1] {
			dup = append(dup, k)
		}
	}
	return dup
}

// JSON 返回机读格式（--plan-format=json）。
func (p *Plan) JSON() ([]byte, error) {
	return json.MarshalIndent(p, "", "  ")
}

// Hash 对规范化 JSON（紧凑、字段序固定、切片已排序）求 sha256。
func (p *Plan) Hash() string {
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Explain 输出人读格式（--plan 默认）。
func (p *Plan) Explain(w io.Writer) {
	fmt.Fprintf(w, "app: %s %s (env=%s)\n", p.App.Name, p.App.Version, p.App.Env)
	if p.Role != "" {
		fmt.Fprintf(w, "role: %s\n", p.Role)
	}
	fmt.Fprintf(w, "input: %s\n", p.InputHash)

	fmt.Fprintln(w, "nodes:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, n := range p.Nodes {
		var flags []string
		flags = append(flags, string(n.Origin))
		if n.Opaque {
			flags = append(flags, "opaque")
		}
		if n.Override {
			flags = append(flags, "override: "+n.Reason)
		}
		if n.Reload != ReloadStatic {
			flags = append(flags, "reload="+n.Reload.String())
		}
		fmt.Fprintf(tw, "  %s\t%s\tpriority=%d\t%s\n", n.Phase, n.Key, n.Priority, strings.Join(flags, ", "))
		if len(n.Segments) > 0 {
			fmt.Fprintf(tw, "\t\t\tsegments=%s\n", strings.Join(n.Segments, ","))
		}
		if len(n.DependsOn) > 0 {
			deps := make([]string, len(n.DependsOn))
			for i, d := range n.DependsOn {
				deps[i] = string(d)
			}
			fmt.Fprintf(tw, "\t\t\tdependsOn=%s\n", strings.Join(deps, ","))
		}
	}
	tw.Flush()

	fmt.Fprintf(w, "opaque: %d node(s)\n", p.OpaqueCount)

	fmt.Fprintln(w, "segments:")
	for _, s := range p.Segments {
		var status []string
		if len(s.Consumers) > 0 {
			keys := make([]string, len(s.Consumers))
			for i, k := range s.Consumers {
				keys[i] = string(k)
			}
			status = append(status, "consumers="+strings.Join(keys, ","))
		}
		if s.Framework {
			status = append(status, "framework")
		}
		if s.Whitelisted {
			status = append(status, "whitelisted")
		}
		if len(status) == 0 {
			status = append(status, "unconsumed")
		}
		fmt.Fprintf(w, "  %-20s %s\n", s.Path, strings.Join(status, ", "))
	}

	if len(p.Warnings) > 0 {
		fmt.Fprintln(w, "warnings:")
		for _, warn := range p.Warnings {
			subject := ""
			if warn.Subject != "" {
				subject = " [" + warn.Subject + "]"
			}
			fmt.Fprintf(w, "  %s %s%s: %s\n", warn.Level, warn.Code, subject, warn.Message)
		}
	}
	fmt.Fprintf(w, "plan hash: %s\n", p.Hash())
}

// writePlan 按 --plan-format 输出计划。
func writePlan(w io.Writer, p *Plan, format string) error {
	switch strings.ToLower(format) {
	case "", "text":
		p.Explain(w)
		return nil
	case "json":
		b, err := p.JSON()
		if err != nil {
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("appkit: unknown --plan-format %q (want text or json)", format)
	}
}
