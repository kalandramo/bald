// Package bootstrap 实现启动装配层：把配置契约（bconf）翻译为可运行组件
// （配置源、日志、服务器、数据/缓存/存储/AI/工作流/消息代理客户端），
// 原名 bconfinit——因实际 scope 超出 conf，2026-09-05 更名 bootstrap；
// 2026-09-15 六域客户端 Registry（Database/Cache/Storage/Ai/Workflow/Broker）
// 自 pkg/appkit 迁入——「契约段 → 实例」的工厂与注册表归装配层（bootstrap），
// 实例的运行期编排（Option 桥接/Effect/停机序）归组合层（appkit）；
// 2026-09-17 服务注册 RegistrarRegistry 同因迁入（registry 契约下放为独立
// module 后，RegistrarProvider 签名不再引用主模块包，模块循环约束消失）。
//
// 职责：读契约 → 建 provider/组件 → 装配产出（Layer 层列表、Logger、Server、
// 域客户端实例表）。
// 设计原则：显式注册，不用 init() + blank import——主程序在 main() 里
// 逐个调用 [ConfigRegistry.MustRegister]，注册序即层优先级，依赖图全程可见。
//
// 归位判别（新插件初始化落点）：产物仅是「契约段 → 实例」的构造（仅依赖
// bconf/标准库/独立 module）→ 注册表放本包；产物需进入运行期编排（依赖
// 根模块包、挂 Effect/Component、存 App 字段）→ 落 pkg/appkit 的 Provider
// 形态（如 tracer/metrics 依赖 otel——这条判据正是 RegistrarRegistry 有条件
// 迁入本包的原因：registry 契约下放为独立 module 后，它对主模块包的依赖消失）。
//
// 依赖方向：
//
//	bootstrap → bootstrap/config（Store 内核）
//	bootstrap → bconf（契约）
//	bootstrap → bconfig/*（源）
//	bootstrap → registry（服务注册契约，零依赖；仅 registrar.go 引用）
//	bconfig/* → 零契约依赖（源层保持纯净）
package bootstrap

import (
	"context"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bootstrap/config"
)

// ConfigProvider 是配置源工厂：从契约顶层结构读取一个子配置，构造并返回一个命名配置层。
//
// 返回值语义：
//   - layer 为 nil 表示「该源未在契约中配置」，[ConfigRegistry.Build] 阶段跳过（非错误）；
//   - layer.Reader 应为整文档源（Load(ctx, "") 返回整份配置文档），实现
//     bconfig.ValueWatcher 时可参与 Store 热更新（layer.Watch 置 true）；
//   - cleanup 释放 provider 持有的资源（含 reader 本身，Store 不重复关闭），可为 nil；
//   - 出错返回 error，Build 短路并回滚已构造的源。
//
// 与 go-wind ConfigAction（返回无参 func() 清理闭包、实例藏于包级全局变量）不同，
// ConfigProvider 返回可见的 layer，支持多实例，cleanup 语义与 bconfig.Closer 对齐。
type ConfigProvider func(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error)

// ConfigRegistry 按名字注册配置源工厂，并维护注册序作为默认级联优先级。
//
// 与 go-wind 的全局 map + init() 自注册不同，ConfigRegistry 是显式实例：
// 每个测试、每个应用可持有独立的 ConfigRegistry，装配什么、以什么顺序，全部写在调用方代码里。
//
// 薄壳（批 1 收敛，见 registry.go）：provider 表与空名/nil/重名校验由嵌入的
// Registry[T] 载体承载（文案经 NameTexts 与既有文案逐字一致）；本类型只保留
// 特有的注册序 order（默认级联优先级）及其锁。
type ConfigRegistry struct {
	*Registry[ConfigProvider]
	mu    sync.RWMutex // 只保护 order（provider 表的锁在载体里）
	order []string     // 注册序 = 默认级联优先级（先注册者优先级高）
}

// NewConfigRegistry 创建一个空的 [ConfigRegistry]。
func NewConfigRegistry() *ConfigRegistry {
	return &ConfigRegistry{Registry: NewRegistry[ConfigProvider](NameTexts("bootstrap", ""))}
}

// Register 注册一个配置源工厂。
//
// name 是源的逻辑名（如 "file" / "env"），仅用于错误信息与调试，不参与装配语义。
// 重名报错、不覆盖（fail-fast）：同一 ConfigRegistry 里多次注册同名 provider 是程序 bug。
//
// 校验与存储由载体 Register 承担；注册成功后在此追加注册序——order 与 provider
// 表分属两把锁（先载体后 order，snapshot 同序），注册序 = 成功注册的完成序
// （main 装配期顺序注册，语义与原单锁实现一致）。
func (r *ConfigRegistry) Register(name string, p ConfigProvider) error {
	if err := r.Registry.Register(name, p); err != nil {
		return err
	}
	r.mu.Lock()
	r.order = append(r.order, name)
	r.mu.Unlock()
	return nil
}

// MustRegister 是 [ConfigRegistry.Register] 的 panic 版本，仅用于主程序 main() 内显式注册。
// 绝不在任何包的 init() 里使用——这是与 go-wind 自注册模式的根本区别。
// 显式保留覆盖实现（不透传载体 MustRegister）：必须走上面的 Register 才能同步维护注册序。
func (r *ConfigRegistry) MustRegister(name string, p ConfigProvider) {
	if err := r.Register(name, p); err != nil {
		panic(err)
	}
}

// snapshot 返回注册序与 provider 的只读快照，使 Build 不长时间持锁。
func (r *ConfigRegistry) snapshot() ([]string, map[string]ConfigProvider) {
	r.mu.RLock()
	names := make([]string, len(r.order))
	copy(names, r.order)
	r.mu.RUnlock()
	return names, r.Snapshot()
}
