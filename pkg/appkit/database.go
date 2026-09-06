// database.go 实现数据库客户端的契约装配注册表（bootstrapv1.Database 段
// → 具体引擎客户端实例）。
//
// 与 RegistrarRegistry 同模式：显式注册（不用 init()+blank import，主程序
// 在 main() 里逐个 MustRegister）；段存在但未注册 fail-fast——未 import 的
// 引擎在启动期报错，而不是静默无数据库；cleanup 挂停机 Effect 逆序回放。
//
// 与 registry「type 单选」语义的差异：Database 是 optional 段集合——8 个
// 引擎段可并存（主库 + 检索库 + 时序库），装配语义是「每段各自构建、
// 全部返回」，键为契约段名。
//
// 类型边界：引擎客户端异构（*gorm.Client / *mongo.Client …），Provider
// 返回 any 是装配层的必然；类型安全在消费侧恢复——SQL 客户端接
// contrib/store-gorm 变 store.DBProvider[T]。any 不向框架内部扩散：
// appkit 只保存与转发，不做任何断言。
//
// 生命周期：阶段 B（BeforeStart，契约装载校验后）构建；database 段不支持
// 热更新（连接池重建侵入性大，变更需重启生效，与 registry 段同理）。
// 停机 Effect 注册在 servers 之前——逆序回放保证「服务器先 drain、
// 数据库连接最后关」。
//
// 用法：
//
//	dr := appkit.NewDatabaseRegistry()
//	dr.MustRegister(gormcontract.Type, gormcontract.Provider)    // "sql"
//	dr.MustRegister(mongocontract.Type, mongocontract.Provider) // "mongodb"
//	app, err := appkit.FromBootstrap(cfg, appkit.WithDatabaseRegistry(dr), ...)
//	// Run 后取实例：
//	cli := app.Database(gormcontract.Type).(*gormcrud.Client)
package appkit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// DatabaseProvider 按契约 Database 段构造具体引擎客户端。
// 返回 (实例, cleanup, error)：cleanup 释放连接池/client 资源（可为 nil），
// 由 FromBootstrap 在停机 Effect 中逆序回放。
// 各引擎的实现见 contrib/database/<engine>/contract 包。
type DatabaseProvider func(ctx context.Context, cfg *bootstrapv1.Database) (any, func(), error)

// DatabaseRegistry 是数据库引擎 Provider 的显式注册表，键为契约段名。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
type DatabaseRegistry struct {
	mu        sync.Mutex
	providers map[string]DatabaseProvider
}

// NewDatabaseRegistry 创建空注册表。
func NewDatabaseRegistry() *DatabaseRegistry {
	return &DatabaseRegistry{providers: make(map[string]DatabaseProvider)}
}

// Register 注册一个引擎 Provider；重名/空名/nil provider 均 fail-fast。
func (r *DatabaseRegistry) Register(typ string, p DatabaseProvider) error {
	if typ == "" {
		return errors.New("appkit: database type is empty")
	}
	if p == nil {
		return fmt.Errorf("appkit: database provider for %q is nil", typ)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]DatabaseProvider)
	}
	if _, dup := r.providers[typ]; dup {
		return fmt.Errorf("appkit: database provider %q already registered", typ)
	}
	r.providers[typ] = p
	return nil
}

// MustRegister 注册引擎 Provider，失败 panic（装配期错误应当炸在启动时）。
func (r *DatabaseRegistry) MustRegister(typ string, p DatabaseProvider) {
	if err := r.Register(typ, p); err != nil {
		panic(err)
	}
}

// databaseSection 契约段枚举：固定顺序即装配顺序（proto 字段序），
// 新引擎在 bconf 加段后在此追加（编译期漏加 = 该段配置静默无消费，
// 由「契约字段须全有消费者」审查兜底）。
type databaseSection struct {
	name   string
	exists func(*bootstrapv1.Database) bool
}

var databaseSections = []databaseSection{
	{"sql", func(d *bootstrapv1.Database) bool { return d.GetSql() != nil }},
	{"mongodb", func(d *bootstrapv1.Database) bool { return d.GetMongodb() != nil }},
	{"clickhouse", func(d *bootstrapv1.Database) bool { return d.GetClickhouse() != nil }},
	{"doris", func(d *bootstrapv1.Database) bool { return d.GetDoris() != nil }},
	{"elasticsearch", func(d *bootstrapv1.Database) bool { return d.GetElasticsearch() != nil }},
	{"opensearch", func(d *bootstrapv1.Database) bool { return d.GetOpensearch() != nil }},
	{"influxdb", func(d *bootstrapv1.Database) bool { return d.GetInfluxdb() != nil }},
	{"cassandra", func(d *bootstrapv1.Database) bool { return d.GetCassandra() != nil }},
}

// Build 按契约段装配全部已配置的数据库客户端：段存在 → 查表构建。
// 全部段缺失为 no-op（返回空 map 与 nil cleanup）；任一段存在但未注册
// Provider 均 fail-fast（显式注册的天然收益）；构建失败回滚已建实例
// 的 cleanup（逆序）。
func (r *DatabaseRegistry) Build(ctx context.Context, cfg *bootstrapv1.Database) (map[string]any, func(), error) {
	if cfg == nil {
		return nil, nil, nil // 未配置任何数据库
	}
	r.mu.Lock()
	provs := make(map[string]DatabaseProvider, len(r.providers))
	for k, v := range r.providers {
		provs[k] = v
	}
	r.mu.Unlock()

	var (
		clients  = make(map[string]any)
		cleanups []func()
		rollback = func() {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
		}
	)
	for _, sec := range databaseSections {
		if !sec.exists(cfg) {
			continue // 段缺失 = 未声明该引擎
		}
		p, ok := provs[sec.name]
		if !ok {
			rollback()
			return nil, nil, fmt.Errorf("appkit: database.%s present but provider not registered (import the backend contract package and MustRegister it)", sec.name)
		}
		cli, cleanup, err := p(ctx, cfg)
		if err != nil {
			rollback()
			return nil, nil, fmt.Errorf("appkit: build database %s: %w", sec.name, err)
		}
		clients[sec.name] = cli
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}
	aggregated := func() { rollback() }
	if len(cleanups) == 0 {
		aggregated = nil
	}
	return clients, aggregated, nil
}

// WithDatabaseRegistry 注入数据库客户端的契约装配注册表（显式注册
// provider，见 DatabaseRegistry）。契约 database 段任一引擎段存在时，
// 阶段 B 按段名查表构建客户端；database 段不支持热更新。
func WithDatabaseRegistry(r *DatabaseRegistry) BootstrapOption {
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
