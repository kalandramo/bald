// domain.go 实现业务域一等概念 appkit.Domain（装配层评估 §4.3 /
// D1-target-appkit.md §1.3-§1.4，判据 ① 的框架侧）。
//
// 域是「每域包导出一个声明值」的一等 Option：Domain 实现 Option（Apply 追加进
// Decl.Domains——Provider 同款手法），变参直传 MustServe：
//
//	appkit.MustServe(ctx, std.Standard(), authv1.Domain, dictv1.Domain)
//
// 接缝裁定（评估 §4.3，架构裁定照录）：appkit 只接受「域声明值」，不新增注册
// 表式 API；域清单的承载者是生成文件（Go 无运行期包发现，W3 的 bald gen
// domain 产出 domains.gen.go 聚合清单；_example 的手写 domains.gen.go 即其
// 目标形态）。init() 自注册禁止（「不做 init() 自注册」哲学，评估 §4.3）。
//
// 三个槽位（以 bald-admin 21 域的真实诉求裁剪，YAGNI）：
//   - Wire：域在装配期自我接线——读自己的配置段（app.Settings() 快照）、构造
//     自己的能力（限流/熔断/重试等）、持有自己的端口（§3.3「业务能力策略住
//     装配层」的修复：builders.go 那类读业务阈值的构造器迁回域包，装配层不再
//     出现业务阈值）；
//   - Routes：HTTP 路由注册——接收 WithHTTP 声明的业务 handler（gin 场景即
//     *gin.Engine；appkit 不 import 路由器，路由器耦合由域自持），在 server
//     构建前生效；gateway 转码面占用 server.http 端口时让位（同端口单面语义）；
//   - Services：gRPC service 注册——并入 server 构建的注册回调（业务注册在
//     前、域按声明序在后），实现端口由 Wire 构造。
//
// 裁剪说明（为何没有迁移/ConfigKeys 槽位）：
//   - 迁移：执行迁移需要存储栈类型（gorm AutoMigrate），appkit 不 import 存储
//     栈——域在 Wire 内经 App.Resource("database.sql") 取连接自行执行
//     （bald-admin audit_wiring.go 的 AutoMigrate 即既有先例）；声明式迁移清单
//     归 W3 生成物（评估 §4.6）。
//   - ConfigKeys：业务配置段只进 Settings 快照、不进契约（bald-admin 的 biz
//     段先例——业务键被契约 Unmarshal 丢弃，与 L3 段覆盖检查无交互）；
//     StrictContract 白名单由应用声明（appkit.StrictContract）。
//
// Wire 执行时点（域 BeforeStart 钩子，注册序 = 执行序，wiring_order_test 族
// 钉住）：
//
//	框架钩子（契约装载 + 阶段 B Provider 循环 + L3）→ 业务 BeforeStart → 域 →
//	迟后阶段（server Provider）→ 额外服务器工厂
//
// 该窗口由两约束夹出：域需要资源面已就绪（阶段 B 产物经 App.Resource 可取）；
// 域的路由/服务注册须在 server 构建前生效。放在业务钩子之后，域还能消费业务
// 钩子建立的运行期资源（bald-admin 的 authenticator/仓储均建于 BeforeStart）。
// 多域次序 = 声明序（Decl.Domains 的 Apply 追加序 = MustServe 变参序）；任一
// 域 Wire/Routes 失败 fail-fast（错误带域名，经 Run 的 beforeStart 失败路径
// 回滚账本）。NewApp 不消费 Domains（非第二条装配路径，见 NewApp 注释）。
package appkit

import (
	"context"
	"fmt"
	"net/http"

	"google.golang.org/grpc"
)

// Domain 是业务域的装配期声明值（每域包导出一个；变参直传 MustServe）。
//
// 三个钩子全部可选（nil = 该域不声明该面）；Name 必填且全应用唯一（装配期
// fail-fast，错误信息按域名定位）。
type Domain struct {
	// Name 域名：fail-fast 错误定位与生成物清单（domains.gen.go）对账的标识。
	Name string

	// Wire 域自我接线（装配期执行一次）：读自己的配置段（app.Settings()）、
	// 构造自己的能力、持有自己的端口（§3.3 修复）。执行时资源面已就绪
	// （App.Resource 可取阶段 B 产物），业务 BeforeStart 钩子已跑完（域可
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
	Services func(s *grpc.Server)
}

// Apply 把域追加进声明（Option 实现；Provider 同款手法——域值直接作为
// MustServe/NewApp 的变参项）。
func (d Domain) Apply(decl *Decl) { decl.Domains = append(decl.Domains, d) }

// validateDomains 装配期校验域声明（构造期 fail-fast，先于阶段 A Logger 等
// 一切副作用）：
//   - Name 必填且唯一（生成期校验「name 唯一」的运行期镜像，评估 §4.3 裁决 4）；
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

// wireDomains 执行域装配（域 BeforeStart 钩子体）：声明序逐域 Wire → Routes。
// Services 不在此调用——已并入 GRPCRegister，由迟后阶段 server 构建时调用。
func wireDomains(ctx context.Context, a *App, spec *Decl) error {
	for _, dom := range spec.Domains {
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
