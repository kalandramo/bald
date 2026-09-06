// storage.go 实现对象存储客户端的契约装配注册表（bootstrapv1.Storage 段 →
// 具体后端实例）。
//
// 与 DatabaseRegistry 同模式：显式注册（不用 init()+blank import，主程序
// 在 main() 里逐个 MustRegister）；段存在但未注册 fail-fast；cleanup 挂
// 停机 Effect 逆序回放。storage 是 optional 段集合（minio/s3 可并存），
// 装配语义是「每段各自构建、全部返回」。
//
// 生命周期：阶段 B（BeforeStart，契约装载校验后）构建；storage 段不支持
// 热更新（与 database 段同理）。停机 Effect 注册在 cache-clients 之后——
// 逆序回放：storage/metrics 先关、缓存次之、数据库最后关。
//
// 用法：
//
//	sr := appkit.NewStorageRegistry()
//	sr.MustRegister(miniocontract.Type, miniocontract.Provider) // "minio"
//	sr.MustRegister(s3contract.Type, s3contract.Provider)      // "s3"
//	app, err := appkit.FromBootstrap(cfg, appkit.WithStorageRegistry(sr), ...)
//	// Run 后取实例：
//	st := app.Storage(miniocontract.Type).(*minio.Storage)
package appkit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// StorageProvider 按契约 Storage 段构造具体后端对象存储客户端。
// 返回 (实例, cleanup, error)：cleanup 释放后端资源（可为 nil），由
// FromBootstrap 在停机 Effect 中逆序回放。
// 各后端的实现见 oss/<backend>/contract 包。
type StorageProvider func(ctx context.Context, cfg *bootstrapv1.Storage) (any, func(), error)

// StorageRegistry 是对象存储后端 Provider 的显式注册表，键为契约段名。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
type StorageRegistry struct {
	mu        sync.Mutex
	providers map[string]StorageProvider
}

// NewStorageRegistry 创建空注册表。
func NewStorageRegistry() *StorageRegistry {
	return &StorageRegistry{providers: make(map[string]StorageProvider)}
}

// Register 注册一个后端 Provider；重名/空名/nil provider 均 fail-fast。
func (r *StorageRegistry) Register(typ string, p StorageProvider) error {
	if typ == "" {
		return errors.New("appkit: storage type is empty")
	}
	if p == nil {
		return fmt.Errorf("appkit: storage provider for %q is nil", typ)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]StorageProvider)
	}
	if _, dup := r.providers[typ]; dup {
		return fmt.Errorf("appkit: storage provider %q already registered", typ)
	}
	r.providers[typ] = p
	return nil
}

// MustRegister 注册后端 Provider，失败 panic（装配期错误应当炸在启动时）。
func (r *StorageRegistry) MustRegister(typ string, p StorageProvider) {
	if err := r.Register(typ, p); err != nil {
		panic(err)
	}
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
func (r *StorageRegistry) Build(ctx context.Context, cfg *bootstrapv1.Storage) (map[string]any, func(), error) {
	if cfg == nil {
		return nil, nil, nil // 未配置任何对象存储
	}
	r.mu.Lock()
	provs := make(map[string]StorageProvider, len(r.providers))
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
	for _, sec := range storageSections {
		if !sec.exists(cfg) {
			continue // 段缺失 = 未声明该后端
		}
		p, ok := provs[sec.name]
		if !ok {
			rollback()
			return nil, nil, fmt.Errorf("appkit: storage.%s present but provider not registered (import the backend contract package and MustRegister it)", sec.name)
		}
		cli, cleanup, err := p(ctx, cfg)
		if err != nil {
			rollback()
			return nil, nil, fmt.Errorf("appkit: build storage %s: %w", sec.name, err)
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

// WithStorageRegistry 注入对象存储后端的契约装配注册表（显式注册
// provider，见 StorageRegistry）。契约 storage 段任一后端段存在时，阶段 B
// 按段名查表构建客户端；storage 段不支持热更新。
func WithStorageRegistry(r *StorageRegistry) BootstrapOption {
	return func(s *bootstrapSpec) { s.storageRegistry = r }
}

// buildStorages 在阶段 B（契约装载校验后）按契约 storage 段构建全部已配置
// 后端的对象存储客户端，存入 a.storages 供业务经 Storage/Storages 取用。
// storage 段缺失为 no-op；段存在但未接 StorageRegistry 则 fail-fast。
// 返回聚合 cleanup（可为 nil），由 FromBootstrap 挂停机 Effect。
func buildStorages(a *AppKit, cfg *bootstrapv1.BootstrapConfig, spec *bootstrapSpec) (func(), error) {
	scfg := cfg.GetStorage()
	if scfg == nil {
		return nil, nil // 未配置任何对象存储
	}
	if spec.storageRegistry == nil {
		return nil, errors.New("appkit: bootstrap.storage present but no StorageRegistry wired (WithStorageRegistry missing)")
	}
	clients, cleanup, err := spec.storageRegistry.Build(context.Background(), scfg)
	if err != nil {
		return nil, err
	}
	a.storages = clients
	return cleanup, nil
}

// Storage 返回按契约段装配的后端对象存储客户端实例（阶段 B 后、Run 期
// 可用；FromBootstrap 构造期尚未装载契约，返回空）。
// typ 为契约段名（见 oss/<backend>/contract.Type，如 "minio"）。
// 并发语义：storages 只在 BeforeStart（阶段 B）单次写入，Run 后只读，
// happens-before 由 Run 启动流程保证。
func (k *AppKit) Storage(typ string) (any, bool) {
	cli, ok := k.storages[typ]
	return cli, ok
}

// Storages 返回全部已装配后端对象存储客户端（副本，段名→实例），用于
// 遍历诊断。
func (k *AppKit) Storages() map[string]any {
	out := make(map[string]any, len(k.storages))
	for typ, cli := range k.storages {
		out[typ] = cli
	}
	return out
}
