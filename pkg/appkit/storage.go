// storage.go 是 appkit 侧的对象存储客户端资源面：契约 storage 段经段驱动
// Provider 装配（引擎把产物 map[子段名]any 落入 a.storages），本文件保留
// Run 期取用面——Storage/Storages 访问器。storage cleanup 的 Effect 注册
// 在 cache-clients 之后——停机逆序：storage 先关（暂无长连接）、缓存次之、
// 数据库最后关。storage 段不支持热更新。
package appkit

// Storage 返回按契约段装配的对象存储客户端（阶段 B 后、Run 期可用）。
// typ 为契约子段名（见 oss/<backend>/contract.Type，如 "s3"、"minio"）。
func (k *AppKit) Storage(typ string) (any, bool) {
	cli, ok := k.storages[typ]
	return cli, ok
}

// Storages 返回全部已装配对象存储客户端（副本，段名→实例），用于遍历诊断。
func (k *AppKit) Storages() map[string]any {
	out := make(map[string]any, len(k.storages))
	for typ, cli := range k.storages {
		out[typ] = cli
	}
	return out
}
