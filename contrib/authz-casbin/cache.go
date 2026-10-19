package authzcasbin

import (
	"container/list"
	"sync"
)

// enforceCache 是 (subject, object, action) → 判定结果 的有界 LRU 结果缓存，
// 批 6 第 1 项（casbin 引擎逐请求分配优化）的核心构件。
//
// 背景：批 5 W3 落证 casbin 引擎每次 Enforce ≈206 allocs/8.6KB（函数表/参数
// 装箱等逐请求重建），策略全扫拒绝路径更高（≈1,270 allocs，101 行全扫）。而
// Web 服务的授权流量高度重入——同一 subject 反复访问同一 (object, action) 是
// 常态——结果缓存在这一形态下收益最大。
//
// 安全语义（勿放宽，对齐本包 fail-closed 纪律）：
//   - 只缓存「评估完成且无错误」的判定：命中返回的是**曾经依据当时策略算出的**
//     结果，不是无策略依据的放行；错误永不入缓存（引擎故障逐次如实上报）；
//   - 策略变更必须整体失效：Reload 成功后缓存必空（见 casbin.go Reload——
//     LoadPolicy 无论成败都失效，失败时 casbin 保留旧策略，失效只是多一次
//     冷启动，不构成正确性风险）；
//   - 容量有界：LRU 淘汰，插入数超过容量时逐出最久未用条目，map 尺寸有上界
//     （防 subject/cardinality 爆炸式膨胀）；
//   - 并发安全：单互斥锁保护（临界区仅 map 查找/指针改链，无分配）；未分片是
//     YAGNI 取舍——如并发画像显示锁争用，可按 key 哈希分片而不改语义。
//
// 键即结构体（三个既有 string 字段，无拼接）：命中路径零分配——不重蹈
// 「省了引擎分配、键又造出新的」的覆辙（官方 CachedEnforcer 的键构造走
// 可变参数装箱 + strings.Builder，每次查询本身就是数笔分配）。
type enforceCache struct {
	mu sync.Mutex

	// cap 是最大条目数（构造后只读）；ll 队首为最近使用、队尾为最久未用。
	cap   int
	ll    *list.List
	items map[enforceKey]*list.Element
}

// enforceKey 是缓存键：三个入参原样成键（string 不可变，作 map 键零额外分配）。
// action 取 ToLower 之后的值（与喂给引擎的一致），保证大小写变体共享同一判定。
type enforceKey struct {
	subject string
	object  string
	action  string
}

// enforceEntry 是链表节点载荷；key 回存以支持逐出时反删 map。
type enforceEntry struct {
	key     enforceKey
	allowed bool
}

// defaultEnforceCacheCapacity 默认容量：1,024 条判定结果。gen 产物默认策略集
// （百级资源 × 千级 subject）的活跃工作集内，足以覆盖常态流量。
const defaultEnforceCacheCapacity = 1024

func newEnforceCache(capacity int) *enforceCache {
	if capacity < 1 {
		capacity = 1 // 防御：容量至少 1，语义保持有界
	}
	// map 不按容量预分配：构造期成本归零（NewRows_LoadPolicy 不因缓存膨胀），
	// 稳态尺寸由实际工作集驱动、上界仍是 cap。
	return &enforceCache{
		cap:   capacity,
		ll:    list.New(),
		items: make(map[enforceKey]*list.Element),
	}
}

// get 返回 (判定结果, 是否命中)；命中即触及（移到队首）。
// 只在锁内做 map 查找与指针改链，零分配。
func (c *enforceCache) get(key enforceKey) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return false, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*enforceEntry).allowed, true
}

// put 插入或更新判定；容量已满时逐出队尾（最久未用）。
func (c *enforceCache) put(key enforceKey, allowed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		el.Value.(*enforceEntry).allowed = allowed
		return
	}
	if c.ll.Len() >= c.cap {
		if back := c.ll.Back(); back != nil {
			c.ll.Remove(back)
			delete(c.items, back.Value.(*enforceEntry).key)
		}
	}
	entry := &enforceEntry{key: key, allowed: allowed}
	c.items[key] = c.ll.PushFront(entry)
}

// invalidate 整体清空（Reload 后缓存必空的不变量由此维护）。
func (c *enforceCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.items = make(map[enforceKey]*list.Element)
}
