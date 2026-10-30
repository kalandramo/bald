// 本包是 bald-demo 示例的装配层（对照 `bald gen app --spec` 产出的
// internal/bootstrap/assemble.gen.go）：Options(role) 是装配的单一声明面，
// 两个进程——cmd/bald-demo 的 api 角色与 cmd/bald-demo-worker 的 worker 角色
// ——的 MustServe 只消费它，入口里没有装配知识。
//
// 与生成物的唯一差别是「谁写这个函数」：生成物由 CLI 产出并标 DO NOT EDIT，
// 本文件手写（演示业务 Provider、域与网关接线）。三层形态与生成物一致：
// 角色 → 契约文件（configFileForRole）、角色 → 传输面（Options 的 role 分支）、
// 每角色一个 cmd —— 这就是「一份声明两个进程」的参考实现。
//
// 装配分工（配置驱动参数，代码声明能力）：
//
//	框架内化（Serve + std.Standard()）：--config 解析、契约装载、Bind×3、
//	配置装载+校验、日志两阶段（启动默认→契约重建）、热更新回调、app 元数据
//	（id/name/version/env/stop_timeout）、服务器构造（std server Provider，
//	能力声明驱动）、注册中心启停、停机资源释放。
//	本包（装配声明）：角色、传输面、域清单、拦截器链、探针依赖的组装。
//	第三方 Provider（加法式）：storegorm.Provider 消费契约 database.sql 段
//	（目标 main.go 形态的首个第三方后端范本，见 pkg/appkit/std 包文档）；
//	loggerProvider 覆盖 std 默认日志（Attr 层脱敏 password/token + service.name）。
//	业务域（一等概念，判据 ① 活体）：greetv1.Domain 经域清单（domains.gen.go）
//	变参直传——域自我接线（读自己的配置段、构造能力、持有端口）+ 自持路由与
//	gRPC service 注册；新增一个业务域只手写 3 个文件、装配零改动。
//	代码声明（业务必供）：gin 路由（internal/apiserver）、拦截器链序、
//	就绪探针依赖、gateway 转码注册（WithGatewayRegister，-tags grpcgw）。
//
// 运行（契约路径相对运行目录，故在 _example/bald 下执行）：
//
//	go run ./cmd/bald-demo                        # api：HTTP + gRPC
//	go run ./cmd/bald-demo-worker                 # worker：只开 gRPC 面
//	go run -tags grpcgw ./cmd/bald-demo           # api 的网关转码面
//	go run -tags nacos ./cmd/bald-demo            # nacos 注册/配置中心（见 register_nacos.go）
//
// 覆盖与多环境（四源优先级 flag > env > 文件 > 远程，键路径 = 契约字段路径）：
//
//	BALD_DEMO_SERVER_HTTP_ADDR=:18080 go run ./cmd/bald-demo  # env 覆盖 http.addr
//	go run ./cmd/bald-demo --server.http.addr=:18080          # flag 优先级最高
//	go run ./cmd/bald-demo --env=prod                         # 选 configs/bald-demo-prod.yaml
//	go run ./cmd/bald-demo --log.format=json --log.level=debug
//
// 验证 HTTP 示例路由（默认构建，gin 演示路由挂在 server.http 端口）：
//
//	curl.exe -i http://127.0.0.1:8080/v1/ping
//	curl.exe --% -i -XPOST http://127.0.0.1:8080/v1/greet -H "Content-Type: application/json" -d "{\"name\":\"bald\"}"
//	curl.exe --% -XPOST "http://127.0.0.1:8080/v1/articles/42?lang=zh" -H "Content-Type: application/json" -d "{\"title\":\"hi\"}"
//
// grpcgw 构建下 REST 面换成网关转码（契约 server.http.driver=grpc-gateway，
// 同端口单面语义），greet 域路由经转码进同一个 gRPC service。
package bootstrap

import (
	"context"
	"net/http"

	"google.golang.org/grpc"

	storegorm "github.com/kalandramo/bald/contrib/store-gorm"
	"github.com/kalandramo/bald/health"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/appkit/std"
	"github.com/kalandramo/bald/registry/inmemory"

	apiserver "github.com/kalandramo/bald/example/bald/internal/apiserver"
)

// 角色名（对应 AppSpec.meta.roles[].name）：每角色一个 cmd、一份契约。
const (
	// RoleAPI 开 HTTP 面（默认构建 gin 演示路由 / -tags grpcgw 网关转码面）+ gRPC 面。
	RoleAPI = "api"
	// RoleWorker 只开 gRPC 面——它的契约里没有 server.http 段。
	RoleWorker = "worker"
)

// Options 返回指定角色的装配声明（该角色进程里 MustServe 的唯一实参）。
//
// 未知角色返回 nil（MustServe 以空装配失败）：角色名写错必须是启动期错误，
// 不能静默退化成某个角色的装配——与生成物同一判据。
func Options(role string) []appkit.Option {
	cfg := configFileForRole(role)
	if cfg == "" {
		return nil
	}
	opts := []appkit.Option{
		// 框架自有后端的段驱动 Provider 集合：server.http/grpc 服务器构造
		// （按能力声明）、audit 的 log 后端与 logger（契约段驱动）。
		std.Standard(),
		// 第三方 Provider（加法式范本）：消费契约 database.sql 段，产物经
		// storegorm.SQL.Resource(app) 取用，停机自动释放连接池。
		storegorm.AppProvider(),
		// 配置文件按角色分叉（每角色一份契约）；- 由契约 app.name 派生 env
		// 前缀（BALD_DEMO_*）与多环境文件名（契约无 app.name 时启动 fail-fast）。
		appkit.ConfigFile(cfg),
		// 角色进计划与计划缓存键：doctor 的报告与基线对比按角色分核。
		appkit.WithRole(role),
		appkit.WatchConfigFile(true),
		// gRPC 面：两个角色都开。拦截器链是安全策略，归业务自持
		//（链序见 newGRPCServerOptions，ErrorInterceptor 必须最外层）。
		appkit.WithGRPCOptions(newGRPCServerOptions),
		// 健康检查默认装配：HTTP 双探针 + gRPC health 状态对称联动
		//（无 HTTP 面的角色只有 gRPC health 一侧）。
		appkit.WithHealth(healthChecker),
		// 服务注册中心：默认构建用 inmemory（显式实例优先于契约 registry 段，
		// 零外部依赖覆盖 register→运行→deregister 全流程）；-tags nacos 时
		// 走段驱动 Provider（见 register_nacos.go）。
		appkit.Registrar(inmemory.New()),
		// 日志装饰（B7 后装饰归 provider 自持）：本应用的 loggerProvider 覆盖
		// std 默认（段路径末者胜），恢复 Attr 层脱敏与服务标识。
		newLoggerProvider(),
	}
	// 传输面按角色分叉——这就是「一份声明两个进程」的全部差异点。
	switch role {
	case RoleAPI:
		// api：HTTP 面 + gRPC 面（gRPC 服务器由 greet 域的 Services 声明驱动，
		// 故这里不 extra 声明 WithGRPC）。HTTP 面二选一，同一 server.http 段
		// 只跑一个面。
		if gatewayRegister != nil {
			// grpcgw 构建：网关转码面（契约 server.http.driver=grpc-gateway），
			// 同一 server.http 端口只跑一个面，gin 演示路由让位。
			opts = append(opts, appkit.WithGatewayRegister(gatewayRegister))
		} else {
			// 默认构建：gin 演示路由（gin.Engine 即 http.Handler）。
			opts = append(opts, appkit.WithHTTP(apiserver.NewRouter()))
		}
	case RoleWorker:
		// worker：只开 gRPC 面，且不装任何业务域（见 domainOptionsForRole），
		// 于是 service 只剩框架的 health——本角色的价值是证明「不带 HTTP、
		// 不带 REST 路由的进程」能独立装配与启停。它的契约里因此没有
		// server.http 段（能力声明与契约段必须对偶）。
		opts = append(opts, appkit.WithGRPC(registerGRPCService, newGRPCServerOptions()...))
	}
	// 业务域（一等概念）：域清单（domains.gen.go）按角色分叉——api 装 greet
	// 域（域自我接线、自持 REST 路由与 gRPC service 注册），worker 一个业务域
	// 都不装（runner 形态：只保留框架面）。
	opts = append(opts, domainOptionsForRole(role)...)
	// nacos 后端（注册中心 + 配置中心）：默认空操作，-tags nacos 时接入。
	opts = append(opts, nacosBootstrapOptions()...)
	// 计划缓存：启动期把计划按 (role, input) 记为基线，doctor 据此报告装配
	// 变更（本地文件，非执行依据；读写失败只告警）。
	opts = append(opts, appkit.WithPlanCache(".bald/plan-cache.json"))
	// 构建期变体（Q7）：声明恒进计划——不带 tag 的构建也能从 --plan/doctor
	// 的 variants 段看到两条分叉（declared/active），命中的变体另产 ambient
	// 警告。register_*.go 只保留接线本身（gatewayRegister 等），不再各自登记
	// ambient；两条声明写在始终参与编译的这里，所以未命中也可见。
	opts = append(opts, appkit.WithVariantDecls(
		appkit.VariantDecl{
			Name:      "grpcgw",
			BuildTags: []string{"grpcgw"},
			Detail:    "server.http may be served by grpc-gateway transcoding; wiring is compiled only under -tags grpcgw",
			Active:    appkit.HasBuildTags(appkit.BuildTags(), "grpcgw"),
		},
		appkit.VariantDecl{
			Name:      "nacos",
			BuildTags: []string{"nacos"},
			Detail:    "nacos registry provider and remote config source are compiled only under -tags nacos",
			Active:    appkit.HasBuildTags(appkit.BuildTags(), "nacos"),
		},
	))
	// 逃逸舱预算：api 角色 2 个（程序化 registry、std server）——greet 域已迁到
	// Domain v2 的 ApplyNode 形态，不再产生兼容节点，逃逸舱低于设计 §7 的
	// v0.28 目标（≤3）。预算=当前值，新增逃逸舱会让 --plan/doctor 出现
	// opaque-budget 告警。
	opts = append(opts, appkit.WithOpaqueBudget(2))
	return opts
}

// domainOptionsForRole 按角色选择业务域清单（对照生成物 domains.gen.go 的
// domainOptionsForRole：单模板源，装配层只做选择）。
//
// worker 不装 greet 域不只是「示范分叉」，而是 appkit 的硬约束：域声明了
// Routes 就必须同时声明 HTTP 面，否则启动报
// domain "greet" declares Routes but no HTTP face is declared。
// 因此「不开 HTTP 面的角色」只能装配不带 REST 路由的域，或者不装域。
func domainOptionsForRole(role string) []appkit.Option {
	switch role {
	case RoleAPI:
		return domainOptions
	case RoleWorker:
		return nil
	}
	return nil
}

// configFileForRole 把角色映射到它的契约文件：每角色一份
// configs/<app>-<role>.yaml。worker 的契约里没有 server.http 段。
func configFileForRole(role string) string {
	switch role {
	case RoleAPI:
		return "configs/bald-demo-api.yaml"
	case RoleWorker:
		return "configs/bald-demo-worker.yaml"
	}
	return ""
}

// gatewayRegister 是 grpc-gateway 转码注册回调（REST → gRPC 转码）。
//
// 默认 nil：HTTP 面走 gin 演示路由。启用 `go run -tags grpcgw` 后，由
// register_grpcgw.go 的 init 注入 registerGateway，配合契约
// server.http.driver=grpc-gateway 接入网关转码面。
//
// 用回调而非直接构造，是因为 GatewayServer 依赖 grpc-gateway（较重），默认
// 构建不该引入它。网关也不是独立服务器：同一 server.http 段只跑一个面，
// 模式由契约 server.http.driver 驱动。
var gatewayRegister func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error)

// healthChecker 是探针数据源：HTTP /healthz /readyz 与 gRPC health 状态同源
// （未就绪 → /readyz 503、gRPC NOT_SERVING，K8s 摘流量）。
var healthChecker = func() *health.Health {
	h := health.New()
	h.Register("demo", health.PingFunc(func(context.Context) error {
		// TODO: 在此检查业务依赖（如 DB ping、下游连通性）。返回 nil=就绪。
		return nil
	}))
	return h
}()
