// Package crudbridge 提供 bald 身份体系（pkg/contextx）到 bald-crud viewer 体系的桥接。
//
// bald 的请求级身份是 string 型（contextx.UserID/TenantID），bald-crud 的 viewer.Context
// 承载权限/数据范围/视图判定。2026-09-24 起两侧 TenantID 统一为 string——此前
// viewer 侧为 uint64，桥接需 strconv.ParseUint，非数字租户 ID 解析失败会静默
// 降级为平台视图（安全缺陷）。统一后转换消失，无失败路径。
//
// 推荐接入点：AuthnInterceptor 认证成功后（与 contextx.WithTenantID 同一位置）调用
// InjectViewerFromContext，或由业务用 JWT claims 显式构造 SimpleViewer 注入。
//
// 安全语义（务必阅读）：
//   - viewer.Context.IsSystemContext() == true 会被 bald-crud 的 EnforceTenant
//     视为系统视图而跳过租户强制。因此本包的 System 标志必须显式设置，
//     绝不基于「UserID 为空」等隐式条件推断，防止匿名请求绕过租户隔离。
//   - TenantID 原样透传（2026-09-24 起）：viewer.Context.TenantID 已统一为 string，
//     与 bald 生态（contextx/authn/jwt/audit）一致。此前 uint64 版本对非数字租户 ID
//     解析失败会静默留 0，被 IsPlatformContext 误判为平台视图而跳过隔离——该有损
//     转换已移除，不存在「解析失败」这一路径。
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
	TenantIDValue  string
	OrgUnitIDValue uint64
	PermsValue     []string
	RolesValue     []string
	ScopesValue    []viewer.DataScope
	TraceIDValue   string

	// System 显式声明系统后台任务视图（绕过租户强制的唯一开关）。
	System bool
	// Platform 显式声明平台级身份（跨租户视图，如平台管理员）。
	//
	// 语义（2026-09-25，见《待处理事项》#2）：平台身份**必须显式声明**，
	// 绝不基于「TenantID 为空」推断——空租户同时表示「匿名/未认证」与
	// 「平台视图」两种相反语义，靠推断会 fail-open。与 System 同纪律。
	// 置位后 EnforceTenant 放行（跨租户可见）。
	Platform bool
	// Auditable 显式声明是否需要审计记录。
	Auditable bool
}

// 编译期保证实现完整。
var _ viewer.Context = (*SimpleViewer)(nil)

func (s *SimpleViewer) UserID() uint64                { return s.UserIDValue }
func (s *SimpleViewer) TenantID() string              { return s.TenantIDValue }
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

// IsPlatformContext 平台管理视图：**显式声明** Platform 且非系统任务。
//
// 2026-09-25 语义收紧（《待处理事项》#2，方案 D）：此前为
// `!System && TenantIDValue == ""`——把「空租户」**推断**为平台视图。
// 空租户同时表示「匿名/未认证」与「平台视图」两种相反语义，推断会 fail-open
// （已认证但租户为空的身份会看到全部租户数据）。改为显式 Platform 字段后，
// 空租户不再被推断为平台视图——它落入「身份不完整」，经 EnforceTenant
// fail-closed。与 bald 侧 contextx.WithPlatform 的「显式声明」纪律对齐。
func (s *SimpleViewer) IsPlatformContext() bool { return !s.System && s.Platform }

// IsTenantContext 租户业务视图：有租户身份（TenantID 非空）且非系统、非平台。
func (s *SimpleViewer) IsTenantContext() bool {
	return !s.System && !s.Platform && s.TenantIDValue != ""
}

// IsSystemContext 仅在显式声明 System 时为真。
func (s *SimpleViewer) IsSystemContext() bool { return s.System }

func (s *SimpleViewer) ShouldAudit() bool { return s.Auditable }

// ViewerFromIdentity 从平铺身份字段构造 viewer.Context。
//
// 这是认证中间件的推荐入口：transport 层（gin/gRPC）认证成功拿到 claims 后，
// 把字段平铺传入（本包不依赖 authn，避免 crudbridge ↔ authn 循环依赖）。
//   - userID 为 string，解析为 uint64；解析失败按 0；
//   - tenantID 为 string，**原样透传**（2026-09-24 统一）：此前解析为 uint64，
//     非数字租户 ID（如 "t-default"）解析失败被静默当作平台视图，导致隔离失效；
//   - platform 显式声明平台级身份（2026-09-25，见《待处理事项》#2）：由调用方
//     从 claims.Platform 透传，绝不基于空租户推断——否则已认证但租户为空的
//     身份会被当作平台视图（fail-open）；
//   - perms 建议传 OAuth scopes（"user:read" 格式，直接对上 HasPermission）；
//   - 全部字段为空且非平台时返回 noop（三视图全 false → EnforceTenant fail-closed）。
func ViewerFromIdentity(userID, tenantID, traceID string, perms, roles []string, platform bool) viewer.Context {
	if userID == "" && tenantID == "" && traceID == "" && len(perms) == 0 && len(roles) == 0 && !platform {
		return viewer.NewNoopContext()
	}

	v := &SimpleViewer{TraceIDValue: traceID, PermsValue: perms, RolesValue: roles, Platform: platform}
	if uid, err := strconv.ParseUint(userID, 10, 64); err == nil {
		v.UserIDValue = uid
	}
	v.TenantIDValue = tenantID // 原样透传，无解析失败路径
	return v
}

// InjectViewerFromIdentity 由 ViewerFromIdentity 构造 viewer 并注入 context，
// 供认证中间件在 contextx.WithTenantID 之后链式调用。
func InjectViewerFromIdentity(ctx context.Context, userID, tenantID, traceID string, perms, roles []string, platform bool) context.Context {
	return viewer.WithContext(ctx, ViewerFromIdentity(userID, tenantID, traceID, perms, roles, platform))
}

// ViewerFromContext 从 bald 的 contextx 身份信息尽力构造 viewer.Context。
//
// 仅映射 contextx 已有的信息（UserID/TenantID/TraceID），权限、角色不可得；
// 认证中间件注入请改走 InjectViewerFromIdentity（可携带 scopes/roles）。
// platform 取 contextx.PlatformFromContext——与 pkg/store 的隔离豁免同源，
// 保证同一请求在 viewer 侧与 store 侧对「平台身份」的判定一致。
func ViewerFromContext(ctx context.Context) viewer.Context {
	if ctx == nil {
		return viewer.NewNoopContext()
	}
	return ViewerFromIdentity(
		contextx.UserIDFromContext(ctx),
		contextx.TenantIDFromContext(ctx),
		contextx.TraceIDFromContext(ctx),
		nil, nil,
		contextx.PlatformFromContext(ctx),
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
