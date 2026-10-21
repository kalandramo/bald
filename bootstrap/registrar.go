// registrar.go 实现服务注册中心的契约装配注册表（bootstrapv1.Registry 段
// → 具体后端 Registrar 实例）——与 Database/Cache/Storage/Ai/Workflow/Broker
// 六域 Registry 同族的第七个域注册表。
//
// 位置沿革：2026-09-17 之前本文件必须放 pkg/appkit——Provider 签名引用主模块的
// pkg/registry.Registrar，放 bootstrap module 会形成 bald⇄bald/bootstrap 的
// 模块循环（appkit import bootstrap module）。registry 契约下放为独立 module
// （零依赖）后该约束消失，本文件随之迁入 bootstrap：产物是「契约段 → 实例」的
// 构造，只依赖 bconf + registry 两个独立 module，命中本包归位判别（见
// bootstrap/registry.go 包注释）；实例的运行期编排（start 后 Register、
// stopAll 前 Deregister、cleanup 挂 Effect 回放）仍归组合层 appkit。
//
// 设计原则（对齐本包 ConfigRegistry）：**显式注册**——不用 init() + blank import，
// 主程序在 main() 里逐个调用 MustRegister；未 import 的后端零依赖零编译成本，
// 运行期 fail-fast 报错，依赖图全程可见。
//
// 与 ConfigRegistry 的两点差异：
//  1. 按契约 registry.type **单选**分发（type 字段的契约语义就是单选），
//     不做 optional 子消息迭代多选（go-wind resolveRegistry 的做法——
//     多注册中心 fan-out 无消费方，语义空洞）；
//  2. Provider 必须返回注册中心**实例**（而非仅 cleanup）——真正的
//     Register/Deregister 生命周期由 appkit 驱动（start 后注册、stopAll
//     前反注册），这是 go-wind RegistryAction（实例被 `_ = reg` 丢弃）
//     缺失的一环。cleanup 只负责 client 生命周期，停机 Effect 回放。
//
// 用法（经 registry 段 Provider 接入段驱动装配——Provider 等价形态见
// _example/bald/register_nacos.go 的 nacosRegistrarProvider）：
//
//	rr := bootstrap.NewRegistrarRegistry()
//	rr.MustRegister(etcdcontract.Type, etcdcontract.Provider)
//	appkit.MustServe(ctx, std.Standard(), registrarSegmentProvider(rr), ...)
package bootstrap

import (
	"context"
	"errors"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/registry"
)

// RegistrarProvider 按契约 Registry 段构造具体后端。
// 返回 (实例, cleanup, error)：cleanup 释放 client/连接资源（可为 nil），
// 由 appkit 在停机 Effect 中回放（Deregister 先于它，顺序安全）。
// 各后端的实现见 registry/<backend>/contract 包。
type RegistrarProvider func(ctx context.Context, cfg *bootstrapv1.Registry) (registry.Registrar, func(), error)

// RegistrarRegistry 是服务注册中心 Provider 的显式注册表。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
//
// 与 ConfigRegistry 的差异：不做多源级联（registry 无优先级语义），
// 按契约 registry.type 单选分发。
// 薄壳（见 registry.go）：互斥锁 + provider 表 + Register/MustRegister
// 由嵌入的 Registry[T] 载体承载（文案经 TypeTexts 与既有文案逐字一致）；
// 本文件只保留 registrar 特有的单选分发 Build。
type RegistrarRegistry struct {
	*Registry[RegistrarProvider]
}

// NewRegistrarRegistry 创建空注册表。
func NewRegistrarRegistry() *RegistrarRegistry {
	return &RegistrarRegistry{Registry: NewRegistry[RegistrarProvider](TypeTexts("bootstrap", "registrar"))}
}

// Build 按契约 registry.type 单选分发到已注册的 Provider。
// 段为 nil/type 为空/type 未注册均 fail-fast——显式注册的天然收益：
// 未 import 的后端在这里报错，而不是静默无注册。
//
// 生命周期：由段驱动装配引擎在阶段 B（BeforeStart，契约装载校验后）
// 调用；registry 段不支持热更新（client 重建侵入性大，变更需重启生效）。
// cleanup 交还调用方，appkit 挂停机 Effect appkit:registrar-client 释放。
func (r *RegistrarRegistry) Build(ctx context.Context, cfg *bootstrapv1.Registry) (registry.Registrar, func(), error) {
	if cfg == nil {
		return nil, nil, errors.New("bootstrap: registry section is nil")
	}
	typ := cfg.GetType()
	if typ == "" {
		return nil, nil, errors.New("bootstrap: registry.type is empty")
	}
	p, ok := r.Get(typ)
	if !ok {
		return nil, nil, fmt.Errorf("bootstrap: registrar provider %q not registered (import the backend contract package and MustRegister it)", typ)
	}
	return p(ctx, cfg)
}
