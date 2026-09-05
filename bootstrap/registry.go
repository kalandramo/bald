// Package bootstrap 实现启动装配层：把配置契约（bconf）翻译为可运行组件
// （配置源、日志、服务器），原名 bconfinit——因实际 scope 覆盖三个 Registry
// （配置源/日志/服务器）而非仅 conf，2026-09-05 更名 bootstrap。
//
// 职责：读契约 → 建 provider/组件 → 装配产出（Layer 层列表、Logger、Server）。
// 设计原则：显式注册，不用 init() + blank import——主程序在 main() 里
// 逐个调用 [Registry.MustRegister]，注册序即层优先级，依赖图全程可见。
//
// 依赖方向：
//
//	bootstrap → bootstrap/config（Store 内核）
//	bootstrap → bconf（契约）
//	bootstrap → bconfig/*（源）
//	bconfig/* → 零契约依赖（源层保持纯净）
package bootstrap

import (
	"context"
	"fmt"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bootstrap/config"
)

// Provider 是配置源工厂：从契约顶层结构读取一个子配置，构造并返回一个命名配置层。
//
// 返回值语义：
//   - layer 为 nil 表示「该源未在契约中配置」，[Registry.Build] 阶段跳过（非错误）；
//   - layer.Reader 应为整文档源（Load(ctx, "") 返回整份配置文档），实现
//     bconfig.ValueWatcher 时可参与 Store 热更新（layer.Watch 置 true）；
//   - cleanup 释放 provider 持有的资源（含 reader 本身，Store 不重复关闭），可为 nil；
//   - 出错返回 error，Build 短路并回滚已构造的源。
//
// 与 go-wind ConfigAction（返回无参 func() 清理闭包、实例藏于包级全局变量）不同，
// Provider 返回可见的 layer，支持多实例，cleanup 语义与 bconfig.Closer 对齐。
type Provider func(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error)

// Registry 按名字注册配置源工厂，并维护注册序作为默认级联优先级。
//
// 与 go-wind 的全局 map + init() 自注册不同，Registry 是显式实例：
// 每个测试、每个应用可持有独立的 Registry，装配什么、以什么顺序，全部写在调用方代码里。
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	order     []string // 注册序 = 默认级联优先级（先注册者优先级高）
}

// NewRegistry 创建一个空的 [Registry]。
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register 注册一个配置源工厂。
//
// name 是源的逻辑名（如 "file" / "env"），仅用于错误信息与调试，不参与装配语义。
// 重名报错、不覆盖（fail-fast）：同一 Registry 里多次注册同名 provider 是程序 bug。
func (r *Registry) Register(name string, p Provider) error {
	if name == "" {
		return fmt.Errorf("bootstrap: provider name is empty")
	}
	if p == nil {
		return fmt.Errorf("bootstrap: provider %q is nil", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[name]; ok {
		return fmt.Errorf("bootstrap: provider %q already registered", name)
	}
	r.providers[name] = p
	r.order = append(r.order, name)
	return nil
}

// MustRegister 是 [Registry.Register] 的 panic 版本，仅用于主程序 main() 内显式注册。
// 绝不在任何包的 init() 里使用——这是与 go-wind 自注册模式的根本区别。
func (r *Registry) MustRegister(name string, p Provider) {
	if err := r.Register(name, p); err != nil {
		panic(err)
	}
}

// snapshot 返回注册序与 provider 的只读快照，使 Build 不长时间持锁。
func (r *Registry) snapshot() ([]string, map[string]Provider) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, len(r.order))
	copy(names, r.order)
	providers := make(map[string]Provider, len(r.providers))
	for k, v := range r.providers {
		providers[k] = v
	}
	return names, providers
}
