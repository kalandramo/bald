// workflow.go 实现工作流引擎客户端的契约装配注册表（bootstrapv1.Workflow 段 →
// 具体后端实例）。
//
// 与 AiRegistry 同模式：显式注册（不用 init()+blank import，主程序
// 在 main() 里逐个 MustRegister）；段存在但未注册 fail-fast；cleanup 挂
// 停机 Effect 逆序回放。workflow 是 optional 段集合（当前仅 argo，后续可
// 扩 temporal/conductor 等），装配语义是「每段各自构建、全部返回」。
//
// 生命周期：阶段 B（BeforeStart，契约装载校验后）构建；workflow 段不支持
// 热更新（与 database 段同理）。停机 Effect 注册在 ai-clients 之后——
// 停机逆序回放时工作流客户端先于 ai/storage/cache/database 关闭。
//
// 用法：
//
//	wr := appkit.NewWorkflowRegistry()
//	wr.MustRegister(argocontract.Type, argocontract.Provider) // "argo"
//	app, err := appkit.FromBootstrap(cfg, appkit.WithWorkflowRegistry(wr), ...)
//	// Run 后取实例：
//	cli := app.Workflow(argocontract.Type).(*argo.WorkflowClient)
package appkit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// WorkflowProvider 按契约 Workflow 段构造具体工作流引擎客户端。
// 返回 (实例, cleanup, error)：cleanup 释放后端资源（如关闭 HTTP 连接），
// 由 FromBootstrap 在停机 Effect 中逆序回放。
// 各后端的实现见 workflow/<engine>/contract 包。
type WorkflowProvider func(ctx context.Context, cfg *bootstrapv1.Workflow) (any, func(), error)

// WorkflowRegistry 是工作流引擎后端 Provider 的显式注册表，键为契约段名。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
type WorkflowRegistry struct {
	mu        sync.Mutex
	providers map[string]WorkflowProvider
}

// NewWorkflowRegistry 创建空注册表。
func NewWorkflowRegistry() *WorkflowRegistry {
	return &WorkflowRegistry{providers: make(map[string]WorkflowProvider)}
}

// Register 注册一个后端 Provider；重名/空名/nil provider 均 fail-fast。
func (r *WorkflowRegistry) Register(typ string, p WorkflowProvider) error {
	if typ == "" {
		return errors.New("appkit: workflow type is empty")
	}
	if p == nil {
		return fmt.Errorf("appkit: workflow provider for %q is nil", typ)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]WorkflowProvider)
	}
	if _, dup := r.providers[typ]; dup {
		return fmt.Errorf("appkit: workflow provider %q already registered", typ)
	}
	r.providers[typ] = p
	return nil
}

// MustRegister 注册后端 Provider，失败 panic（装配期错误应当炸在启动时）。
func (r *WorkflowRegistry) MustRegister(typ string, p WorkflowProvider) {
	if err := r.Register(typ, p); err != nil {
		panic(err)
	}
}

// workflowSection 契约段枚举：固定顺序即装配顺序（proto 字段序），
// 新后端在 bconf 加段后在此追加。
type workflowSection struct {
	name   string
	exists func(*bootstrapv1.Workflow) bool
}

var workflowSections = []workflowSection{
	{"argo", func(w *bootstrapv1.Workflow) bool { return w.GetArgo() != nil }},
}

// Build 按契约段装配全部已配置的工作流客户端：段存在 → 查表构建。
// 全部段缺失为 no-op；任一段存在但未注册 Provider 均 fail-fast；
// 构建失败回滚已建实例的 cleanup（逆序）。
func (r *WorkflowRegistry) Build(ctx context.Context, cfg *bootstrapv1.Workflow) (map[string]any, func(), error) {
	if cfg == nil {
		return nil, nil, nil // 未配置任何工作流客户端
	}
	r.mu.Lock()
	provs := make(map[string]WorkflowProvider, len(r.providers))
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
	for _, sec := range workflowSections {
		if !sec.exists(cfg) {
			continue // 段缺失 = 未声明该后端
		}
		p, ok := provs[sec.name]
		if !ok {
			rollback()
			return nil, nil, fmt.Errorf("appkit: workflow.%s present but provider not registered (import the backend contract package and MustRegister it)", sec.name)
		}
		cli, cleanup, err := p(ctx, cfg)
		if err != nil {
			rollback()
			return nil, nil, fmt.Errorf("appkit: build workflow %s: %w", sec.name, err)
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

// WithWorkflowRegistry 注入工作流引擎后端的契约装配注册表（显式注册
// provider，见 WorkflowRegistry）。契约 workflow 段任一后端段存在时，
// 阶段 B 按段名查表构建客户端；workflow 段不支持热更新。
func WithWorkflowRegistry(r *WorkflowRegistry) BootstrapOption {
	return func(s *bootstrapSpec) { s.workflowRegistry = r }
}

// buildWorkflows 在阶段 B（契约装载校验后）按契约 workflow 段构建全部已
// 配置后端的工作流客户端，存入 a.workflows 供业务经 Workflow/Workflows 取用。
// workflow 段缺失为 no-op；段存在但未接 WorkflowRegistry 则 fail-fast。
// 返回聚合 cleanup（可为 nil），由 FromBootstrap 挂停机 Effect。
func buildWorkflows(a *AppKit, cfg *bootstrapv1.BootstrapConfig, spec *bootstrapSpec) (func(), error) {
	wcfg := cfg.GetWorkflow()
	if wcfg == nil {
		return nil, nil // 未配置任何工作流客户端
	}
	if spec.workflowRegistry == nil {
		return nil, errors.New("appkit: bootstrap.workflow present but no WorkflowRegistry wired (WithWorkflowRegistry missing)")
	}
	clients, cleanup, err := spec.workflowRegistry.Build(context.Background(), wcfg)
	if err != nil {
		return nil, err
	}
	a.workflows = clients
	return cleanup, nil
}

// Workflow 返回按契约段装配的工作流引擎客户端实例（阶段 B 后、Run 期可用；
// FromBootstrap 构造期尚未装载契约，返回空）。
// typ 为契约段名（见 workflow/<engine>/contract.Type，如 "argo"）。
// 并发语义：workflows 只在 BeforeStart（阶段 B）单次写入，Run 后只读，
// happens-before 由 Run 启动流程保证。
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
