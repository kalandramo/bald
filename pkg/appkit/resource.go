// resource.go 是统一资源面：
// 段驱动 Provider 的产物按**契约段路径**存取（App.Resource 单一访问口），
// 不设逐域类型化访问器。
//
// 分工（契约段 → 实例的构造归 Provider，运行期编排归 appkit）：
//   - Provider（范本：contrib/store-gorm 的 Provider）：按契约子段构建；
//   - 引擎（storeProduct）：产物按段路径落库（六域 map 按 "<顶层段>.<子段>"
//     展开，registry/audit 等整产物落 "<顶层段>"，表外段同构，见
//     bootstrap.go storeProduct）；
//   - appkit：本访问器供 Run 期取用；cleanup 挂停机 Effect——六域注册序在
//     servers 之前，逆序回放保证「服务器先 drain、数据库连接最后关」。
//     database/cache/storage/ai/workflow/broker 段不支持热更新。
//
// 用法（类型安全在消费侧恢复）：
//
//	appkit.MustServe(ctx, std.Standard(), storegorm.Provider(), ...)
//	// Run 后取实例：
//	cli := app.Resource("database.sql").(*gormcrud.Client)
//	// 集合遍历（副本快照，取代原 Databases()/Ais() 的副本语义）：
//	if all, ok := app.Resource("database").(map[string]any); ok { ... }
package appkit

import "strings"

// Resource 按契约段路径取阶段 B Provider 的装配产物（阶段 B 后、Run 期可用；
// 装配构造期尚未装载契约，恒 (nil, false)）。segment 为段路径：
//   - 子段精确路径（"database.sql"）→ 该实例本身；
//   - 顶层段（"database"）→ map[string]any 副本快照（键=子段名）——副本
//     语义沿用原 Databases()/Ais() 的既有取舍（调用方可安全遍历/留存，
//     不暴露内部 map）；registry/audit 等整产物段返回产物本身；
//   - 表外（第三方）段：map 产物按 "<顶层段>.<子段>" 展开、非 map 产物落
//     "<顶层段>"（引擎 storeProduct 的统一约定）。
//
// 未装配或段缺失返回 (nil, false)。tracer/metrics 产物刻意不落资源面
// （shutdown/暴露端由 Provider 自持，cleanup 槽位承担停机回收）。
func (k *App) Resource(segment string) (any, bool) {
	if segment == "" {
		return nil, false
	}
	if v, ok := k.resources[segment]; ok {
		return v, true
	}
	// 顶层段读取：从 "<segment>.*" 子段键聚合出副本快照（拷贝内部 map，
	// 沿用原 Xs() 访问器的副本取舍）。
	prefix := segment + "."
	var snap map[string]any
	for key, v := range k.resources {
		if strings.HasPrefix(key, prefix) {
			if snap == nil {
				snap = make(map[string]any)
			}
			snap[key[len(prefix):]] = v
		}
	}
	if snap == nil {
		return nil, false
	}
	return snap, true
}
