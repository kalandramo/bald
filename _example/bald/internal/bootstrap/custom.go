// 本文件是生成器之外的装配接线（gen app 生成一次，此后归业务维护）：
// assemble.gen.go 只声明 AppSpec 能表达的东西，第三方 Provider、显式
// Registrar、拦截器链、契约白名单与逃逸舱预算收敛在这里，由 Options()
// 末尾的 customOptions 展开（同名单值字段末者胜）。
package bootstrap

import (
	storegorm "github.com/kalandramo/bald/contrib/store-gorm"
	"github.com/kalandramo/bald/pkg/appkit"
)

// 角色名（对应 AppSpec.meta.roles[].name）。
const (
	RoleAPI    = "api"
	RoleWorker = "worker"
)

// customOptions 返回应用自定义接线（两个角色相同；按角色分叉时可读 role）。
func customOptions(_ string) []appkit.Option {
	opts := []appkit.Option{
		// database.sql 段 → GORM 连接池（资源容器取用，停机释放）。
		storegorm.AppProvider(),
	}
	// 注册中心按构建形态接：见 registrar_inmemory.go（默认）与
	// register_nacos.go（-tags nacos）。
	opts = append(opts, registrarOptions()...)
	return append(opts,
		// 拦截器链是安全策略，归应用自持（ErrorInterceptor 必须在最外层）；
		// 链的限流/超时参数来自契约 server.grpc.middleware 段——
		// WithGRPCOptionsContract 是该段的消费口，与 HTTP 面的
		// WithHTTPContract(apiserver.NewRouter) 对称。
		appkit.WithGRPCOptionsContract(newGRPCChain),
		// Attr 层脱敏 + 服务标识：覆盖 std 的默认 logger Provider。
		newLoggerProvider(),
		// biz.* 是业务配置段（域经 Settings 快照读取），不在 bconf 契约里。
		appkit.StrictContract("biz"),
		// 逃逸舱预算：进程内 registry + std 的两个 server 面（server.http、
		// server.grpc 各一节点，注册回调装在闭包里）。计量口径是节点数，
		// 分段声明的面各自计——转码面接管 server.http 时仍是 3（同键替换）。
		// -tags nacos 时 registry 由契约段派生，不是舱（实测 2 个）；预算是上界，
		// 少一个不响，plan_acceptance_test.go 那条按构建形态派生的清单才是判据。
		appkit.WithOpaqueBudget(3),
	)
}

// variantOpts 是构建期变体的应用级接线表（键 = AppSpec.variants.name）：
// 带 //go:build 的文件在 init 里登记，variantOptions() 在 Options() 调用期
// 取出——init 顺序与文件名无关。
var variantOpts = map[string][]appkit.Option{}

func customVariantOpts(name string) []appkit.Option { return variantOpts[name] }
