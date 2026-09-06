// Package etcd 提供 etcd 后端的服务注册/发现实现（Registrar + Discovery）。
//
// 双模式构造（对齐 bconfig provider 约定）：
//   - New(opts...)：按 options 自建 clientv3.Client 并拥有其生命周期
//     （Close 时关闭）——契约装配路径；
//   - NewWithClient(c, opts...)：注入既有 client，不负责关闭——复用路径
//     （应用内已有 etcd 连接可共享）。
//
// 契约装配走子包 contract（bootstrapv1.Registry → options 映射 + Provider）。
// 注册生命周期由 appkit 驱动：start 后 Register、stopAll 前 Deregister、
// Effect 回放时 Close。heartBeat 断链自动重试（指数退避，上限 maxRetry）。
//
// 移植自 go-wind-plugins/registry/etcd，实例类型换用 registry.ServiceInstance。
package etcd

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	registry "github.com/kalandramo/bald/pkg/registry"
)

var (
	_ registry.Registrar = (*Registry)(nil)
	_ registry.Discovery = (*Registry)(nil)
)

// Registry 是 etcd 后端的注册中心实现。
type Registry struct {
	opts     options
	client   *clientv3.Client
	kv       clientv3.KV
	lease    clientv3.Lease
	owned    bool // 是否拥有 client（New 自建时为 true，Close 时负责关闭）
	mu       sync.Mutex
	hbCancel context.CancelFunc

	closeOnce  sync.Once
	stopCtx    context.Context // Registry 级 ctx：Close 取消，heartBeat 随之退出
	stopCancel context.CancelFunc
}

// New 按 options 自建 etcd client 并构造 Registry（拥有 client 生命周期）。
func New(opts ...Option) (*Registry, error) {
	op := newOptions(opts...)
	if len(op.endpoints) == 0 {
		return nil, errors.New("etcd: endpoints is required")
	}
	cfg := clientv3.Config{
		Endpoints:   op.endpoints,
		DialTimeout: op.dialTimeout,
	}
	if op.username != "" {
		cfg.Username = op.username
	}
	if op.password != "" {
		cfg.Password = op.password
	}
	client, err := clientv3.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("etcd: create client: %w", err)
	}
	return newRegistry(client, op, true), nil
}

// NewWithClient 注入既有 etcd client 构造 Registry（不负责 client 关闭）。
func NewWithClient(c *clientv3.Client, opts ...Option) (*Registry, error) {
	if c == nil {
		return nil, errors.New("etcd: client is nil")
	}
	return newRegistry(c, newOptions(opts...), false), nil
}

func newRegistry(c *clientv3.Client, op options, owned bool) *Registry {
	r := &Registry{opts: op, client: c, kv: clientv3.NewKV(c), owned: owned}
	r.stopCtx, r.stopCancel = context.WithCancel(op.ctx)
	return r
}

// Close 释放 Registry 资源：取消心跳级 ctx，自建模式下关闭 client。
// 幂等，可安全在停机 Effect 与测试 cleanup 中重复调用。
func (r *Registry) Close() error {
	var err error
	r.closeOnce.Do(func() {
		r.stopCancel()
		r.mu.Lock()
		cancel := r.hbCancel
		r.hbCancel = nil
		r.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if r.owned {
			err = r.client.Close()
		}
	})
	return err
}

// Register 注册一个实例：Grant 租约 + Put（heartBeat 后台续约保活）。
func (r *Registry) Register(ctx context.Context, service *registry.ServiceInstance) error {
	value, err := marshal(service)
	if err != nil {
		return err
	}

	key := fmt.Sprintf("%s/%s/%s", r.opts.namespace, service.Name, service.ID)

	// create new lease locally first
	newLease := clientv3.NewLease(r.client)

	// swap leases under lock
	r.mu.Lock()
	oldLease := r.lease
	oldCancel := r.hbCancel
	r.lease = newLease
	r.hbCancel = nil
	r.mu.Unlock()

	// cancel old heartbeat and close old lease after swap
	if oldCancel != nil {
		oldCancel()
	}
	if oldLease != nil {
		_ = oldLease.Close()
	}

	// try to register with KV using ctx
	leaseID, err := r.registerWithKV(ctx, key, value)
	if err != nil {
		// rollback: restore old lease and close the new one
		r.mu.Lock()
		r.lease = oldLease
		r.mu.Unlock()

		_ = newLease.Close()
		return err
	}

	// start heartbeat with registry-level context (long-living) and save cancel
	hbCtx, hbCancel := context.WithCancel(r.stopCtx)
	r.mu.Lock()
	r.hbCancel = hbCancel
	r.mu.Unlock()
	go r.heartBeat(hbCtx, leaseID, key, value)

	return nil
}

// Deregister 注销实例并停掉心跳/租约（appkit 优雅停机时先于 servers.Stop 调用）。
func (r *Registry) Deregister(ctx context.Context, service *registry.ServiceInstance) error {
	// remove kv first
	key := fmt.Sprintf("%s/%s/%s", r.opts.namespace, service.Name, service.ID)
	_, err := r.client.Delete(ctx, key)
	if err != nil {
		return wrapConnError("delete key", key, err)
	}

	// stop heartbeat and close lease safely
	r.mu.Lock()
	cancel := r.hbCancel
	l := r.lease
	r.hbCancel = nil
	r.lease = nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if l != nil {
		_ = l.Close()
	}
	return err
}

// GetService 返回服务名下的全部实例（前缀扫描 + JSON 解码）。
func (r *Registry) GetService(ctx context.Context, name string) ([]*registry.ServiceInstance, error) {
	key := fmt.Sprintf("%s/%s", r.opts.namespace, name)

	resp, err := r.kv.Get(ctx, key, clientv3.WithPrefix())
	if err != nil {
		return nil, wrapConnError("get key prefix", key, err)
	}

	items := make([]*registry.ServiceInstance, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		si, err := unmarshal(kv.Value)
		if err != nil {
			return nil, err
		}
		if si.Name != name {
			continue
		}
		items = append(items, si)
	}
	return items, nil
}

// Watch 订阅某服务名前缀的实例变更。
func (r *Registry) Watch(ctx context.Context, name string) (registry.Watcher, error) {
	key := fmt.Sprintf("%s/%s", r.opts.namespace, name)
	w, err := newWatcher(ctx, key, name, r.client)
	if err != nil {
		return nil, wrapConnError("create watcher", key, err)
	}
	return w, nil
}

// registerWithKV 基于当前租约句柄 Grant + Put，返回 leaseID。
func (r *Registry) registerWithKV(ctx context.Context, key string, value string) (clientv3.LeaseID, error) {
	r.mu.Lock()
	l := r.lease
	r.mu.Unlock()

	if l == nil {
		return 0, errNoLease
	}

	grant, err := l.Grant(ctx, int64(r.opts.ttl.Seconds()))
	if err != nil {
		return 0, wrapConnError("grant lease", "", err)
	}

	_, err = r.kv.Put(ctx, key, value, clientv3.WithLease(grant.ID))
	if err != nil {
		return 0, wrapConnError("put key", key, err)
	}

	return grant.ID, nil
}

// heartBeat 循环消费 KeepAlive 响应；断链后指数退避重试重注册（上限 maxRetry）。
func (r *Registry) heartBeat(ctx context.Context, leaseID clientv3.LeaseID, key string, value string) {
	curLeaseID := leaseID
	kac, err := r.client.KeepAlive(ctx, leaseID)
	if err != nil {
		curLeaseID = 0
	}

	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))

	for {
		if curLeaseID == 0 {
			// try to registerWithKV
			var retreat []int
			for retryCnt := 0; retryCnt < r.opts.maxRetry; retryCnt++ {
				if ctx.Err() != nil {
					return
				}
				// prevent infinite blocking
				idChan := make(chan clientv3.LeaseID, 1)
				errChan := make(chan error, 1)
				cancelCtx, cancel := context.WithCancel(ctx)
				go func() {
					defer cancel()
					id, registerErr := r.registerWithKV(cancelCtx, key, value)
					if registerErr != nil {
						errChan <- registerErr
					} else {
						idChan <- id
					}
				}()

				select {
				case <-time.After(3 * time.Second):
					cancel()
					continue
				case <-errChan:
					continue
				case curLeaseID = <-idChan:
				}

				kac, err = r.client.KeepAlive(ctx, curLeaseID)
				if err == nil {
					break
				}
				retreat = append(retreat, 1<<retryCnt)
				time.Sleep(time.Duration(retreat[rnd.Intn(len(retreat))]) * time.Second)
			}
			if _, ok := <-kac; !ok {
				// retry failed
				return
			}
		}

		select {
		case _, ok := <-kac:
			if !ok {
				if ctx.Err() != nil {
					// channel closed due to context cancel
					return
				}
				// need to retry registration
				curLeaseID = 0
				continue
			}
		case <-r.stopCtx.Done():
			return
		}
	}
}

// isConnError 判断错误是否为连接/服务不可达类。
func isConnError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	e := strings.ToLower(err.Error())
	indicators := []string{
		"connection refused", "connection reset", "no available endpoints",
		"transport is closing", "i/o timeout", "timeout", "connection timed out",
		"tls:", "connection refused", "connection reset by peer", "eof",
	}
	for _, sub := range indicators {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

// wrapConnError 为连接类错误附加更清晰的上下文。
func wrapConnError(op string, key string, err error) error {
	if err == nil {
		return nil
	}
	if isConnError(err) {
		if key == "" {
			return fmt.Errorf("etcd: %s failed (cannot reach etcd server): %w", op, err)
		}
		return fmt.Errorf("etcd: %s failed for key %s (cannot reach etcd server): %w", op, key, err)
	}
	return fmt.Errorf("etcd: %s failed: %w", op, err)
}
