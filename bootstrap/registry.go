// registry.go 提供各域 Provider 注册表的泛型载体 Registry[T]——装配期
// 「按名注册 + fail-fast」的单一实现。
//
// bootstrap 十份
// （Ai/Broker/Cache/Config/Database/Workflow/Storage/Log/Server/Registrar）与
// pkg/appkit 三份（Tracer/Metrics/Audit）注册表共用同一套样板——互斥锁 +
// providers map + Register/MustRegister 的空名/nil/重名校验——统一嵌入本载体，
// 各自特有的 Build/段逻辑不变。错误文案经 RegistryTexts 在构造时注入，与既有
// 各表的逐字文案对齐（零行为变化）；现存三种文案族见 TypeTexts / NameTexts。
//
// 并发纪律与既有各表一致：Register/MustRegister 写安全（互斥锁）；Build 路径
// 经 Get / Snapshot 在锁内取值或拷贝、锁外调用 provider；注册表实例的装配期
// 使用约定（main 装配期注册）不变。
//
// 注：pkg/appkit 另有机制层 Registry[T]（mount.go 运行期可逆挂载 Mount/Unmount
// 的基座，直引 mu/items 字段）——可逆挂载与装配期 fail-fast 语义不同，不并入
// 本载体；其收敛归机制层任务。
//
// 设计原则（显式装配哲学，与本包 ConfigRegistry 注释一致）：显式注册，
// 不用 init() + blank import——主程序在 main() 里逐个 MustRegister，
// 绝不在任何包的 init() 里使用。
package bootstrap

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// RegistryTexts 是 Register 的三条 fail-fast 错误文案（空名 / nil provider /
// 重名），由 TypeTexts 或 NameTexts 构造，与既有各 Registry 的文案逐字对齐。
type RegistryTexts struct {
	empty func(name string) error
	nilP  func(name string) error
	dup   func(name string) error
}

// TypeTexts 返回「<prefix>: <kind> …」文案族——bootstrap 七域
// （ai/broker/cache/database/workflow/storage/registrar）与 appkit 三表
// （tracer/metrics/audit）共十表的现状文案：
//
//	空名： "<prefix>: <kind> type is empty"
//	nil：  "<prefix>: <kind> provider for %q is nil"
//	重名： "<prefix>: <kind> provider %q already registered"
func TypeTexts(prefix, kind string) RegistryTexts {
	return RegistryTexts{
		empty: func(string) error { return errors.New(prefix + ": " + kind + " type is empty") },
		nilP: func(name string) error {
			return fmt.Errorf("%s: %s provider for %q is nil", prefix, kind, name)
		},
		dup: func(name string) error {
			return fmt.Errorf("%s: %s provider %q already registered", prefix, kind, name)
		},
	}
}

// NameTexts 返回「provider name」文案族——log/server/config 三表的现状文案：
//
//	空名： "<prefix>: [<kind> ]provider name is empty"
//	nil：  "<prefix>: [<kind> ]provider %q is nil"
//	重名： "<prefix>: [<kind> ]provider %q already registered"
//
// kind 为空串时省略 kind 词（config 现状："bootstrap: provider %q already registered"）。
func NameTexts(prefix, kind string) RegistryTexts {
	p := prefix + ": "
	if kind != "" {
		p += kind + " "
	}
	return RegistryTexts{
		empty: func(string) error { return fmt.Errorf("%sprovider name is empty", p) },
		nilP:  func(name string) error { return fmt.Errorf("%sprovider %q is nil", p, name) },
		dup:   func(name string) error { return fmt.Errorf("%sprovider %q already registered", p, name) },
	}
}

// Registry 是各域 Provider 注册表的泛型载体：互斥锁 + 按名 provider 表 +
// Register/MustRegister/Get/Snapshot。重名报错、不覆盖（fail-fast，防静默踩踏）；
// 空名/nil provider/重名的错误文案由构造时的 RegistryTexts 决定，与既有各
// Registry 的文案逐字一致。
type Registry[T any] struct {
	mu        sync.Mutex
	providers map[string]T
	texts     RegistryTexts
}

// NewRegistry 构造空注册表。texts 经 TypeTexts / NameTexts 构造，与该域
// 既有错误文案逐字对齐。
func NewRegistry[T any](texts RegistryTexts) *Registry[T] {
	return &Registry[T]{providers: make(map[string]T), texts: texts}
}

// Register 注册一个 provider；空名/nil provider/重名 fail-fast（不覆盖）。
func (r *Registry[T]) Register(name string, p T) error {
	if name == "" {
		return r.texts.empty(name)
	}
	if isNilValue(p) {
		return r.texts.nilP(name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]T)
	}
	if _, dup := r.providers[name]; dup {
		return r.texts.dup(name)
	}
	r.providers[name] = p
	return nil
}

// MustRegister 是 Register 的 panic 版本（装配期错误应当炸在启动时），仅用于
// 主程序 main() 内显式注册，绝不在任何包的 init() 里使用。
func (r *Registry[T]) MustRegister(name string, p T) {
	if err := r.Register(name, p); err != nil {
		panic(err)
	}
}

// Get 按名取 provider，未命中返回 (零值, false)。
func (r *Registry[T]) Get(name string) (T, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.providers[name]
	return p, ok
}

// Snapshot 返回 providers 的拷贝——Build 路径先拷贝再出锁调用 provider，
// 与既有各表「锁内拷贝、锁外构建」的纪律一致。
func (r *Registry[T]) Snapshot() map[string]T {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]T, len(r.providers))
	for k, v := range r.providers {
		out[k] = v
	}
	return out
}

// isNilValue 复现「p == nil」对 nilable 类型（本仓全部 provider 均为函数类型）
// 的判定语义；非 nilable 类型恒为 false（类型参数不可直接与 nil 比较，经
// reflect 分派，仅在装配期注册路径调用）。
func isNilValue[T any](v T) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}
