// Package authzcasbin 实现基于 casbin 的 bald authz.Authorizer 桥接，
// 自 go-bald-admin 范例 internal/security/casbin 晋升（P11，见 docs/devel/zh-CN/架构优化路线.md）。
//
// 设计约束（对齐 bald P7/P9 原则）：
//   - 实现 bald 核心的 authz.Authorizer 接口，作为业务侧桥接子模块；
//     核心框架保持零策略引擎耦合，casbin 只存在于本 module（不进 bald 核心）。
//   - 传输中立归一化已由核心拦截器完成（P9 反哺）：gin/grpc Authz 中间件经
//     authz.DefaultHTTPObject/DefaultHTTPAction（gin）与 authz.DefaultGRPCObject/
//     DefaultGRPCAction（grpc）在拦截器层把请求翻译为 (资源名, 动作)，本桥接只做纯 Enforce，
//     不再重复归一化（M6.8 CR Issue4 根因：双命名空间泄漏，已在核心层根治）。
//
// 职责划分：RBAC **模型**（.conf）是框架性声明，由本包内嵌为默认；**策略**（.csv，
// 角色→权限、subject→角色）是业务数据，由调用方注入（通常 go:embed 业务自己的 csv）。
//
// 归一化后的权限点约定（casbin 的 obj:act）：
//
//	GET /v1/secret/123 (HTTP) / SecretService/GetSecret (gRPC) -> obj="secret", act="get"
//	DEL /v1/secret/123            / SecretService/DeleteSecret -> obj="secret", act="delete"
//	                               / SecretService/ListUsers  -> obj="secret", act="list"
package authzcasbin

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/casbin/casbin/v2"
	casbinmodel "github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist/string-adapter"

	"github.com/kalandramo/bald/pkg/authz"
)

//go:embed rbac_model.conf
var defaultModel string

// Authorizer 是基于 casbin 的 RBAC 授权器，实现 authz.Authorizer。
type Authorizer struct {
	enf *casbin.Enforcer
}

// New 用内嵌的默认 RBAC 模型 + 调用方策略构造 casbin 授权器。
// policyCSV 为 casbin 策略文本（通常是业务 go:embed 的 .csv），形如：
//
//	p, admin, secret, get
//	g, u-admin, admin
func New(policyCSV string) (*Authorizer, error) {
	return NewWithModel(defaultModel, policyCSV)
}

// NewWithModel 用自定义模型与策略构造授权器（默认模型不满足时使用，如 ABAC/域模型）。
func NewWithModel(modelConf, policyCSV string) (*Authorizer, error) {
	m, err := casbinmodel.NewModelFromString(modelConf)
	if err != nil {
		return nil, fmt.Errorf("casbin: parse model: %w", err)
	}
	enf, err := casbin.NewEnforcer(
		m,
		stringadapter.NewAdapter(policyCSV),
	)
	if err != nil {
		return nil, fmt.Errorf("casbin: new enforcer: %w", err)
	}
	return &Authorizer{enf: enf}, nil
}

// Authorize 判定 subject（用户 ID）是否对 (object, action) 有权限。
//   - subject 为空（未认证）→ 报错拒绝；
//   - casbin Enforce 返回 false → 拒绝（默认拒绝，需显式授权）；
//   - 引擎错误 → 透传 error（拦截器映射为 500）。
//
// object/action 已由核心拦截器归一化（见包文档），此处直接作为 (obj, act) 喂给 casbin。
func (a *Authorizer) Authorize(ctx context.Context, subject, object, action string) (bool, error) {
	if subject == "" {
		return false, fmt.Errorf("casbin: empty subject (not authenticated)")
	}
	allowed, err := a.enf.Enforce(subject, object, strings.ToLower(action))
	if err != nil {
		return false, fmt.Errorf("casbin: enforce: %w", err)
	}
	return allowed, nil
}

// compile-time 断言：*Authorizer 实现 authz.Authorizer。
var _ authz.Authorizer = (*Authorizer)(nil)
