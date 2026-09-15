// Package contract 提供 argo 后端的契约装配：bootstrapv1.Workflow 的 argo
// 段 → bald/workflow/argo ClientOptions 映射 + WorkflowRegistry Provider。
//
// 单独成包的原因：argo 包保持零契约依赖（纯 REST 客户端），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/workflow/argo"
)

// Type 是契约 workflow 段中 argo 后端的段名。
const Type = "argo"

// Provider 按契约 workflow.argo 段构造 Argo Workflows 客户端。
// 返回的 cleanup 关闭客户端（断开空闲连接）。仅当 workflow.argo 段存在时被
// WorkflowRegistry 调度到。签名与 bootstrap.WorkflowProvider 结构化兼容，直接注册：
//
//	wr := bootstrap.NewWorkflowRegistry()
//	wr.MustRegister(argocontract.Type, argocontract.Provider)
func Provider(ctx context.Context, cfg *bootstrapv1.Workflow) (any, func(), error) {
	sec := cfg.GetArgo()
	if sec == nil {
		return nil, nil, fmt.Errorf("workflow: type=%q but argo section is missing", Type)
	}

	client, err := argo.NewClient(argo.ClientOptions{
		ServerURL:          sec.GetServerUrl(),
		Namespace:          sec.GetNamespace(),
		Token:              sec.GetToken(),
		InsecureSkipVerify: sec.GetInsecureSkipVerify(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("workflow: create argo client: %w", err)
	}
	return client, func() { _ = client.Close() }, nil
}

// 类型守卫：Provider 签名与 bootstrap.WorkflowProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Workflow) (any, func(), error) = Provider
