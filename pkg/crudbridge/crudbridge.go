// Package crudbridge 提供 bald 身份体系（pkg/contextx）到 bald-crud viewer 体系的桥接。
//
// bald 的请求级身份是 string 型（contextx.UserID/TenantID），bald-crud 的 viewer.Context
// 是 uint64 型并承载权限/数据范围/视图判定。本包把两者接通，使 bald-crud 的
// EnforceTenant（租户强制）、DataScope（行级范围）能在 bald 服务的请求链路里生效。
//
// 推荐接入点：AuthnInterceptor 认证成功后（与 contextx.WithTenantID 同一位置）调用
// InjectViewerFromContext，或由业务用 JWT claims 显式构造 SimpleViewer 注入。
//
// 安全语义（务必阅读）：
//   - viewer.Context.IsSystemContext() == true 会被 bald-crud 的 EnforceTenant
//     视为系统视图而跳过租户强制。因此本包的 System 标志必须显式设置，
//     绝不基于「UserID 为空」等隐式条件推断，防止匿名请求绕过租户隔离。
//   - TenantID 无法解析为正整数时按平台视图（0）处理——若你的租户 ID 不是
//     数字字符串，请勿使用默认转换，应自行实现 viewer.Context 或直接构造 SimpleViewer。
package crudbridge

import (
	"context"
	"strconv"

	"github.com/kalandramo/bald-crud/viewer"
	"github.com/kalandramo/bald/pkg/contextx"
)

// SimpleViewer 是 viewer.Context 的直白实现，字段全部显式暴露，
// 供业务用 JWT claims / session 自由构造，避免隐式转换语义。
type SimpleViewer struct {
	UserIDValue    uint64
	TenantIDValue  uint64
	OrgUnitIDValue uint64
	PermsValue     []string
	RolesValue     []string
	ScopesValue    []viewer.DataScope
	TraceIDValue   string

	// System 显式声明系统后台任务视图（绕过租户强制的唯一开关）。
	System bool
	// Auditable 显式声明是否需要审计记录。
	Auditable bool
}

// 编译期保证实现完整。
var _ viewer.Context = (*SimpleViewer)(nil)

func (s *SimpleViewer) UserID() uint64                { return s.UserIDValue }
func (s *SimpleViewer) TenantID() uint64              { return s.TenantIDValue }
func (s *SimpleViewer) OrgUnitID() uint64             { return s.OrgUnitIDValue }
func (s *SimpleViewer) Permissions() []string         { return s.PermsValue }
func (s *SimpleViewer) Roles() []string               { return s.RolesValue }
func (s *SimpleViewer) DataScope() []viewer.DataScope { return s.ScopesValue }
func (s *SimpleViewer) TraceID() string               { return s.TraceIDValue }

func (s *SimpleViewer) HasPermission(action, resource string) bool {
	if len(s.PermsValue) == 0 {
		return false
	}
	want := action + ":" + resource
	for _, p := range s.PermsValue {
		if p == want {
			return true
		}
	}
	return false
}

// IsPlatformContext 平台管理视图：租户为 0 且非系统任务。
func (s *SimpleViewer) IsPlatformContext() bool { return !s.System && s.TenantIDValue == 0 }

// IsTenantContext 租户业务视图：租户非 0 且非系统任务。
func (s *SimpleViewer) IsTenantContext() bool { return !s.System && s.TenantIDValue > 0 }

// IsSystemContext 仅在显式声明 System 时为真。
func (s *SimpleViewer) IsSystemContext() bool { return s.System }

func (s *SimpleViewer) ShouldAudit() bool { return s.Auditable }

// ViewerFromIdentity 从平铺身份字段构造 viewer.Context。
//
// 这是认证中间件的推荐入口：transport 层（gin/gRPC）认证成功拿到 claims 后，
// 把字段平铺传入（本包不依赖 authn，避免 crudbridge ↔ authn 循环依赖）。
//   - userID/tenantID 为 string，解析为 uint64；解析失败按 0（平台视图），
//     语义警告见包注释；
//   - perms 建议传 OAuth scopes（"user:read" 格式，直接对上 HasPermission）；
//   - 全部字段为空时返回 noop（三视图全 false → EnforceTenant fail-closed）。
func ViewerFromIdentity(userID, tenantID, traceID string, perms, roles []string) viewer.Context {
	if userID == "" && tenantID == "" && traceID == "" && len(perms) == 0 && len(roles) == 0 {
		return viewer.NewNoopContext()
	}

	v := &SimpleViewer{TraceIDValue: traceID, PermsValue: perms, RolesValue: roles}
	if uid, err := strconv.ParseUint(userID, 10, 64); err == nil {
		v.UserIDValue = uid
	}
	if tid, err := strconv.ParseUint(tenantID, 10, 64); err == nil {
		v.TenantIDValue = tid
	}
	return v
}

// InjectViewerFromIdentity 由 ViewerFromIdentity 构造 viewer 并注入 context，
// 供认证中间件在 contextx.WithTenantID 之后链式调用。
func InjectViewerFromIdentity(ctx context.Context, userID, tenantID, traceID string, perms, roles []string) context.Context {
	return viewer.WithContext(ctx, ViewerFromIdentity(userID, tenantID, traceID, perms, roles))
}

// ViewerFromContext 从 bald 的 contextx 身份信息尽力构造 viewer.Context。
//
// 仅映射 contextx 已有的信息（UserID/TenantID/TraceID），权限、角色不可得；
// 认证中间件注入请改走 InjectViewerFromIdentity（可携带 scopes/roles）。
func ViewerFromContext(ctx context.Context) viewer.Context {
	if ctx == nil {
		return viewer.NewNoopContext()
	}
	return ViewerFromIdentity(
		contextx.UserIDFromContext(ctx),
		contextx.TenantIDFromContext(ctx),
		contextx.TraceIDFromContext(ctx),
		nil, nil,
	)
}

// InjectViewerFromContext 由 ViewerFromContext 构造 viewer 并注入 context。
func InjectViewerFromContext(ctx context.Context) context.Context {
	return viewer.WithContext(ctx, ViewerFromContext(ctx))
}

// InjectViewer 将业务自建的 viewer.Context 注入 context（推荐主路径）。
func InjectViewer(ctx context.Context, vc viewer.Context) context.Context {
	return viewer.WithContext(ctx, vc)
}
