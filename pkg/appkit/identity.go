package appkit

// identity.go 定义 appkit 对业务身份的最小端口（D1 §4.4，装配层评估 §4.5）。
//
// 背景：appkit 为发运行期审计事件（组件挂载/卸载、R1-2 协调收敛/错误）需要
// 「ctx 里的业务身份（subject/tenant）」。此前直接 import pkg/authn 读
// AuthClaims——App 层被迫认识业务身份的存放方式。B7 依赖倒置：appkit 只声明
// 它需要的取值函数形状，authn 到端口的适配由应用（或 contrib）以 1 行 lambda
// 提供；缺省（未声明 Identity）为空身份——与今日 claims 为 nil 时行为一致。
//
// 用法（应用侧适配）：
//
//	appkit.Identity(func(ctx context.Context) (subject, tenant string) {
//	    if claims := authn.AuthClaimsFromContext(ctx); claims != nil {
//	        return claims.Subject, claims.TenantID
//	    }
//	    return "", ""
//	})

import "context"

// IdentityFunc 从 ctx 取审计事件的业务身份（subject/tenant）。返回空串即
// 空身份；实现必须纯函数式（只读 ctx，不得阻塞——审计旁路不阻断业务）。
type IdentityFunc func(ctx context.Context) (subject, tenant string)

// Identity 声明审计事件的业务身份取值端口（见包文件 identity.go 的动机与
// 用法）。多次声明后写覆盖；nil 函数等价于未声明（空身份）。
func Identity(fn IdentityFunc) Option {
	return optionFunc(func(d *Decl) { d.Identity = fn })
}
