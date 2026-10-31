package appkit

import (
	"context"
	"fmt"
	"sort"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

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

	// 构建期变体（Q7）：命中变体的声明增量并入 Decl/契约——组件、路由、契约
	// 补丁都变成普通装配，计划随后如实投影；未命中的声明只进 Variants 段。
	vp, err := applyVariantEffects(d, cfg)
	if err != nil {
		return nil, err
	}

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
	return planFrom(ctx, d, cfg, settings, pp, vp)
}

// planFrom 把已解析的 Provider 计划投影为可检视的 Plan（纯函数）。编译预检与
// 配置变更重算共用本函数：同一份 Decl/契约必得同一计划（I2）。settings 是
// 合并后的配置快照，`biz.<path>` 段的期望态摘要取自它。
func planFrom(ctx context.Context, d *Decl, cfg *bootstrapv1.BootstrapConfig, settings map[string]any, pp *providerPlan, vp *variantPlan) (*Plan, error) {
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
	// custom-routes 警告，不假装计划能看见它。命中变体声明的路由并入同一
	// 冲突判据（同 (方法, 路径) / gin 通配形态）——比 gin 启动期 panic 早一步。
	routes, routeWarnings, err := collectDomainRoutes(d)
	if err != nil {
		return nil, err
	}
	if vp != nil && len(vp.routes) > 0 {
		routes, err = mergeRoutes(routes, vp.routes)
		if err != nil {
			return nil, err
		}
	}
	p.Routes = routes
	p.Warnings = append(p.Warnings, routeWarnings...)

	// 构建期变体声明（Q7）：声明恒进计划（可审计，含未命中的增量声明）；
	// 命中的增量已由 applyVariantEffects 并入 Decl/契约，这里只投影声明面。
	if vp != nil {
		p.Variants = vp.variants
		p.Warnings = append(p.Warnings, vp.warnings...)
	}

	// 进程内组件：BeforeStart 链之后、servers Start 之前顺序启动（component.go）。
	// 变体声明的组件带来源（Origin=variant + 变体名），不再是 ambient 旁白。
	for _, c := range d.Components {
		n := Node{
			Key:      ResourceKey("component." + c.Name()),
			Kind:     "component",
			Phase:    PhaseServe,
			Priority: 20, // server 构造（late Provider）之后、servers Start 之前
			Reload:   ReloadReconcile,
			Opaque:   true,
			Origin:   OriginApp,
		}
		if vp != nil {
			if variantName, ok := vp.components[c.Name()]; ok {
				n.Origin = OriginVariant
				n.Variant = variantName
				for _, v := range vp.variants {
					if v.Name != variantName {
						continue
					}
					for _, cdecl := range v.Effects.Components {
						if cdecl.Name == c.Name() && cdecl.Config != "" {
							n.Segments = []string{cdecl.Config}
						}
					}
				}
			}
		}
		p.Nodes = append(p.Nodes, n)
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
	// 环境注入（build tag / init() 接线，WithAmbient 显式声明）。已声明的
	// 构建期变体不进 ambient（I11：它们的增量在节点/路由上带来源），ambient
	// 只留给第三方 init() 这类无法声明化的接线。
	p.Warnings = append(p.Warnings, pp.warnings...)
	p.Warnings = append(p.Warnings, overrideWarnings(d)...)
	p.Warnings = append(p.Warnings, whitelistWarnings(pp)...)
	p.Warnings = append(p.Warnings, opaqueBudgetWarnings(d.OpaqueBudget, p.Nodes)...)
	ambient, err := ambientWarnings(d.Ambient)
	if err != nil {
		return nil, err
	}
	if err := rejectAmbientVariants(d.Ambient, p.Variants); err != nil {
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
