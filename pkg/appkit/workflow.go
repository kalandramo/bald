// workflow.go 是 appkit 侧的工作流客户端装配胶水：契约 workflow 段的
// Registry（构造期）已迁 bootstrap（2026-09-15，见 bootstrap/workflow.go），
// 本文件保留运行期装配面——WithWorkflowRegistry Option、buildWorkflows
// 胶水、Workflow/Workflows 访问器。
//
// 分工（构造期归 bootstrap，运行期归 appkit）：
//   - bootstrap.WorkflowRegistry：显式注册 Provider、段枚举、Build；
//   - appkit：BeforeStart（阶段 B，契约装载校验后）调 Build，实例存
//     a.workflows；cleanup 挂停机 Effect——注册在 ai-clients 之后，
//     停机逆序回放时工作流客户端先于 ai/storage/cache/database 关闭。
//     workflow 段不支持热更新。
//
// 用法：
//
//	wr := bootstrap.NewWorkflowRegistry()
//	wr.MustRegister(argocontract.Type, argocontract.Provider) // "argo"
//	app, err := appkit.FromBootstrap(cfg, appkit.WithWorkflowRegistry(wr), ...)
//	// Run 后取实例：
//	cli := app.Workflow(argocontract.Type).(*argo.WorkflowClient)
package appkit

import (
	"context"
	"errors"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
)

// WithWorkflowRegistry 注入工作流引擎后端的契约装配注册表（显式注册
// provider，见 bootstrap.WorkflowRegistry）。契约 workflow 段任一后端段
// 存在时，阶段 B 按段名查表构建客户端；workflow 段不支持热更新。
func WithWorkflowRegistry(r *baldbootstrap.WorkflowRegistry) BootstrapOption {
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
