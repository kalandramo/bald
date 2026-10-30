// domain.go 实现业务域一等概念 appkit.Domain。
//
// 域是「每域包导出一个声明值」的一等 Option：Domain 实现 Option（Apply 追加进
// Decl.Domains——Provider 同款手法），变参直传 MustServe：
//
//	appkit.MustServe(ctx, std.Standard(), authv1.Domain, dictv1.Domain)
//
// 接缝约定：appkit 只接受「域声明值」，不新增注册
// 表式 API；域清单的承载者是生成文件（Go 无运行期包发现，bald gen
// domain 产出 domains.gen.go 聚合清单；_example 的手写 domains.gen.go 即其
// 目标形态）。init() 自注册禁止（「不做 init() 自注册」哲学）。
//
// 槽位（以 bald-admin 20 域的真实诉求裁剪，YAGNI）：
//   - ApplyNode：声明节点的执行点——引擎在域窗口内按计划序逐条调用，声明
//     从「拓扑」变成「可执行资源」（目标形态，设计 §4.6）；与兼容槽位互斥；
//   - Wire：域在装配期自我接线——读自己的配置段（app.Settings() 快照）、构造
//     自己的能力（限流/熔断/重试等）、持有自己的端口（业务能力策略住域：
//     读业务阈值的构造器归域包，装配层不再出现业务阈值）；
//   - Routes：HTTP 路由注册——接收 WithHTTP 声明的业务 handler（gin 场景即
//     *gin.Engine；appkit 不 import 路由器，路由器耦合由域自持），在 server
//     构建前生效；gateway 转码面占用 server.http 端口时让位（同端口单面语义）；
//   - Services：gRPC service 注册——并入 server 构建的注册回调（业务注册在
//     前、域按声明序在后），实现端口由 Wire/ApplyNode 构造；
//   - Reconcile：Declare 声明的 ReloadReconcile 节点的协调钩子——配置热更
//     时重建域自有资源（设计 §4.6 的目标形态补全：声明节点不再只是拓扑）。
//
// 裁剪说明（为何没有迁移/ConfigKeys 槽位）：
//   - 迁移：执行迁移需要存储栈类型（gorm AutoMigrate），appkit 不 import 存储
//     栈——域在 Wire 内经类型化资源句柄取连接自行执行（如
//     storegorm.SQL.Resource(app)；bald-admin audit_wiring.go 的 AutoMigrate
//     即既有先例）；声明式迁移清单由 gen domain 生成物承载。
//   - ConfigKeys：业务配置段只进 Settings 快照、不进契约（bald-admin 的 biz
//     段先例——业务键被契约 Unmarshal 丢弃，与 L3 段覆盖检查无交互）；
//     StrictContract 白名单由应用声明（appkit.StrictContract）。
//
// Wire/ApplyNode 执行时点（域 BeforeStart 钩子；Wire 按注册序执行，ApplyNode
// 按计划序执行声明节点，wiring_order_test 族钉住）：
//
//	框架钩子（契约装载 + 阶段 B Provider 循环 + L3）→ 业务 BeforeStart → 域 →
//	迟后阶段（server Provider）→ 额外服务器工厂
//
// 该窗口由两约束夹出：域需要资源面已就绪（阶段 B 产物经 Key[T].Resource 可取）；
// 域的路由/服务注册须在 server 构建前生效。放在业务钩子之后，域还能消费业务
// 钩子建立的运行期资源（bald-admin 的 authenticator/仓储均建于 BeforeStart）。
// 多域次序 = 声明序（Decl.Domains 的 Apply 追加序 = MustServe 变参序）；任一
// 域 Wire/Routes/ApplyNode 失败 fail-fast（错误带域名，经 Run 的 beforeStart
// 失败路径回滚账本）。NewApp 不消费 Domains（非第二条装配路径，见 NewApp
// 注释）。
package appkit

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/grpc"
)

// Domain 是业务域的装配期声明值（每域包导出一个；变参直传 MustServe）。
//
// 三个钩子全部可选（nil = 该域不声明该面）；Name 必填且全应用唯一（装配期
// fail-fast，错误信息按域名定位）。
type Domain struct {
	// Name 域名：fail-fast 错误定位与生成物清单（domains.gen.go）对账的标识。
	Name string

	// Declare 是域的计划期声明（纯函数，不构造实例）：返回的节点进入装配
	// 计划（非 Opaque），声明域自有资源、依赖与重载策略（设计 §4.6）。
	// 节点 Phase 必须落在 wire 或其后（configure/bootstrap 归框架 Provider）；
	// Key 必填且全应用唯一，Kind 缺省 domain，Reload 缺省 static。Segments
	// 须是 BootstrapConfig 的真实字段路径，或业务配置路径 biz.<path>（拼错
	// 在计划期报错）；声明 ReloadReconcile 的节点须提供 Domain.Reconcile 且
	// 至少声明一个段（期望态摘要取自段内容：框架段取 proto 序列化，biz 段取
	// Settings 快照的子树 JSON）。
	//
	// 迁移期语义：Wire/Routes/Services 仍是实际接线点；声明了 Declare 时
	// 引擎不再逐个合成它们的 Opaque 节点，而是塌缩成一个
	// Key("domain.<name>.wire") 的 Opaque 节点——构造仍不透明，拓扑声明已
	// 可检视。省略 Declare 的域保持既有逐槽位投影（domain.go 头注释）。
	Declare func(ctx context.Context, s PlanScope) ([]Node, error)

	// Wire 域自我接线（装配期执行一次）：读自己的配置段（app.Settings()）、
	// 构造自己的能力、持有自己的端口。执行时资源面已就绪
	// （Key[T].Resource 可取阶段 B 产物），业务 BeforeStart 钩子已跑完（域可
	// 消费业务建立的运行期资源）。返回错误 = 启动失败（fail-fast）。
	Wire func(ctx context.Context, app *App) error

	// Routes HTTP 路由注册：参数是 WithHTTP 声明的业务 handler（gin 场景即
	// *gin.Engine，域自行断言路由器类型——appkit 不 import 路由器）。在 Wire
	// 之后、server 构建之前调用，注册的路由随该 handler 一起挂载。gateway
	// 转码面占用 server.http 端口时整体让位（不调用——同端口单面语义，与
	// WithHTTP/WithGatewayRegister 的既有约定一致）。
	Routes func(h http.Handler) error

	// Services gRPC service 注册：并入 server 构建的注册回调（业务注册在前、
	// 域按声明序在后；调用点在迟后阶段 server 构建——彼时 Wire 已完成，域
	// 端口就绪）。appkit.WithGRPC/WithGRPCOptions 声明的拦截器链照常生效。
	//
	// 与 ApplyNode 共存合法：Services 是延迟注册回调（不在域窗口执行），不
	// 存在「接线跑两遍」；此时域不得再为服务注册声明 server.grpc 节点（该
	// 节点的执行点在 server 构建时，ApplyNode 的窗口里没有 grpc.Server——
	// 声明了只会得到计划与执行的双真相源，计划期 fail-fast）。
	Services func(s *grpc.Server)

	// ApplyNode 是声明节点的执行点（目标形态）：引擎在域窗口内按**计划序**
	//（Phase → Priority → Key，含跨域依赖）逐条调用 Declare 返回的节点，node
	// 是该节点的计划投影（Key/Kind/Segments/Reload 可读）。声明因此从「拓扑
	// 声明」变成「可执行资源」，域不再产生 Opaque 兼容节点。
	//
	// 与兼容槽位互斥：同时声明 Wire/Routes 会让同一接线在域窗口执行两次，
	// 装配期 fail-fast（Services 是延迟注册回调，见字段注释，可与 ApplyNode
	// 共存）。执行时点与 Wire 相同（业务 BeforeStart 之后、server 构建之前）
	// ——路由仍先于 server 构建；失败 fail-fast（错误带域名与节点 Key），
	// 已执行节点无回滚（与 Wire 失败同语义：进程终止）。
	//
	// 声明节点应覆盖 ApplyNode 消费的配置段（Segments）——L3 覆盖检查据此
	// 判定段已消费，漏声明会让启动按未消费段失败。
	ApplyNode func(ctx context.Context, app *App, node Node) error

	// Reconcile 是域内可协调节点的协调钩子：Declare 返回 ReloadReconcile
	// 节点的域必须提供它（否则装配期 fail-fast——引擎无法重建域自有状态，
	// 静默留下永远 Stale 的节点只会让 Drift 失真）。配置热更时引擎按节点
	// Key 调用，node 是新计划的期望节点；app 提供资源面与新配置快照
	// （app.Settings()）。
	//
	// 与 appkit.NodeReconciler 同一契约：幂等（重复调用朝同一终态收敛）；
	// 返回错误时已应用摘要不更新，Drift 如实报 Stale，下次变更重试。该节点
	// 在 Wire 成功后即记为已应用——构造期资源的实际态由 Wire 建立。
	//
	// 协调器在装配期按初始计划的声明节点登记：声明节点集合应稳定（拓扑变化
	// 本就是重启语义），热更中新出现的节点键没有登记面，Drift 会如实报 Missing。
	Reconcile func(ctx context.Context, app *App, node Node) error
}

// Apply 把域追加进声明（Option 实现；Provider 同款手法——域值直接作为
// MustServe/NewApp 的变参项）。
func (d Domain) Apply(decl *Decl) { decl.Domains = append(decl.Domains, d) }

// validateDomains 装配期校验域声明（构造期 fail-fast，先于阶段 A Logger 等
// 一切副作用）：
//   - Name 必填且唯一（生成期校验「name 唯一」的运行期镜像）；
//   - ApplyNode 需有 Declare 可执行，且不与 Wire/Routes 混用（否则接线跑
//     两遍；Services 是延迟注册回调，不在此列）；
//   - Routes 需有 HTTP 面可挂（WithHTTP/WithGatewayRegister 至少其一——
//     gateway 面运行期让位语义见 Routes 字段注释）；
//   - Services 需契约 server.grpc 段存在（与 WithGRPC 的既有 fail-fast 同语义）。
func validateDomains(d *Decl, grpcPresent bool) error {
	seen := map[string]bool{}
	for i, dom := range d.Domains {
		if dom.Name == "" {
			return fmt.Errorf("appkit: domain[%d] (%T) declares no Name", i, dom)
		}
		if seen[dom.Name] {
			return fmt.Errorf("appkit: duplicate domain name %q (each domain must be declared once)", dom.Name)
		}
		seen[dom.Name] = true
		if dom.ApplyNode != nil {
			if dom.Declare == nil {
				return fmt.Errorf("appkit: domain %q declares ApplyNode but no Declare (nothing to execute)", dom.Name)
			}
			if dom.Wire != nil || dom.Routes != nil {
				return fmt.Errorf("appkit: domain %q mixes ApplyNode with compat hooks Wire/Routes "+
					"(wiring would run twice; move the construction into the declared nodes)", dom.Name)
			}
		}
		if dom.Routes != nil && d.HTTPHandler == nil && d.GatewayRegister == nil {
			return fmt.Errorf("appkit: domain %q declares Routes but no HTTP face is declared "+
				"(use appkit.WithHTTP or appkit.WithGatewayRegister)", dom.Name)
		}
		if dom.Services != nil && !grpcPresent {
			return fmt.Errorf("appkit: domain %q declares Services but bootstrap.server.grpc is nil", dom.Name)
		}
	}
	return nil
}

// composeDomainServices 把域 Services 并入声明的 gRPC 注册回调（业务注册在
// 前、域按声明序在后）。业务未声明注册回调而域有 Services 时合成仅有域的
// 回调——std server Provider 据此正常构建 gRPC 面（与 WithGRPC 同一通道，
// 拦截器链等选项声明照常生效）。注册回调是闭包：域实现端口由 Wire 构造，
// 本函数在装配期只合成调用点，解引用发生在迟后阶段 server 构建时。
func composeDomainServices(d *Decl) {
	if len(d.Domains) == 0 {
		return
	}
	business := d.GRPCRegister
	if business == nil && !hasDomainServices(d.Domains) {
		return
	}
	domains := d.Domains
	d.GRPCRegister = func(s *grpc.Server) {
		if business != nil {
			business(s)
		}
		for _, dom := range domains {
			if dom.Services != nil {
				dom.Services(s)
			}
		}
	}
}

// hasDomainServices 报告是否有域声明 Services。
func hasDomainServices(domains []Domain) bool {
	for _, d := range domains {
		if d.Services != nil {
			return true
		}
	}
	return false
}

// declaredDomainNodes 调用 Declare 并规范化返回节点（纯函数投影，不构造实例）。
// 校验：Key 必填；Phase 必须落在 wire/serve/drain/teardown（configure/bootstrap
// 归框架 Provider）；Kind 缺省 domain；来源统一标记为 OriginDomain。
func declaredDomainNodes(ctx context.Context, dom Domain, scope PlanScope) ([]Node, error) {
	raw, err := dom.Declare(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("appkit: domain %q: declare: %w", dom.Name, err)
	}
	out := make([]Node, 0, len(raw))
	for i, n := range raw {
		if n.Key == "" {
			return nil, fmt.Errorf("appkit: domain %q: declared node #%d: empty key", dom.Name, i)
		}
		if n.Phase < PhaseWire {
			return nil, fmt.Errorf("appkit: domain %q: declared node %q: phase %s is framework-owned "+
				"(declare wire/serve/drain/teardown)", dom.Name, n.Key, n.Phase)
		}
		if n.Phase > PhaseTeardown {
			return nil, fmt.Errorf("appkit: domain %q: declared node %q: unknown phase %s", dom.Name, n.Key, n.Phase)
		}
		for _, seg := range n.Segments {
			// 拼错的段只是「消费了一个不存在的路径」：L3 覆盖检查看不到它，
			// 期望态摘要也退化成 Key/Kind 常量（reconcile 节点将永不触发）。
			// `biz.<path>` 指向 Settings 快照的业务段（Q8），其余必须是
			// BootstrapConfig 字段路径。
			if !segmentPathKnown(seg) {
				if _, ok := bizSegmentPath(seg); !ok {
					return nil, fmt.Errorf("appkit: domain %q: declared node %q: unknown segment path %q (want a BootstrapConfig field path like \"server.http\" or a business-config path like \"biz.<section>\")", dom.Name, n.Key, seg)
				}
			}
		}
		if n.Reload == ReloadReconcile {
			// 可协调节点必须可收敛：段缺失时期望态摘要只剩 Key/Kind，配置
			// 变更永远 diff 不出该节点（钩子静默不触发）；钩子缺失时引擎
			// 无法重建域自有状态。两种静默失效都在计划期拦下。
			if dom.Reconcile == nil {
				return nil, fmt.Errorf("appkit: domain %q: declared node %q: reload=reconcile requires Domain.Reconcile", dom.Name, n.Key)
			}
			if dom.Wire == nil && dom.ApplyNode == nil {
				// Wire/ApplyNode 是域资源的唯一构造点；启动后记「已应用」必须有
				// 真实实际态可对应，否则 Drift 从第一天起就在说谎。
				return nil, fmt.Errorf("appkit: domain %q: declared node %q: reload=reconcile requires Domain.Wire or Domain.ApplyNode (the construction point of the reconcilable state)", dom.Name, n.Key)
			}
			if len(n.Segments) == 0 {
				return nil, fmt.Errorf("appkit: domain %q: declared node %q: reload=reconcile requires at least one segment (desired state is derived from segment contents)", dom.Name, n.Key)
			}
		}
		if dom.ApplyNode != nil && n.Opaque {
			// 引擎逐条执行的节点不可能「构造不透明」——这个标记只会让
			// Opaque 计数虚高，掩盖真正未迁移的部分。
			return nil, fmt.Errorf("appkit: domain %q: declared node %q: ApplyNode domain must declare non-opaque nodes", dom.Name, n.Key)
		}
		if dom.ApplyNode != nil && claimsSegment(n.Segments, "server.grpc") {
			// grpc.Server 在 ApplyNode 的窗口（域 BeforeStart）里还不存在；
			// 服务注册只能走 Domain.Services（server 构建时的延迟回调）。
			// 声明该节点而执行点无法兑现 = 计划说谎，计划期拦下。
			return nil, fmt.Errorf("appkit: domain %q: declared node %q: server.grpc cannot be applied in the domain window "+
				"(register services with Domain.Services instead)", dom.Name, n.Key)
		}
		if n.Kind == "" {
			n.Kind = "domain"
		}
		n.Origin = OriginDomain
		out = append(out, n)
	}
	return out, nil
}

// claimsSegment 报告节点声明的段是否落在 path 之下（含 path 本身）。
func claimsSegment(segments []string, path string) bool {
	for _, seg := range segments {
		if seg == path || strings.HasPrefix(seg, path+".") {
			return true
		}
	}
	return false
}

// domainCompatNode 把声明了 Declare 的域的兼容槽位塌缩成单一 Opaque 节点：
// 构造仍不透明，但只占一个逃逸舱名额（设计 §4.6）。无任何兼容钩子时返回
// false——纯声明域不产生构造节点。
func domainCompatNode(dom Domain, spec *Decl) (Node, bool) {
	if dom.ApplyNode != nil {
		return Node{}, false // 节点由引擎逐条执行，构造不再不透明
	}
	var segs []string
	if dom.Routes != nil && spec.HTTPHandler != nil {
		segs = append(segs, "server.http") // 与 wireDomains 的调用条件同源
	}
	if dom.Services != nil {
		segs = append(segs, "server.grpc")
	}
	if dom.Wire == nil && len(segs) == 0 {
		return Node{}, false
	}
	return Node{
		Key:      ResourceKey("domain." + dom.Name + ".wire"),
		Kind:     "domain",
		Segments: segs,
		Phase:    PhaseWire,
		Reload:   ReloadStatic,
		Opaque:   true,
		Origin:   OriginDomain,
	}, true
}

// wireDomains 执行域装配（域 BeforeStart 钩子体）：
//   - ApplyNode 域：其声明节点按**计划序**逐条执行（Phase → Priority → Key，
//     跨域依赖由计划保证）；节点是计划的投影，执行失败带域名与节点 Key；
//   - 兼容钩子域：声明序逐域 Wire → Routes（ApplyNode 域没有兼容钩子，
//     见 validateDomains）。
//
// Services 不在此调用——已并入 GRPCRegister，由迟后阶段 server 构建时调用。
func wireDomains(ctx context.Context, a *App, spec *Decl, owner map[ResourceKey]Domain, plan *Plan) error {
	if plan != nil {
		for _, n := range plan.Nodes {
			dom, ok := owner[n.Key]
			if !ok || dom.ApplyNode == nil {
				continue
			}
			if err := dom.ApplyNode(ctx, a, n); err != nil {
				return fmt.Errorf("appkit: domain %q: apply %q: %w", dom.Name, n.Key, err)
			}
		}
	}
	for _, dom := range spec.Domains {
		if dom.ApplyNode != nil {
			continue
		}
		if dom.Wire != nil {
			if err := dom.Wire(ctx, a); err != nil {
				return fmt.Errorf("appkit: domain %q: %w", dom.Name, err)
			}
		}
		if dom.Routes != nil && spec.HTTPHandler != nil {
			if err := dom.Routes(spec.HTTPHandler); err != nil {
				return fmt.Errorf("appkit: domain %q routes: %w", dom.Name, err)
			}
		}
	}
	return nil
}
