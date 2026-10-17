// ai.go 是 appkit 侧的 AI 模型客户端资源面：契约 ai 段经段驱动 Provider
// 装配（引擎把产物 map[子段名]any 落入 a.ais），本文件保留 Run 期取用面
// ——Ai/Ais 访问器。ai 段不支持热更新。
package appkit

// Ai 返回按契约段装配的后端模型客户端实例（阶段 B 后、Run 期可用）。
// typ 为契约子段名（见 ai/<backend>/contract.Type，如 "openai"）。
func (k *AppKit) Ai(typ string) (any, bool) {
	cli, ok := k.ais[typ]
	return cli, ok
}

// Ais 返回全部已装配后端模型客户端（副本，段名→实例），用于遍历诊断。
func (k *AppKit) Ais() map[string]any {
	out := make(map[string]any, len(k.ais))
	for typ, cli := range k.ais {
		out[typ] = cli
	}
	return out
}
