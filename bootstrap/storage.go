package bootstrap

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// StorageProvider 按契约 Storage 段构造具体后端对象存储客户端。
// 返回 (实例, cleanup, error)：cleanup 释放后端资源（可为 nil），由
// 调用方释放（appkit 挂停机 Effect 逆序回放）。
// 各后端的实现见 oss/<backend>/contract 包。
type StorageProvider func(ctx context.Context, cfg *bootstrapv1.Storage) (any, func(), error)

// StorageRegistry 是对象存储后端 Provider 的显式注册表，键为契约段名。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
//
// 与 DatabaseRegistry 同模式：显式注册（不用 init()+blank import，主程序
// 在 main() 里逐个 MustRegister）；段存在但未注册 fail-fast。
// storage 是 optional 段集合（minio/s3 可并存），装配语义是
// 「每段各自构建、全部返回」。
// 薄壳（批 1 收敛，见 registry.go）：互斥锁 + provider 表 + Register/MustRegister
// 由嵌入的 Registry[T] 载体承载（文案经 TypeTexts 与既有文案逐字一致）；
// 本文件只保留 storage 特有的段表与 Build 装配序。
type StorageRegistry struct {
	*Registry[StorageProvider]
}

// NewStorageRegistry 创建空注册表。
func NewStorageRegistry() *StorageRegistry {
	return &StorageRegistry{Registry: NewRegistry[StorageProvider](TypeTexts("bootstrap", "storage"))}
}

// storageSection 契约段枚举：固定顺序即装配顺序（proto 字段序），
// 新后端在 bconf 加段后在此追加。
type storageSection struct {
	name   string
	exists func(*bootstrapv1.Storage) bool
}

var storageSections = []storageSection{
	{"minio", func(s *bootstrapv1.Storage) bool { return s.GetMinio() != nil }},
	{"s3", func(s *bootstrapv1.Storage) bool { return s.GetS3() != nil }},
}

// Build 按契约段装配全部已配置的对象存储客户端：段存在 → 查表构建。
// 全部段缺失为 no-op；任一段存在但未注册 Provider 均 fail-fast；
// 构建失败回滚已建实例的 cleanup（逆序）。
//
// 生命周期：由段驱动装配引擎在阶段 B（BeforeStart，契约装载校验后）
// 调用；storage 段不支持热更新（与 database 段同理）。cleanup 交还调用方，
// appkit 挂停机 Effect——注册在 cache-clients 之后，逆序回放：
// storage/metrics 先关、缓存次之、数据库最后关。
func (r *StorageRegistry) Build(ctx context.Context, cfg *bootstrapv1.Storage) (map[string]any, func(), error) {
	if cfg == nil {
		return nil, nil, nil // 未配置任何对象存储
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
	for _, sec := range storageSections {
		if !sec.exists(cfg) {
			continue // 段缺失 = 未声明该后端
		}
		p, ok := provs[sec.name]
		if !ok {
			rollback()
			return nil, nil, fmt.Errorf("bootstrap: storage.%s present but provider not registered (import the backend contract package and MustRegister it)", sec.name)
		}
		cli, cleanup, err := p(ctx, cfg)
		if err != nil {
			rollback()
			return nil, nil, fmt.Errorf("bootstrap: build storage %s: %w", sec.name, err)
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
