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
// 实例类型为 registry.ServiceInstance。
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

	registry "github.com/kalandramo/bald/registry"
	"github.com/kalandramo/bald/retry"
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
	// 前置闸门（单一真相源，见 registry.ServiceInstance.Validate）：
	// 缺 Name/ID 会静默注册到 `<ns>//<id>` 这类畸形 key。
	if err := service.Validate(); err != nil {
		return err
	}
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

// heartBeat 循环消费 KeepAlive 响应；断链后重试重注册，次数上限 maxRetry、
// 每次之间的等待见 [retreatPolicy.delay]（默认 1s 起翻倍、截到
// defaultRetreatMax、叠 FullJitter；契约 registry.etcd.backoff 可改写曲线与抖动）。
func (r *Registry) heartBeat(ctx context.Context, leaseID clientv3.LeaseID, key string, value string) {
	curLeaseID := leaseID
	kac, err := r.client.KeepAlive(ctx, leaseID)
	if err != nil {
		curLeaseID = 0
	}

	for {
		if curLeaseID == 0 {
			// try to registerWithKV
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
				case <-ctx.Done():
					cancel()
					return
				case <-time.After(3 * time.Second):
					cancel()
					continue
				case _ = <-errChan:
					continue
				case curLeaseID = <-idChan:
				}

				kac, err = r.client.KeepAlive(ctx, curLeaseID)
				if err == nil {
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(r.opts.retreat.delay(retryCnt, rand.Float64)):
				}
			}
			if kac == nil {
				// 重试耗尽：KeepAlive 出错时返回 nil channel，直接接收会永久阻塞。
				return
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

// defaultRetreatMax 是单次重注册退避的上界（抖动前）。
//
// 曲线按 2^attempt 秒增长，而 max_retry 来自契约 registry.etcd.max_retry——
// 配置值直接决定最后一次能睡多久，实测（本机，纯算术）：
//   - attempt=9 → 8m32s；attempt=20 → 291h；attempt=33 → 272 年。
//     睡过 TTL 的几倍就是「心跳 goroutine 还活着，注册项早没了」。
//   - attempt≥34 起秒数×1e9 溢出成**负** duration，time.After 立刻返回，
//     退避反转成忙轮询——同一个配置键既能把服务睡死、又能把它打爆。
//
// 封顶到这里意味着：更大的 max_retry 只多买几次重试，不多买一次长睡。
// 契约 registry.etcd.backoff 能改写这条曲线（形状与必填口径见该消息的注释），
// 但改写不了「必须有上界」这件事——缺席时用这里的默认，写了就必须自带 max。
const defaultRetreatMax = 5 * time.Minute

// retreatPolicy 是一次重注册退避的两个自由度：增长曲线与抖动。
//
// 零值不是合法状态——[newOptions] 总会先装入 [defaultRetreatPolicy]，
// [WithRetreat] 再按项覆盖。两个自由度各自独立覆盖，契约段只写了曲线时
// 抖动仍是后端的默认形状。
type retreatPolicy struct {
	backoff retry.Backoff
	jitter  retry.Jitter
}

// defaultRetreatPolicy 是未配置时的形状：1s 起、每次翻倍、截到
// [defaultRetreatMax]，再叠 [0, d) 的 FullJitter。
//
// 换掉的是原先「retreat = [1,2,4,…,2^attempt] 秒里等概率取一档」的写法。
// 三条口径差别都写在这儿，不是实现细节：
//   - 上界：attempt≤8 时同为 2^attempt 秒（曲线未触顶），≥9 起截到 5min。
//     默认 max_retry=5（最高档 16s）落在未触顶区，行为不变。
//   - 分布：档位取样在 attempt 很大时仍有 1/(n+1) 的概率只退 1s——注册中心
//     恢复瞬间成百上千个实例一起重连，正是要靠 FullJitter 打散的那种同步风暴。
//   - 下限：不再保 1s，低 attempt 的探测更轻（rng 接近 0 时近乎立刻重试一次）。
func defaultRetreatPolicy() retreatPolicy {
	return retreatPolicy{
		backoff: retry.ExponentialBackoff{Initial: time.Second, Factor: 2, Max: defaultRetreatMax},
		jitter:  retry.FullJitter,
	}
}

// delay 是第 attempt 次重注册失败之后的等待（纯函数 + 注入 rng，为了能把
// 边界钉死而不必拿墙钟去猜）。
func (p retreatPolicy) delay(attempt int, rng func() float64) time.Duration {
	return p.jitter(p.backoff.Delay(attempt), rng)
}

// retreatDelay 是默认策略下的等待，保留成包级函数是为了让曲线与上界的判据
// 直接落在纯函数上（见 retreat_test.go）。
func retreatDelay(attempt int, rng func() float64) time.Duration {
	return defaultRetreatPolicy().delay(attempt, rng)
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
