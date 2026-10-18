package bootstrap

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// DatabaseProvider 按契约 Database 段构造具体引擎客户端。
// 返回 (实例, cleanup, error)：cleanup 释放连接池/client 资源（可为 nil），
// 由调用方释放（appkit 挂停机 Effect 逆序回放）。
// 各引擎的实现见 contrib/database/<engine>/contract 包。
type DatabaseProvider func(ctx context.Context, cfg *bootstrapv1.Database) (any, func(), error)

// DatabaseRegistry 是数据库引擎 Provider 的显式注册表，键为契约段名。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
//
// 显式注册（不用 init()+blank import，主程序在 main() 里逐个 MustRegister）；
// 段存在但未注册 fail-fast——未 import 的引擎在启动期报错，而不是静默无数据库。
//
// 与 registry「type 单选」语义的差异：Database 是 optional 段集合——8 个
// 引擎段可并存（主库 + 检索库 + 时序库），装配语义是「每段各自构建、
// 全部返回」，键为契约段名。
//
// 类型边界：引擎客户端异构（*gorm.Client / *mongo.Client …），Provider
// 返回 any 是装配层的必然；类型安全在消费侧恢复——SQL 客户端接
// contrib/store-gorm 变 store.DBProvider[T]。any 不向框架内部扩散：
// 本包只保存与转发，不做任何断言。
// 薄壳（批 1 收敛，见 registry.go）：互斥锁 + provider 表 + Register/MustRegister
// 由嵌入的 Registry[T] 载体承载（文案经 TypeTexts 与既有文案逐字一致）；
// 本文件只保留 database 特有的段表与 Build 装配序。
type DatabaseRegistry struct {
	*Registry[DatabaseProvider]
}

// NewDatabaseRegistry 创建空注册表。
func NewDatabaseRegistry() *DatabaseRegistry {
	return &DatabaseRegistry{Registry: NewRegistry[DatabaseProvider](TypeTexts("bootstrap", "database"))}
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
//
// 生命周期：由段驱动装配引擎在阶段 B（BeforeStart，契约装载校验后）
// 调用；database 段不支持热更新（连接池重建侵入性大，变更需重启生效）。
// cleanup 交还调用方，appkit 挂停机 Effect——注册在 servers 之前，逆序
// 回放保证「服务器先 drain、数据库连接最后关」。
func (r *DatabaseRegistry) Build(ctx context.Context, cfg *bootstrapv1.Database) (map[string]any, func(), error) {
	if cfg == nil {
		return nil, nil, nil // 未配置任何数据库
	}
	provs := r.Snapshot()

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
			return nil, nil, fmt.Errorf("bootstrap: database.%s present but provider not registered (import the backend contract package and MustRegister it)", sec.name)
		}
		cli, cleanup, err := p(ctx, cfg)
		if err != nil {
			rollback()
			return nil, nil, fmt.Errorf("bootstrap: build database %s: %w", sec.name, err)
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
