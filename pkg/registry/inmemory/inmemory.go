// Package inmemory 提供一个内存版 registry.Registrar 实现，
// 适用于单实例开发、测试与本地调试，不依赖任何外部组件。
package inmemory

import (
	"context"
	"sync"

	"github.com/kalandramo/bald/pkg/registry"
)

// Registrar 是内存服务注册中心，线程安全。
type Registrar struct {
	mu        sync.RWMutex
	instances map[string]*registry.ServiceInstance
}

// New 构造内存 Registrar。
func New() *Registrar {
	return &Registrar{instances: make(map[string]*registry.ServiceInstance)}
}

// Register 保存实例（按 ID 覆盖）。
func (r *Registrar) Register(_ context.Context, instance *registry.ServiceInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.instances[instance.ID] = instance
	return nil
}

// Deregister 删除实例。
func (r *Registrar) Deregister(_ context.Context, instance *registry.ServiceInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.instances, instance.ID)
	return nil
}

// List 返回当前所有已注册实例（调试/测试用）。
func (r *Registrar) List() []*registry.ServiceInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*registry.ServiceInstance, 0, len(r.instances))
	for _, v := range r.instances {
		out = append(out, v)
	}
	return out
}
