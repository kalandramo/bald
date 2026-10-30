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

	// Opaque 表示该节点内部是命令式闭包，计划只能看到边界（期望态无法由
	// 声明派生）：v1 Provider（AdaptLegacy）、Domain Wire、命令式组件、std
	// server（注册回调装进去的路由/service 不可见）均为 true；契约派生的
	// std logger/audit 节点不是 Opaque。
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

// Route 是一条域自持 REST 路由的声明（域名 + 方法 + 路径 + 可选 RPC 名）。
//
// 它是计划可检视的路由清单，不是路由的实现：域在 Domain.RouteTable 里声明
// 自己实际挂载的路由（生成器可从 proto 的 google.api.http 注解派生），
// Compile 汇总进 Plan.Routes。方法归一化为大写，路径须以 "/" 开头；
// 同一方法下路径冲突（完全重复，或 gin 无法并存的通配形态）是编译错误——
// 路由冲突在计划期暴露，而不是等 gin 在启动期 panic。
type Route struct {
	Domain string `json:"domain"`
	Method string `json:"method"`
	Path   string `json:"path"`
	RPC    string `json:"rpc,omitempty"`
}

// httpRoutingMethods 是计划期接受的路由方法（typo 在计划期报错）。
var httpRoutingMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "HEAD": true, "OPTIONS": true,
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
	// 排序）。声明恒进计划——不带 tag 的构建也能看到「声明了哪些变体」；
	// Active 表示当前二进制实际带有了该变体的全部 tag（另产 ambient 诊断）。
	Variants []VariantDecl `json:"variants,omitempty"`
	// Imperative 是命令式槽位清单（effect/keyWatch/reconcile/hook/serverFunc/
	// serverCapability/onConfigChange/identity/auditor；按 (Kind,Name) 排序，
	// 保证置换不变性）。
	Imperative []Imperative `json:"imperative,omitempty"`
	Warnings   []Diagnostic `json:"warnings,omitempty"`
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

	// REST 路由清单：域声明的 RouteTable 汇总进计划（Q5 的可见性面——路由
	// 从闭包里的副作用变成可检视数据）；手写 Routes 闭包没有清单时给
	// custom-routes 警告，不假装计划能看见它。同 (方法, 路径) 跨域冲突在此
	// 报错——比 gin 启动期 panic 早一步。
	routes, routeWarnings, err := collectDomainRoutes(d)
	if err != nil {
		return nil, err
	}
	p.Routes = routes
	p.Warnings = append(p.Warnings, routeWarnings...)

	// 构建期变体声明（Q7）：声明恒进计划（可审计）；命中的变体在下面并入
	// ambient 诊断（与 WithAmbient 共用一套重名裁决）。
	variants, err := variantProjection(d.Variants)
	if err != nil {
		return nil, err
	}
	p.Variants = variants

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

	// 命令式槽位可见化（effect/keyWatch/reconcile/hook）：闭包不进节点拓扑，
	// 但计划必须如实列出声明——配置热更订阅者、停机效果与生命周期钩子不因
	//「不可分析」而消失（P1：BeforeStart 等函数字段让「会起什么」不可回答）。
	// 按 (Kind, Name) 排序，保持置换不变性。
	for _, e := range d.Effects {
		p.Imperative = append(p.Imperative, Imperative{Kind: "effect", Name: e.Name})
	}
	for _, k := range d.KeyWatches {
		p.Imperative = append(p.Imperative, Imperative{Kind: "keyWatch", Name: k.Key})
	}
	for _, r := range d.Reconciles {
		p.Imperative = append(p.Imperative, Imperative{Kind: "reconcile", Name: r.Name})
	}
	// 钩子按声明序编号：钩子顺序本身是执行语义（前后钩子逆序回放），索引
	// 是它在计划里的稳定身份。
	for _, h := range []struct {
		name string
		fns  []func(context.Context) error
	}{
		{"beforeStart", d.BeforeStart},
		{"afterStart", d.AfterStart},
		{"beforeStop", d.BeforeStop},
		{"afterStop", d.AfterStop},
	} {
		for i := range h.fns {
			p.Imperative = append(p.Imperative, Imperative{
				Kind: "hook", Name: fmt.Sprintf("%s[%d]", h.name, i),
			})
		}
	}
	// 运行期服务器工厂与配置热更回调同样是闭包：工厂返回什么、回调改什么
	// 计划分析不了，但「声明了几个」必须可见（否则 --plan 漏报装配面）。
	for i := range d.ServersFunc {
		p.Imperative = append(p.Imperative, Imperative{
			Kind: "serverFunc", Name: fmt.Sprintf("extraServer[%d]", i),
		})
	}
	// 服务器能力声明（WithHTTP/WithGatewayRegister/WithGRPC/WithGRPCOptions）：
	// 注册回调装进去的路由/service 计划看不见，但「声明了哪一面」必须可见——
	// 否则纯 gRPC 与纯 HTTP 应用的 --plan 完全相同。
	for _, c := range []struct {
		name    string
		present bool
	}{
		{"gateway", d.GatewayRegister != nil},
		{"grpc", d.GRPCRegister != nil},
		{"grpcOptions", d.GRPCOptionsFn != nil},
		{"http", d.HTTPHandler != nil},
	} {
		if c.present {
			p.Imperative = append(p.Imperative, Imperative{Kind: "serverCapability", Name: c.name})
		}
	}
	if d.OnConfigChange != nil {
		p.Imperative = append(p.Imperative, Imperative{Kind: "onConfigChange"})
	}
	// 身份端口与审计 sink 是行为值（函数/接口）：计划分析不了它们做什么，
	// 但换掉它们会改变审计事件的 subject/tenant 与落点——必须能在计划
	// 与跨版本 diff 里看见。
	if d.Identity != nil {
		p.Imperative = append(p.Imperative, Imperative{Kind: "identity"})
	}
	if d.Auditor != nil {
		p.Imperative = append(p.Imperative, Imperative{Kind: "auditor"})
	}
	sort.Slice(p.Imperative, func(i, j int) bool {
		if p.Imperative[i].Kind != p.Imperative[j].Kind {
			return p.Imperative[i].Kind < p.Imperative[j].Kind
		}
		return p.Imperative[i].Name < p.Imperative[j].Name
	})

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
	// 环境注入（build tag / init() 接线，WithAmbient 显式声明；命中的构建期
	// 变体并入同一身份空间——同 Subject 同说明合并，异说明报错）。
	p.Warnings = append(p.Warnings, pp.warnings...)
	p.Warnings = append(p.Warnings, overrideWarnings(d)...)
	p.Warnings = append(p.Warnings, whitelistWarnings(pp)...)
	p.Warnings = append(p.Warnings, opaqueBudgetWarnings(d.OpaqueBudget, p.Nodes)...)
	ambientNotes := append(append([]Ambient(nil), d.Ambient...), variantAmbients(p.Variants)...)
	ambient, err := ambientWarnings(ambientNotes)
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

// collectDomainRoutes 汇总域的 RouteTable 为计划路由清单：
//   - 方法归一化为大写、路径原样（校验在 validateDomains：方法白名单、以 "/"
//     开头、非空）；
//   - 同一 (方法, 路径) 被两个域声明即冲突——两个域都想挂同一路由，运行期
//     必然踩踏（gin panic 或静默覆盖），在计划期报错并点名双方；
//   - 声称能挂、gin 却挂不上去的形态也在计划期报错：同一方法下通配段重名
//     不一致（/users/:id 与 /users/:name）会 panic，catch-all 与任何更长路径
//     也无法并存——这类冲突不靠「路径字符串完全相同」就能发现，按段比对；
//   - 声明了 Routes 闭包却没有 RouteTable 的域给 custom-routes 警告：
//     手写路由计划看不见，这是诚实缺省，不是错误（Q5 的 --no-routes/--no-bind 逃逸）。
func collectDomainRoutes(d *Decl) ([]Route, []Diagnostic, error) {
	var (
		out   []Route
		warns []Diagnostic
	)
	owner := map[string]Route{} // "METHOD path" -> 已登记的 Route
	for _, dom := range d.Domains {
		if len(dom.RouteTable) == 0 {
			if dom.Routes != nil {
				warns = append(warns, Diagnostic{
					Level:   DiagWarn,
					Code:    "custom-routes",
					Subject: dom.Name,
					Message: "domain registers REST routes through a closure with no RouteTable; the plan cannot list them",
				})
			}
			continue
		}
		for _, r := range dom.RouteTable {
			method := strings.ToUpper(strings.TrimSpace(r.Method))
			path := strings.TrimSpace(r.Path)
			if err := validateRouteShape(dom.Name, method, path); err != nil {
				return nil, nil, err
			}
			key := method + " " + path
			if prev, ok := owner[key]; ok {
				if prev.Domain == dom.Name {
					// 同一域重复登记常见于「生成器派生了清单、域里还留着
					// 手写 RouteTable」——运行期同样会注册两遍，点名处置方式。
					return nil, nil, fmt.Errorf("appkit: REST route %s %s is declared twice by domain %q (drop the hand-written RouteTable or the generated one)", method, path, dom.Name)
				}
				return nil, nil, fmt.Errorf("appkit: REST route %s %s is declared by both domain %q and %q", method, path, prev.Domain, dom.Name)
			}
			for _, prev := range out {
				if prev.Method != method {
					continue // 方法各自一棵 gin 路由树，互不干扰
				}
				reason := routeShapeConflict(prev.Path, path)
				if reason == "" {
					continue
				}
				if prev.Domain == dom.Name {
					return nil, nil, fmt.Errorf("appkit: domain %q declares conflicting REST routes %s %s and %s %s: %s",
						dom.Name, method, prev.Path, method, path, reason)
				}
				return nil, nil, fmt.Errorf("appkit: REST routes %s %s (domain %q) and %s %s (domain %q) collide in the gin route tree: %s",
					method, prev.Path, prev.Domain, method, path, dom.Name, reason)
			}
			now := Route{Domain: dom.Name, Method: method, Path: path, RPC: r.RPC}
			owner[key] = now
			out = append(out, now)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Method != out[j].Method {
			return out[i].Method < out[j].Method
		}
		return out[i].RPC < out[j].RPC
	})
	return out, warns, nil
}

// routeSeg 是路由路径的一个段：静态字面量、参数（:name / {name}）或
// catch-all（*name）。{name} 是 proto 注解形态，生成器已转成 :name，这里
// 一并识别，避免同一路由的两种写法绕过冲突检查。
type routeSeg struct {
	kind byte   // 's' static, 'p' param, 'a' catch-all
	name string // 通配名（静态段为空）
	text string // 原始段文本，用于报错
}

func splitRoutePath(path string) []routeSeg {
	raw := strings.Split(strings.TrimPrefix(path, "/"), "/")
	segs := make([]routeSeg, len(raw))
	for i, s := range raw {
		switch {
		case strings.HasPrefix(s, "*"):
			segs[i] = routeSeg{kind: 'a', name: s[1:], text: s}
		case strings.HasPrefix(s, ":"):
			segs[i] = routeSeg{kind: 'p', name: s[1:], text: s}
		case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"):
			name := strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}")
			name, _, _ = strings.Cut(name, "=")
			segs[i] = routeSeg{kind: 'p', name: name, text: s}
		default:
			segs[i] = routeSeg{kind: 's', text: s}
		}
	}
	return segs
}

// routeShapeConflict 按 gin 的注册语义比两条同方法路径，返回冲突原因
// （空串表示可以并存）：
//   - 静态兄弟与通配兄弟可以并存（gin 回溯匹配，静态优先），不同静态段
//     一旦分叉就再无交集；
//   - 同一位置两个参数必须同名——gin 的树按通配文本共享节点，:id 与
//     :name 会在注册时 panic；
//   - catch-all 吞掉其后所有段，与任何同前缀的更具体路径都不能并存，
//     无论注册先后。
//
// 只识别整段通配（:name / {name} / *name），段内混排通配不在检查范围。
func routeShapeConflict(a, b string) string {
	sa, sb := splitRoutePath(a), splitRoutePath(b)
	for i := 0; i < len(sa) && i < len(sb); i++ {
		x, y := sa[i], sb[i]
		switch {
		case x.kind == 'a' || y.kind == 'a':
			return fmt.Sprintf("segment %d has catch-all %q against %q, and gin cannot register a catch-all beside any other route under the same prefix", i+1, x.text, y.text)
		case x.kind == 's' && y.kind == 's':
			if x.text != y.text {
				return ""
			}
		case x.kind == 's' || y.kind == 's':
			return ""
		default:
			if x.name != y.name {
				return fmt.Sprintf("segment %d uses wildcards %q and %q, and gin requires one wildcard name per position", i+1, x.text, y.text)
			}
		}
	}
	if len(sa) < len(sb) && sa[len(sa)-1].kind == 'a' {
		return fmt.Sprintf("catch-all %q leaves no room for the longer path %q", sa[len(sa)-1].text, b)
	}
	if len(sb) < len(sa) && sb[len(sb)-1].kind == 'a' {
		return fmt.Sprintf("catch-all %q leaves no room for the longer path %q", sb[len(sb)-1].text, a)
	}
	return ""
}

// validateRouteShape 把 gin 注册期才会 panic 的路径形态提前到计划期：
// catch-all 必须在最后一段、通配符必须有名字、每段至多一个通配符、'*' 只能
// 出现在段首。{name=...} 的 proto 形态整段视为参数，不按 gin 语法拆。
func validateRouteShape(domain, method, path string) error {
	segs := splitRoutePath(path)
	for i, s := range segs {
		switch s.kind {
		case 'a':
			if s.name == "" {
				return fmt.Errorf("appkit: domain %q route %s %s: catch-all %q must be named", domain, method, path, s.text)
			}
			if i != len(segs)-1 {
				return fmt.Errorf("appkit: domain %q route %s %s: catch-all %q must be the last path segment", domain, method, path, s.text)
			}
		case 'p':
			if s.name == "" {
				return fmt.Errorf("appkit: domain %q route %s %s: wildcard %q must be named", domain, method, path, s.text)
			}
		}
		if strings.HasPrefix(s.text, "{") {
			continue
		}
		if n := strings.Count(s.text, ":") + strings.Count(s.text, "*"); n > 1 {
			return fmt.Errorf("appkit: domain %q route %s %s: segment %q has %d wildcards; gin allows at most one per segment", domain, method, path, s.text, n)
		}
		if k := strings.IndexByte(s.text, '*'); k > 0 {
			return fmt.Errorf("appkit: domain %q route %s %s: catch-all %q must start a path segment", domain, method, path, s.text)
		}
	}
	return nil
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
	// 业务配置段（biz.<path>）：不在契约段集合里，但节点确实读取——同样进
	// 汇总（有消费者），否则 segments 视图对 Q8 节点说谎。
	for path, keys := range exact {
		if path != "biz" && !strings.HasPrefix(path, "biz.") {
			continue
		}
		if _, ok := byPath[path]; ok {
			continue
		}
		add(SegmentUse{Path: path, Consumers: keys})
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

	if len(p.Routes) > 0 {
		fmt.Fprintln(w, "routes:")
		for _, r := range p.Routes {
			rpc := ""
			if r.RPC != "" {
				rpc = " -> " + r.RPC
			}
			fmt.Fprintf(w, "  %-12s %-7s %s%s\n", r.Domain, r.Method, r.Path, rpc)
		}
	}

	if len(p.Variants) > 0 {
		active := 0
		for _, v := range p.Variants {
			if v.Active {
				active++
			}
		}
		fmt.Fprintf(w, "variants: %d (active %d)\n", len(p.Variants), active)
		for _, v := range p.Variants {
			state := "declared"
			if v.Active {
				state = "active"
			}
			fmt.Fprintf(w, "  %-8s %-20s tags=%s\n", state, v.Name, strings.Join(v.BuildTags, ","))
		}
	}

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

	if len(p.Imperative) > 0 {
		fmt.Fprintln(w, "imperative:")
		for _, it := range p.Imperative {
			name := it.Name
			if name == "" {
				name = "-"
			}
			fmt.Fprintf(w, "  %-12s %s\n", it.Kind, name)
		}
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
