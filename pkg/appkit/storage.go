// storage.go 是 appkit 侧的对象存储装配胶水：契约 storage 段的 Registry
// （构造期）已迁 bootstrap（2026-09-15，见 bootstrap/storage.go），本文件
// 保留运行期装配面——WithStorageRegistry Option、buildStorages 胶水、
// Storage/Storages 访问器。
//
// 分工（构造期归 bootstrap，运行期归 appkit）：
//   - bootstrap.StorageRegistry：显式注册 Provider、段枚举、Build；
//   - appkit：BeforeStart（阶段 B，契约装载校验后）调 Build，实例存
//     a.storages；cleanup 挂停机 Effect——注册在 cache-clients 之后，
//     逆序回放：storage/metrics 先关、缓存次之、数据库最后关。
//     storage 段不支持热更新。
//
// 用法：
//
//	sr := bootstrap.NewStorageRegistry()
//	sr.MustRegister(miniocontract.Type, miniocontract.Provider) // "minio"
//	sr.MustRegister(s3contract.Type, s3contract.Provider)      // "s3"
//	app, err := appkit.FromBootstrap(cfg, appkit.WithStorageRegistry(sr), ...)
//	// Run 后取实例：
//	st := app.Storage(miniocontract.Type).(*minio.Storage)
package appkit

import (
	"context"
	"errors"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
)

// WithStorageRegistry 注入对象存储后端的契约装配注册表（显式注册
// provider，见 bootstrap.StorageRegistry）。契约 storage 段任一后端段
// 存在时，阶段 B 按段名查表构建客户端；storage 段不支持热更新。
func WithStorageRegistry(r *baldbootstrap.StorageRegistry) BootstrapOption {
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
