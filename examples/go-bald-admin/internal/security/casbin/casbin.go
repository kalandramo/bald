// Package casbin 是 go-bald-admin 的授权装配薄壳（M6.1 引入，P11 起实现晋升 contrib）。
//
// 职责变化（P11，见 docs/devel/zh-CN/架构优化路线.md）：casbin 桥接的**实现**已晋升为
// contrib module（github.com/kalandramo/bald-authz-casbin，内嵌通用 RBAC 模型 + 纯 Enforce）；
// 本包仅保留**业务策略数据**（rbac_policy.csv：角色→权限、subject→角色——策略即业务数据，
// 归业务 module 所有）并把它注入 contrib 构造器。传输中立归一化仍由核心拦截器完成（P9）。
package casbin

import (
	_ "embed"

	contribcasbin "github.com/kalandramo/bald-authz-casbin"
)

//go:embed rbac_policy.csv
var policyCSV string

// New 用本业务策略 + contrib 内嵌的通用 RBAC 模型构造授权器。
func New() (*contribcasbin.Authorizer, error) {
	return contribcasbin.New(policyCSV)
}
