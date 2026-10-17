// registry.go 承载运行期可逆挂载基座 Registry[T]（原 pkg/appkit 的导出类型，
// B5 下沉：类型与全部方法自包含、无 App 未导出状态依赖，接收者方法随类型
// 同包迁移无 Go 约束；docs/devel/zh-CN/装配层评估.md §4.4、D1 §2.6）。
//
// 设计意图（显式装配哲学）：注册表实例由调用方显式构造（NewRegistry），
// 装配期 Register 冲突报错 fail-fast，运行期变更走 Mount/Unmount 可逆语义
// （效应账本）。不做全局预置实例、不做 init() 自注册——引擎/传输/中间件的
// 接线由业务侧在装配代码里显式完成。
//
// 注：与 bootstrap 的装配期注册表载体（bootstrap/registry.go，fail-fast
// 语义）语义不同、不共用——两套泛型注册表并存是批 1 的裁定终态
// （装配层评估.md §9.6）。
package mech

import (
	"fmt"
	"sort"
	"sync"
)

// Registry 是并发安全的按名注册表。T 为被注册的实现类型。
type Registry[T any] struct {
	mu    sync.RWMutex
	items map[string]T
}

// NewRegistry 构造空注册表。
func NewRegistry[T any]() *Registry[T] {
	return &Registry[T]{items: make(map[string]T)}
}

// Register 注册一个实现；重名返回 error（不覆盖，避免静默踩踏）。
func (r *Registry[T]) Register(name string, impl T) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[name]; ok {
		return fmt.Errorf("appkit/mech: %q already registered", name)
	}
	r.items[name] = impl
	return nil
}

// MustRegister 同 Register，重名直接 panic（适合装配期显式注册场景）。
func (r *Registry[T]) MustRegister(name string, impl T) {
	if err := r.Register(name, impl); err != nil {
		panic(err)
	}
}

// Get 按名取实现，未命中返回 (零值, false)。
func (r *Registry[T]) Get(name string) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.items[name]
	return v, ok
}

// List 返回所有已注册名字（升序，便于稳定输出/测试）。
func (r *Registry[T]) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.items))
	for k := range r.items {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Mount 运行期可逆挂载：与 Register（装配期、不可逆、冲突报错）对偶。
// 挂载成功返回 undo 闭包（幂等：重复调用无副作用）；已存在同名条目（无论
// Register 还是 Mount 写入）返回错误，不做覆盖。
//
// 约定：同名字换实现应先 Unmount 旧条目再 Mount 新条目（mount/unmount 配对）。
// undo 语义为「撤销本次挂载」：若违反约定在同名新条目存在时执行旧 undo，会按名
// 移除（此时移除的是新条目）——管理面调用保持配对即可避免。
func (r *Registry[T]) Mount(name string, impl T) (undo func(), err error) {
	r.mu.Lock()
	if _, ok := r.items[name]; ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("appkit/mech: %q already registered", name)
	}
	r.items[name] = impl
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { _, _ = r.Unmount(name) })
	}, nil
}

// Unmount 按名卸载（可逆注销），返回被卸载的实例；不存在时返回零值与 false。
func (r *Registry[T]) Unmount(name string) (T, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	impl, ok := r.items[name]
	if !ok {
		var zero T
		return zero, false
	}
	delete(r.items, name)
	return impl, true
}
