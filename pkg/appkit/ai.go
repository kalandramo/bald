// ai.go 实现 AI 模型客户端的契约装配注册表（bootstrapv1.Ai 段 →
// 具体后端实例）。
//
// 与 StorageRegistry 同模式：显式注册（不用 init()+blank import，主程序
// 在 main() 里逐个 MustRegister）；段存在但未注册 fail-fast；cleanup 挂
// 停机 Effect 逆序回放。ai 是 optional 段集合（openai/langchaingo/eino
// 可并存），装配语义是「每段各自构建、全部返回」。
//
// 契约字段消费注记：ai.*.cloud.organization 仅 openai/langchaingo 消费
// （SDK 支持）；eino-ext openai 无 organization 概念，eino 段不消费该字段
// （配置了也无效，文档须提示）。
//
// 生命周期：阶段 B（BeforeStart，契约装载校验后）构建；ai 段不支持热更新
// （与 database 段同理）。停机 Effect 注册在 storage-clients 之后——当前
// 三后端均无连接池（cleanup 为 nil），Effect 预留未来语义。
//
// 用法：
//
//	ar := appkit.NewAiRegistry()
//	ar.MustRegister(openaicontract.Type, openaicontract.Provider)      // "openai"
//	ar.MustRegister(langchaincontract.Type, langchaincontract.Provider) // "langchaingo"
//	app, err := appkit.FromBootstrap(cfg, appkit.WithAiRegistry(ar), ...)
//	// Run 后取实例：
//	cli := app.Ai(openaicontract.Type).(*goopenai.Client)
package appkit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// AiProvider 按契约 Ai 段构造具体后端模型客户端。
// 返回 (实例, cleanup, error)：cleanup 释放后端资源（当前三后端均为 nil），
// 由 FromBootstrap 在停机 Effect 中逆序回放。
// 各后端的实现见 ai/<backend>/contract 包。
type AiProvider func(ctx context.Context, cfg *bootstrapv1.Ai) (any, func(), error)

// AiRegistry 是 AI 模型后端 Provider 的显式注册表，键为契约段名。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
type AiRegistry struct {
	mu        sync.Mutex
	providers map[string]AiProvider
}

// NewAiRegistry 创建空注册表。
func NewAiRegistry() *AiRegistry {
	return &AiRegistry{providers: make(map[string]AiProvider)}
}

// Register 注册一个后端 Provider；重名/空名/nil provider 均 fail-fast。
func (r *AiRegistry) Register(typ string, p AiProvider) error {
	if typ == "" {
		return errors.New("appkit: ai type is empty")
	}
	if p == nil {
		return fmt.Errorf("appkit: ai provider for %q is nil", typ)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]AiProvider)
	}
	if _, dup := r.providers[typ]; dup {
		return fmt.Errorf("appkit: ai provider %q already registered", typ)
	}
	r.providers[typ] = p
	return nil
}

// MustRegister 注册后端 Provider，失败 panic（装配期错误应当炸在启动时）。
func (r *AiRegistry) MustRegister(typ string, p AiProvider) {
	if err := r.Register(typ, p); err != nil {
		panic(err)
	}
}

// aiSection 契约段枚举：固定顺序即装配顺序（proto 字段序），
// 新后端在 bconf 加段后在此追加。
type aiSection struct {
	name   string
	exists func(*bootstrapv1.Ai) bool
}

var aiSections = []aiSection{
	{"openai", func(a *bootstrapv1.Ai) bool { return a.GetOpenai() != nil }},
	{"langchaingo", func(a *bootstrapv1.Ai) bool { return a.GetLangchaingo() != nil }},
	{"eino", func(a *bootstrapv1.Ai) bool { return a.GetEino() != nil }},
}

// Build 按契约段装配全部已配置的 AI 客户端：段存在 → 查表构建。
// 全部段缺失为 no-op；任一段存在但未注册 Provider 均 fail-fast；
// 构建失败回滚已建实例的 cleanup（逆序）。
func (r *AiRegistry) Build(ctx context.Context, cfg *bootstrapv1.Ai) (map[string]any, func(), error) {
	if cfg == nil {
		return nil, nil, nil // 未配置任何 AI 客户端
	}
	r.mu.Lock()
	provs := make(map[string]AiProvider, len(r.providers))
	for k, v := range r.providers {
		provs[k] = v
	}
	r.mu.Unlock()

	var (
		clients  = make(map[string]any)
		cleanups []func()
		rollback = func() {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
		}
	)
	for _, sec := range aiSections {
		if !sec.exists(cfg) {
			continue // 段缺失 = 未声明该后端
		}
		p, ok := provs[sec.name]
		if !ok {
			rollback()
			return nil, nil, fmt.Errorf("appkit: ai.%s present but provider not registered (import the backend contract package and MustRegister it)", sec.name)
		}
		cli, cleanup, err := p(ctx, cfg)
		if err != nil {
			rollback()
			return nil, nil, fmt.Errorf("appkit: build ai %s: %w", sec.name, err)
		}
		clients[sec.name] = cli
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}
	aggregated := func() { rollback() }
	if len(cleanups) == 0 {
		aggregated = nil
	}
	return clients, aggregated, nil
}

// WithAiRegistry 注入 AI 模型后端的契约装配注册表（显式注册 provider，
// 见 AiRegistry）。契约 ai 段任一后端段存在时，阶段 B 按段名查表构建
// 客户端；ai 段不支持热更新。
func WithAiRegistry(r *AiRegistry) BootstrapOption {
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
