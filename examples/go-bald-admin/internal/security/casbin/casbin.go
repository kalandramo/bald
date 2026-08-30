// Package casbin 实现基于 casbin 的 authz.Authorizer（M6.1 接成熟库，消除 §0 偏差）。
//
// 设计约束（对齐 bald P7 原则）：
//   - 实现 bald 核心的 authz.Authorizer 接口，作为业务侧桥接子模块；
//     核心框架保持零策略引擎耦合，casbin 只存在于本业务 module（不进 bald 核心）。
//   - 传输中立归一化已由核心拦截器完成（P9 反哺）：gin/grpc Authz 中间件经
//     authz.DefaultHTTPObject/DefaultHTTPAction（gin）与 authz.DefaultGRPCObject/
//     DefaultGRPCAction（grpc）在拦截器层把请求翻译为 (资源名, 动作)，本桥接只做纯 Enforce，
//     不再重复归一化（M6.8 CR Issue4 的根因：双命名空间泄漏，已在核心层根治，范例删冗余）。
//
// 桥接约定的归一化后权限点（casbin 的 obj:act）：
//   GET  /v1/secret/123   (gin DefaultHTTPObject/Action) -> obj="secret", act="get"
//   DEL  /v1/secret/123                              -> obj="secret", act="delete"
//   GET  /v1/auth/whoami                             -> obj="auth",   act="get"
//   SecretService/GetSecret   (grpc DefaultGRPCObject/Action) -> obj="secret", act="get"
//   SecretService/DeleteSecret                          -> obj="secret", act="delete"
//   SecretService/ListUsers                            -> obj="secret", act="list"
//   AuthService/WhoAmI                                 -> obj="auth",   act="get"
//
// 模型与策略以 casbin 原生 .conf/.csv 形式嵌入（go:embed），便于测试与审阅，
// 角色→权限、subject→角色均在策略文件声明，替换旧 rbac 包的内存静态表。
package casbin

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
var modelConf string

//go:embed rbac_policy.csv
var policyCSV string

// Authorizer 是基于 casbin 的 RBAC 授权器，实现 authz.Authorizer。
type Authorizer struct {
	enf *casbin.Enforcer
}

// New 用内嵌的模型与策略构造 casbin 授权器。
func New() (*Authorizer, error) {
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
//   - subject 为空（未认证）→ 拒绝；
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
