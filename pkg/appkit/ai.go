// ai.go 是 appkit 侧的 AI 模型客户端装配胶水：契约 ai 段的 Registry
// （构造期）已迁 bootstrap（2026-09-15，见 bootstrap/ai.go），本文件保留
// 运行期装配面——WithAiRegistry Option、buildAis 胶水、Ai/Ais 访问器。
//
// 分工（构造期归 bootstrap，运行期归 appkit）：
//   - bootstrap.AiRegistry：显式注册 Provider、段枚举、Build；
//   - appkit：BeforeStart（阶段 B，契约装载校验后）调 Build，实例存
//     a.ais；cleanup 挂停机 Effect——注册在 storage-clients 之后（当前
//     三后端均无连接池，cleanup 为 nil，Effect 预留未来语义）。
//     ai 段不支持热更新。
//
// 契约字段消费注记：ai.*.cloud.organization 仅 openai/langchaingo 消费
// （SDK 支持）；eino-ext openai 无 organization 概念，eino 段不消费该字段
// （配置了也无效，文档须提示）。
//
// 用法：
//
//	ar := bootstrap.NewAiRegistry()
//	ar.MustRegister(openaicontract.Type, openaicontract.Provider)      // "openai"
//	ar.MustRegister(langchaincontract.Type, langchaincontract.Provider) // "langchaingo"
//	app, err := appkit.FromBootstrap(cfg, appkit.WithAiRegistry(ar), ...)
//	// Run 后取实例：
//	cli := app.Ai(openaicontract.Type).(*goopenai.Client)
package appkit

import (
	"context"
	"errors"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
)

// WithAiRegistry 注入 AI 模型后端的契约装配注册表（显式注册 provider，
// 见 bootstrap.AiRegistry）。契约 ai 段任一后端段存在时，阶段 B 按段名
// 查表构建客户端；ai 段不支持热更新。
func WithAiRegistry(r *baldbootstrap.AiRegistry) BootstrapOption {
	return func(s *bootstrapSpec) { s.aiRegistry = r }
}

// buildAis 在阶段 B（契约装载校验后）按契约 ai 段构建全部已配置后端的
// 模型客户端，存入 a.ais 供业务经 Ai/Ais 取用。ai 段缺失为 no-op；
// 段存在但未接 AiRegistry 则 fail-fast。
// 返回聚合 cleanup（可为 nil），由 FromBootstrap 挂停机 Effect。
func buildAis(a *AppKit, cfg *bootstrapv1.BootstrapConfig, spec *bootstrapSpec) (func(), error) {
	acfg := cfg.GetAi()
	if acfg == nil {
		return nil, nil // 未配置任何 AI 客户端
	}
	if spec.aiRegistry == nil {
		return nil, errors.New("appkit: bootstrap.ai present but no AiRegistry wired (WithAiRegistry missing)")
	}
	clients, cleanup, err := spec.aiRegistry.Build(context.Background(), acfg)
	if err != nil {
		return nil, err
	}
	a.ais = clients
	return cleanup, nil
}

// Ai 返回按契约段装配的后端模型客户端实例（阶段 B 后、Run 期可用；
// FromBootstrap 构造期尚未装载契约，返回空）。
// typ 为契约段名（见 ai/<backend>/contract.Type，如 "openai"）。
// 并发语义：ais 只在 BeforeStart（阶段 B）单次写入，Run 后只读，
// happens-before 由 Run 启动流程保证。
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
