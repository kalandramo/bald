// broker.go 是 appkit 侧的消息代理实例资源面：契约 broker 段经段驱动
// Provider 装配（引擎把产物 map[子段名]any 落入 a.brokers），本文件保留
// Run 期取用面——Broker/Brokers 访问器。broker cleanup 的 Effect 排在
// canonical 链最末登记——停机逆序回放最先执行（in-flight 消息优先收）。
// broker 段不支持热更新。
package appkit

// Broker 返回按契约段装配的消息代理实例（阶段 B 后、Run 期可用）。
// typ 为契约子段名（见 broker/<backend>/contract.Type，如 "kafka"）。
func (k *AppKit) Broker(typ string) (any, bool) {
	cli, ok := k.brokers[typ]
	return cli, ok
}

// Brokers 返回全部已装配消息代理实例（副本，段名→实例），用于遍历诊断。
func (k *AppKit) Brokers() map[string]any {
	out := make(map[string]any, len(k.brokers))
	for typ, cli := range k.brokers {
		out[typ] = cli
	}
	return out
}
