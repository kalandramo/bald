// Package model 是 go-bald-admin 的 GORM 实体（存储层）。
//
// 仅承载「表结构 + 列映射」，不含业务逻辑；多租户隔离由 bald core 的
// pkg/store 在查询时自动注入 TenantID 过滤（M2 起生效）。字段名经
// baldgorm.toColumn 默认 snake_case 映射为列名（ID->id, TenantID->tenant_id）。
//
// 实体按功能域分文件：user.go（用户）、tenant.go（租户）、role.go（角色/
// 权限/策略）、menu.go（菜单）、secret.go（机密）、dict.go（字典）、
// file.go（文件）、audit.go（审计）；本文件保留包说明与共享辅助函数。
package model

import "strings"

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
