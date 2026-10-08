package authz

// ---------------------------------------------------------------------------
// 可选重载能力（D7）
//
// 背景：`Authorizer` 只有单方法 `Authorize`，是**无状态判定**契约。这带来两个
// 后果（见《框架缺陷报告》D7）：
//  1. 策略/角色绑定在构造期一次性装载，运行期变更（如新注册用户）无法生效——
//     必须重启进程；
//  2. 策略引擎的装配语义（按配置选引擎、注入模型、取回引擎）无处安放。
//
// 修法取**最小破坏**路径：不扩展 `Authorizer` 本身（那会迫使所有既有实现——
// `Func`/`AllowAll`/`DenyAll`/业务自定义——都补一个多数用不到的 `Reload`），
// 而是新增**可选**的扩展接口 [ReloadableAuthorizer]。能力发现走类型断言，
// 与 [ratelimit.InflightLimiter] 同款惯例。
// ---------------------------------------------------------------------------

// ReloadableAuthorizer 由支持**运行期重载策略**的 Authorizer 实现。
//
// 语义：
//   - Reload 从底层策略源重新装载策略（角色绑定、权限点等），使运行期变更
//     立即对后续 Authorize 生效；
//   - 实现必须并发安全——运行期重载与并发的 Authorize 判定会同时发生；
//   - 重载失败时，实现应保持**上一个可用策略**不停机（fail-safe），并返回
//     错误供调用方记录/重试；
//   - Reload 不要求幂等语义（重复重载是合法无操作）。
//
// 调用方按需发现能力：
//
//	if rl, ok := authorizer.(authz.ReloadableAuthorizer); ok {
//	    if err := rl.Reload(); err != nil { /* 记录，保留旧策略 */ }
//	}
//
// 或直接用 [Reload] 助手（对不支持重载的实现返回 false, nil）。
type ReloadableAuthorizer interface {
	Authorizer

	// Reload 重新装载策略。失败时应保留旧策略并返回错误。
	Reload() error
}

// Reload 尝试重载授权器的策略。
//
// 返回 (true, err) 表示该授权器支持重载且已执行（err 为其结果）；
// (false, nil) 表示该授权器不支持重载（未实现 [ReloadableAuthorizer]），
// 属**正常**情形而非错误——调用方可据此静默跳过（如统一在配置变更钩子里
// 调 Reload，而不关心底层是否可重载）。
func Reload(a Authorizer) (bool, error) {
	rl, ok := a.(ReloadableAuthorizer)
	if !ok {
		return false, nil
	}
	return true, rl.Reload()
}
