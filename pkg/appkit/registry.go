// Package appkit 的泛型注册表：装配期 Register 与运行期 Mount/Unmount
// （mount.go，A1 运行期可逆挂载）的公共基座。
//
// 设计意图（显式装配哲学）：注册表实例由调用方显式构造（NewRegistry），
// 装配期 Register 冲突报错 fail-fast，运行期变更走 Mount/Unmount 可逆语义
// （效应账本）。不做全局预置实例、不做 init() 自注册——引擎/传输/中间件的
// 接线由业务侧在装配代码里显式完成（参见 bootstrap 三 Registry 与本包
// 各 XxxRegistry 的手写装配）。
package appkit

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
		return fmt.Errorf("appkit: %q already registered", name)
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
