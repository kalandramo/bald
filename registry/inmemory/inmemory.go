// Package inmemory 提供一个内存版 registry.Registry 实现（注册 + 发现 + 变更
// 监听），适用于单实例开发、测试与本地调试，不依赖任何外部组件。
package inmemory

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/kalandramo/bald/registry"
)

// 编译期锁定契约：内置实现必须两侧都齐（注册 + 发现），否则「会注册不会解析」
// 会在框架默认路径上重演。
var (
	_ registry.Registrar = (*Registrar)(nil)
	_ registry.Discovery = (*Registrar)(nil)
)

// watcherErrClosed 在 Stop 之后由 Next 返回（调用方据此退出循环）。
var errWatcherClosed = errors.New("registry/inmemory: watcher closed")

// Registrar 是内存服务注册中心，线程安全，同时实现 Discovery。
type Registrar struct {
	mu        sync.RWMutex
	instances map[string]*registry.ServiceInstance
	watchers  map[string]map[*watcher]struct{}
}

// New 构造内存 Registrar。
func New() *Registrar {
	return &Registrar{
		instances: make(map[string]*registry.ServiceInstance),
		watchers:  make(map[string]map[*watcher]struct{}),
	}
}

// Register 保存实例（按 ID 覆盖）并推送该服务名的新快照。
func (r *Registrar) Register(_ context.Context, instance *registry.ServiceInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.instances[instance.ID] = instance
	r.notifyLocked(instance.Name)
	return nil
}

// Deregister 删除实例（优雅停机时调用，避免流量打到已停服务）并推送新快照。
func (r *Registrar) Deregister(_ context.Context, instance *registry.ServiceInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.instances, instance.ID)
	r.notifyLocked(instance.Name)
	return nil
}

// List 返回当前所有已注册实例（调试/测试用）。
func (r *Registrar) List() []*registry.ServiceInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshotLocked("")
}

// GetService 返回该服务名下的全部实例；name 为空返回全部（与 List 同语义）。
func (r *Registrar) GetService(_ context.Context, name string) ([]*registry.ServiceInstance, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshotLocked(name), nil
}

// Watch 订阅某服务名的实例变更。返回的 watcher 立刻带上当前快照，之后每次
// Register/Deregister 影响该服务名时收到最新全量列表。
func (r *Registrar) Watch(_ context.Context, name string) (registry.Watcher, error) {
	w := &watcher{
		reg:     r,
		name:    name,
		updates: make(chan []*registry.ServiceInstance, 1),
		closed:  make(chan struct{}),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watchers[name] == nil {
		r.watchers[name] = make(map[*watcher]struct{})
	}
	r.watchers[name][w] = struct{}{}
	w.pushLocked(r.snapshotLocked(name))
	return w, nil
}

// snapshotLocked 返回按服务名过滤后的实例副本，按 ID 定序（map 遍历无序，
// 调用方要用下标切主副本，顺序不稳会导致每次变更都抖动）。
func (r *Registrar) snapshotLocked(name string) []*registry.ServiceInstance {
	out := make([]*registry.ServiceInstance, 0, len(r.instances))
	for _, v := range r.instances {
		if name == "" || v.Name == name {
			out = append(out, v)
		}
	}
	sortInstances(out)
	return out
}

// notifyLocked 把新快照投给该服务名的所有 watcher；必须在持有 r.mu 写锁时调用。
// 队列容量 1 且有 drain：只保证「最新状态」送达，不保证中间态都送达——对
// 端点解析这类幂等订阅是对的取舍。
func (r *Registrar) notifyLocked(name string) {
	snap := r.snapshotLocked(name)
	for w := range r.watchers[name] {
		w.pushLocked(snap)
	}
}

// watcher 是 Registrar 的订阅句柄。
type watcher struct {
	reg     *Registrar
	name    string
	updates chan []*registry.ServiceInstance
	closed  chan struct{}

	closeOnce sync.Once
}

// pushLocked 投入最新快照，先丢弃积压的旧快照，保证读到的一定是最新。
func (w *watcher) pushLocked(snap []*registry.ServiceInstance) {
	for {
		select {
		case <-w.updates:
			continue
		default:
		}
		select {
		case w.updates <- snap:
		default:
		}
		return
	}
}

// Next 阻塞直到该服务名发生变更，返回最新全量实例列表。
func (w *watcher) Next(ctx context.Context) ([]*registry.ServiceInstance, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case snap := <-w.updates:
		return snap, nil
	case <-w.closed:
		return nil, errWatcherClosed
	}
}

// Stop 注销订阅，重复调用安全。
func (w *watcher) Stop() error {
	w.closeOnce.Do(func() {
		w.reg.mu.Lock()
		defer w.reg.mu.Unlock()
		if ws, ok := w.reg.watchers[w.name]; ok {
			delete(ws, w)
			if len(ws) == 0 {
				delete(w.reg.watchers, w.name)
			}
		}
		close(w.closed)
	})
	return nil
}

func sortInstances(list []*registry.ServiceInstance) {
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
}
