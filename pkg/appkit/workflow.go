// workflow.go 是 appkit 侧的工作流引擎客户端资源面：契约 workflow 段经段
// 驱动 Provider 装配（引擎把产物 map[子段名]any 落入 a.workflows），本文件
// 保留 Run 期取用面——Workflow/Workflows 访问器。workflow 段不支持热更新。
package appkit

// Workflow 返回按契约段装配的工作流引擎客户端（阶段 B 后、Run 期可用）。
// typ 为契约子段名（见 workflow/<backend>/contract.Type）。
func (k *AppKit) Workflow(typ string) (any, bool) {
	cli, ok := k.workflows[typ]
	return cli, ok
}

// Workflows 返回全部已装配工作流客户端（副本，段名→实例），用于遍历诊断。
func (k *AppKit) Workflows() map[string]any {
	out := make(map[string]any, len(k.workflows))
	for typ, cli := range k.workflows {
		out[typ] = cli
	}
	return out
}
