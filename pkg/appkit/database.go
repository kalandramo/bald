// database.go 是 appkit 侧的数据库客户端装配胶水：契约 database 段的
// Registry（构造期）已迁 bootstrap（2026-09-15，见 bootstrap/database.go），
// 本文件保留运行期装配面——WithDatabaseRegistry Option（注入 bootstrap 的
// Registry）、buildDatabases 胶水（阶段 B 调 Registry.Build、实例存 AppKit）、
// Database/Databases 访问器（Run 期取用）。
//
// 分工（构造期归 bootstrap，运行期归 appkit）：
//   - bootstrap.DatabaseRegistry：显式注册 Provider、段枚举、Build；
//   - appkit：BeforeStart（阶段 B，契约装载校验后）调 Build，实例存
//     a.databases；cleanup 挂停机 Effect——注册在 servers 之前，逆序回放
//     保证「服务器先 drain、数据库连接最后关」。database 段不支持热更新。
//
// 用法：
//
//	dr := bootstrap.NewDatabaseRegistry()
//	dr.MustRegister(gormcontract.Type, gormcontract.Provider)    // "sql"
//	dr.MustRegister(mongocontract.Type, mongocontract.Provider) // "mongodb"
//	app, err := appkit.FromBootstrap(cfg, appkit.WithDatabaseRegistry(dr), ...)
//	// Run 后取实例：
//	cli := app.Database(gormcontract.Type).(*gormcrud.Client)
package appkit

import (
	"context"
	"errors"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
)

// WithDatabaseRegistry 注入数据库客户端的契约装配注册表（显式注册
// provider，见 bootstrap.DatabaseRegistry）。契约 database 段任一引擎段
// 存在时，阶段 B 按段名查表构建客户端；database 段不支持热更新。
func WithDatabaseRegistry(r *baldbootstrap.DatabaseRegistry) BootstrapOption {
	return func(s *bootstrapSpec) { s.dbRegistry = r }
}

// buildDatabases 在阶段 B（契约装载校验后）按契约 database 段构建全部
// 已配置引擎的客户端，实例存入 a.databases 供业务经 Database/Databases
// 取用。database 段缺失为 no-op；段存在但未接 DatabaseRegistry 则
// fail-fast（配置意图无法兑现）。
// 返回聚合 cleanup（可为 nil），由 FromBootstrap 挂停机 Effect。
func buildDatabases(a *AppKit, cfg *bootstrapv1.BootstrapConfig, spec *bootstrapSpec) (func(), error) {
	dbcfg := cfg.GetDatabase()
	if dbcfg == nil {
		return nil, nil // 未配置任何数据库
	}
	if spec.dbRegistry == nil {
		return nil, errors.New("appkit: bootstrap.database present but no DatabaseRegistry wired (WithDatabaseRegistry missing)")
	}
	clients, cleanup, err := spec.dbRegistry.Build(context.Background(), dbcfg)
	if err != nil {
		return nil, err
	}
	a.databases = clients
	return cleanup, nil
}

// Database 返回按契约段装配的引擎客户端实例（阶段 B 后、Run 期可用；
// FromBootstrap 构造期尚未装载契约，返回空）。
// typ 为契约段名（见 contrib/database/<engine>/contract.Type，如 "sql"）。
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
