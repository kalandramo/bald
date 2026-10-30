// 本文件是生成器之外的装配接线（gen app 生成一次，此后归业务维护）：
// assemble.gen.go 只声明 AppSpec 能表达的东西，第三方 Provider、显式
// Registrar、拦截器链、契约白名单与逃逸舱预算收敛在这里，由 Options()
// 末尾的 customOptions 展开（同名单值字段末者胜）。
package bootstrap

import (
	storegorm "github.com/kalandramo/bald/contrib/store-gorm"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/registry/inmemory"
)

// 角色名（对应 AppSpec.meta.roles[].name）。
const (
	RoleAPI    = "api"
	RoleWorker = "worker"
)

// customOptions 返回应用自定义接线（两个角色相同；按角色分叉时可读 role）。
func customOptions(_ string) []appkit.Option {
	return []appkit.Option{
		// database.sql 段 → GORM 连接池（资源容器取用，停机释放）。
		storegorm.AppProvider(),
		// 默认构建用进程内注册中心；-tags nacos 时由 nacos 变体的段驱动
		// Provider 接管（显式实例优先于契约，二者不同时生效）。
		appkit.Registrar(inmemory.New()),
		// 拦截器链是安全策略，归应用自持（ErrorInterceptor 必须在最外层）。
		appkit.WithGRPCOptions(newGRPCChain),
		// Attr 层脱敏 + 服务标识：覆盖 std 的默认 logger Provider。
		newLoggerProvider(),
		// biz.* 是业务配置段（域经 Settings 快照读取），不在 bconf 契约里。
		appkit.StrictContract("biz"),
		// 逃逸舱预算：程序化 registry 与 std server 两个框架节点。
		appkit.WithOpaqueBudget(2),
	}
}

// variantOpts 是构建期变体的应用级接线表（键 = AppSpec.variants.name）：
// 带 //go:build 的文件在 init 里登记，variantOptions() 在 Options() 调用期
// 取出——init 顺序与文件名无关。
var variantOpts = map[string][]appkit.Option{}

func customVariantOpts(name string) []appkit.Option { return variantOpts[name] }
