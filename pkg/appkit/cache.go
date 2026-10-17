// cache.go 是 appkit 侧的缓存实例资源面：契约 cache 段经段驱动 Provider
// 装配（引擎把产物 map[子段名]any 落入 a.caches），本文件保留 Run 期取用
// 面——Cache/Caches 访问器。缓存 cleanup 的 Effect 注册在 database-clients
// 之后——停机逆序回放保证缓存先关（加速层，关了不影响正确性）、数据库
// 最后关。cache 段不支持热更新。
package appkit

// Cache 返回按契约段装配的缓存实例（阶段 B 后、Run 期可用）。
// typ 为契约子段名（见 cache/<backend>/contract.Type，如 "redis"）。
func (k *AppKit) Cache(typ string) (any, bool) {
	cli, ok := k.caches[typ]
	return cli, ok
}

// Caches 返回全部已装配缓存实例（副本，段名→实例），用于遍历诊断。
func (k *AppKit) Caches() map[string]any {
	out := make(map[string]any, len(k.caches))
	for typ, cli := range k.caches {
		out[typ] = cli
	}
	return out
}
