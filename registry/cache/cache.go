// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package cache 给 registry.Discovery 套一层弹性读缓存（go-micro registry cache
// 的同思路实现；代码自 onexmesh 移植，接口对齐 bald 的 Discovery 契约）。
// 在底层发现之上叠加：并发查询的 singleflight 去重、服务级 TTL 与节点级 TTL、
// 注册中心故障时的 stale-while-error 降级、以及防止缓存击穿的最小刷新间隔。
//
// 用法：cache.New(discovery, cache.WithTTL(...)) 的返回值仍是 Discovery，
// 可无感嵌进任何消费方（客户端 SDK、selector 等）。
package cache

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/kalandramo/bald/registry"
)

// defaultTTL 是一次拉取的服务快照被视为新鲜时长。
const defaultTTL = time.Minute

// defaultNodeTTL 是单个节点被视为新鲜的时长。
const defaultNodeTTL = 30 * time.Second

// defaultMinRetryInterval 限流注册中心刷新尝试，防止抖动的注册中心被并发调用方打爆。
const defaultMinRetryInterval = 5 * time.Second

// Option 配置缓存。
type Option func(*Options)

// Options 是缓存的可调参数。
type Options struct {
	TTL              time.Duration
	NodeTTL          time.Duration
	MinRetryInterval time.Duration
	// Now 是 TTL 判定用的时钟；测试注入假时钟。放在 Options（而非包级变量），
	// 并发使用不同时钟的缓存之间不会竞争。
	Now func() time.Time
}

// WithTTL 设置服务级缓存 TTL。
func WithTTL(d time.Duration) Option { return func(o *Options) { o.TTL = d } }

// WithNodeTTL 设置节点级缓存 TTL。
func WithNodeTTL(d time.Duration) Option { return func(o *Options) { o.NodeTTL = d } }

// WithMinRetryInterval 设置某服务两次注册中心刷新尝试的最小间隔。
func WithMinRetryInterval(d time.Duration) Option {
	return func(o *Options) { o.MinRetryInterval = d }
}

// WithNow 覆盖 TTL 判定用的时钟（测试用）。
func WithNow(f func() time.Time) Option { return func(o *Options) { o.Now = f } }

// cache 用读穿缓存装饰一个 Discovery。内嵌底层 Discovery 并只覆写 GetService，
// Watch 等其余方法原样透传。
type cache struct {
	registry.Discovery

	opts Options
	sg   singleflight.Group

	mu          sync.Mutex
	status      map[string]error
	cache       map[string][]*registry.ServiceInstance
	ttls        map[string]time.Time
	nttls       map[string]map[string]time.Time
	lastRefresh map[string]time.Time
}

// New 给 d 套上弹性读缓存。
func New(d registry.Discovery, opts ...Option) registry.Discovery {
	o := Options{TTL: defaultTTL, NodeTTL: defaultNodeTTL, MinRetryInterval: defaultMinRetryInterval, Now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	return &cache{
		Discovery:   d,
		opts:        o,
		status:      map[string]error{},
		cache:       map[string][]*registry.ServiceInstance{},
		ttls:        map[string]time.Time{},
		nttls:       map[string]map[string]time.Time{},
		lastRefresh: map[string]time.Time{},
	}
}

// GetService 返回 name 的缓存实例：并发调用去重，注册中心故障时降级到最近快照。
func (c *cache) GetService(ctx context.Context, name string) ([]*registry.ServiceInstance, error) {
	// WithoutCancel 剥掉首调者的 deadline/cancellation——短命或已取消的首调者
	// 不应连坐失败所有并发等待者；拉取仍携带调用方的 values（trace id 等）。
	v, err, _ := c.sg.Do(name, func() (any, error) {
		return c.fetch(context.WithoutCancel(ctx), name)
	})
	if err != nil {
		return nil, err
	}
	return v.([]*registry.ServiceInstance), nil
}

// fetch 执行读穿逻辑。调用方不得持锁。
func (c *cache) fetch(ctx context.Context, name string) ([]*registry.ServiceInstance, error) {
	c.mu.Lock()
	if c.isValidLocked(name) {
		insts := c.pruneExpiredNodesLocked(name)
		c.mu.Unlock()
		return insts, nil
	}
	// 限流刷新尝试：重试间隔内如有旧数据则先给旧数据——冷注册中心不会被
	// 一波相同的查询打爆。
	if c.isThrottledLocked(name) {
		insts := c.pruneExpiredNodesLocked(name)
		c.mu.Unlock()
		return insts, nil
	}
	c.mu.Unlock()

	insts, err := c.Discovery.GetService(ctx, name)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastRefresh[name] = c.opts.Now()
	if err != nil {
		c.status[name] = err
		// 有旧数据则降级；没有才把错误透出。
		if len(c.cache[name]) > 0 {
			return cloneInstances(c.cache[name]), nil
		}
		return nil, err
	}

	delete(c.status, name)
	c.cache[name] = insts
	c.ttls[name] = c.opts.Now().Add(c.opts.TTL)
	nttls := make(map[string]time.Time, len(insts))
	for _, inst := range insts {
		nttls[inst.ID] = c.opts.Now().Add(c.opts.NodeTTL)
	}
	c.nttls[name] = nttls
	return cloneInstances(insts), nil
}

// isValidLocked 报告 name 的缓存快照在服务级是否仍新鲜。节点级新鲜度在返回
// 快照时单独执行（见 pruneExpiredNodesLocked），短节点 TTL 不会强制整服务重拉。
// 调用方必须持 c.mu。
func (c *cache) isValidLocked(name string) bool {
	if _, ok := c.cache[name]; !ok {
		return false
	}
	if exp, ok := c.ttls[name]; !ok || c.opts.Now().After(exp) {
		return false
	}
	return true
}

// pruneExpiredNodesLocked 返回剔除过期节点后的缓存实例，同时把它们从节点级
// TTL 表里清掉。返回深拷贝，调用方无法经引用改到缓存快照。调用方必须持 c.mu。
func (c *cache) pruneExpiredNodesLocked(name string) []*registry.ServiceInstance {
	insts := c.cache[name]
	nttls := c.nttls[name]
	if len(nttls) == 0 {
		return cloneInstances(insts)
	}
	now := c.opts.Now()
	kept := make([]*registry.ServiceInstance, 0, len(insts))
	for _, inst := range insts {
		if exp, ok := nttls[inst.ID]; ok && now.After(exp) {
			delete(nttls, inst.ID)
			continue
		}
		kept = append(kept, inst)
	}
	return cloneInstances(kept)
}

// isThrottledLocked 报告是否应抑制本次刷新、改用旧数据。调用方必须持 c.mu。
func (c *cache) isThrottledLocked(name string) bool {
	if len(c.cache[name]) == 0 {
		return false
	}
	last, ok := c.lastRefresh[name]
	if !ok {
		return false
	}
	return c.opts.Now().Sub(last) < c.opts.MinRetryInterval
}

// cloneInstances 深拷贝实例列表，调用方无法经返回引用改到缓存快照。
func cloneInstances(insts []*registry.ServiceInstance) []*registry.ServiceInstance {
	out := make([]*registry.ServiceInstance, 0, len(insts))
	for _, inst := range insts {
		cp := *inst
		if inst.Endpoints != nil {
			cp.Endpoints = append([]string(nil), inst.Endpoints...)
		}
		if inst.Metadata != nil {
			m := make(map[string]string, len(inst.Metadata))
			for k, v := range inst.Metadata {
				m[k] = v
			}
			cp.Metadata = m
		}
		out = append(out, &cp)
	}
	return out
}
