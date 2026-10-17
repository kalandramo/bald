// database.go 是 appkit 侧的数据库客户端资源面：契约 database 段经段驱动
// Provider 装配（引擎把产物 map[子段名]any 落入 a.databases，见 provider.go
// 的 storeProduct），本文件保留 Run 期取用面——Database/Databases 访问器
// （W3 的 B4 将统一为 App.Resource(segment)，本波保持不动）。
//
// 分工（契约段 → 实例的构造归 Provider，运行期编排归 appkit）：
//   - Provider（范本：contrib/store-gorm 的 Provider，或包一层
//     bootstrap.DatabaseRegistry 做多引擎分派）：显式注册引擎工厂、按
//     database.<engine> 子段构建；
//   - appkit：阶段 B 调 Build（canonical 序：database 先于 audit——审计
//     store 后端经 App.Database("sql") 取连接），实例存 a.databases；
//     cleanup 挂停机 Effect——注册在 servers 之前，逆序回放保证「服务器
//     先 drain、数据库连接最后关」。database 段不支持热更新。
//
// 用法：
//
//	appkit.MustServe(ctx, std.Standard(), storegorm.Provider(), ...)
//	// Run 后取实例（类型安全在消费侧恢复）：
//	cli := app.Database(gormcontract.Type).(*gormcrud.Client)
package appkit

// Database 返回按契约段装配的引擎客户端实例（阶段 B 后、Run 期可用；
// 装配构造期尚未装载契约，返回空）。
// typ 为契约子段名（见 contrib/database/<engine>/contract.Type，如 "sql"）。
// 类型安全在消费侧恢复：kit.Database(gormcontract.Type).(*gormcrud.Client)。
//
// 并发语义：databases 只在 BeforeStart（阶段 B）单次写入，Run 后只读，
// happens-before 由 Run 启动流程保证。
func (k *AppKit) Database(typ string) (any, bool) {
	cli, ok := k.databases[typ]
	return cli, ok
}

// Databases 返回全部已装配引擎客户端（副本，段名→实例），用于遍历诊断。
func (k *AppKit) Databases() map[string]any {
	out := make(map[string]any, len(k.databases))
	for typ, cli := range k.databases {
		out[typ] = cli
	}
	return out
}
