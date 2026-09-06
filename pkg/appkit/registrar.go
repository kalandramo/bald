// registrar.go 实现服务注册中心的契约装配注册表（bootstrapv1.Registry 段
// → 具体后端 Registrar 实例）。
//
// 位置说明：必须放 appkit（主模块）——Provider 签名引用 pkg/registry 的
// Registrar（主模块），若放 bootstrap module 会形成
// bald⇄bald/bootstrap 的模块循环（appkit import bootstrap module）。
//
// 设计原则（对齐 bootstrap 包的 config Registry）：**显式注册**——不用
// init() + blank import，主程序在 main() 里逐个调用 MustRegister；未
// import 的后端零依赖零编译成本，运行期 fail-fast 报错，依赖图全程可见。
//
// 与 config Registry 的两点差异：
//  1. 按契约 registry.type **单选**分发（type 字段的契约语义就是单选），
//     不做 optional 子消息迭代多选（go-wind resolveRegistry 的做法——
//     多注册中心 fan-out 无消费方，语义空洞）；
//  2. Provider 必须返回注册中心**实例**（而非仅 cleanup）——真正的
//     Register/Deregister 生命周期由 appkit 驱动（start 后注册、stopAll
//     前反注册），这是 go-wind RegistryAction（实例被 `_ = reg` 丢弃）
//     缺失的一环。cleanup 只负责 client 生命周期，停机 Effect 回放。
//
// 用法：
//
//	rr := appkit.NewRegistrarRegistry()
//	rr.MustRegister(etcdcontract.Type, etcdcontract.Provider)
//	rr.MustRegister(nacoscontract.Type, nacoscontract.Provider)
//	app, err := appkit.FromBootstrap(cfg, appkit.WithRegistrarRegistry(rr), ...)
package appkit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/pkg/registry"
)

// RegistrarProvider 按契约 Registry 段构造具体后端。
// 返回 (实例, cleanup, error)：cleanup 释放 client/连接资源（可为 nil），
// 由 FromBootstrap 在停机 Effect 中回放（Deregister 先于它，顺序安全）。
// 各后端的实现见 contrib/registry/<backend>/contract 包。
type RegistrarProvider func(ctx context.Context, cfg *bootstrapv1.Registry) (registry.Registrar, func(), error)

// RegistrarRegistry 是服务注册中心 Provider 的显式注册表。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
type RegistrarRegistry struct {
	mu        sync.Mutex
	providers map[string]RegistrarProvider
}

// NewRegistrarRegistry 创建空注册表。
func NewRegistrarRegistry() *RegistrarRegistry {
	return &RegistrarRegistry{providers: make(map[string]RegistrarProvider)}
}

// Register 注册一个后端 Provider；重名/空名/nil provider 均 fail-fast。
func (r *RegistrarRegistry) Register(typ string, p RegistrarProvider) error {
	if typ == "" {
		return errors.New("appkit: registrar type is empty")
	}
	if p == nil {
		return fmt.Errorf("appkit: registrar provider for %q is nil", typ)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]RegistrarProvider)
	}
	if _, dup := r.providers[typ]; dup {
		return fmt.Errorf("appkit: registrar provider %q already registered", typ)
	}
	r.providers[typ] = p
	return nil
}

// MustRegister 注册后端 Provider，失败 panic（装配期错误应当炸在启动时）。
func (r *RegistrarRegistry) MustRegister(typ string, p RegistrarProvider) {
	if err := r.Register(typ, p); err != nil {
		panic(err)
	}
}

// Build 按契约 registry.type 单选分发到已注册的 Provider。
// 段为 nil/type 为空/type 未注册均 fail-fast——显式注册的天然收益：
// 未 import 的后端在这里报错，而不是静默无注册。
func (r *RegistrarRegistry) Build(ctx context.Context, cfg *bootstrapv1.Registry) (registry.Registrar, func(), error) {
	if cfg == nil {
		return nil, nil, errors.New("appkit: registry section is nil")
	}
	typ := cfg.GetType()
	if typ == "" {
		return nil, nil, errors.New("appkit: registry.type is empty")
	}
	r.mu.Lock()
	p, ok := r.providers[typ]
	r.mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("appkit: registrar provider %q not registered (import the backend contract package and MustRegister it)", typ)
	}
	return p(ctx, cfg)
}
