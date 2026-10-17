// audit_registry.go 提供审计后端（audit 契约段）的装配接缝类型。
//
// 批 2 W2b 起，audit 段经**段驱动 Provider** 装配：框架自有的 "log" 后端
// 内置于 std.Standard()（核心 LoggerAuditor 零依赖开箱即用）；store/stream
// 等第三方后端的实现方（contrib/audit-gorm、contrib/audit-stream）经
// AuditProvider 函数值接入——用户声明自己的 audit 段 Provider，在其 Build
// 内把 backends 项映射为后端构造器并经 pkg/audit.Compose 组装（多后端
// 列表分发、构造失败逆序回滚的语义收敛在 Compose，批 1 内化）。
//
// 与 tracer/metrics 的既有差异保持：backends 含 "log" 零注册即得——
// LoggerAuditor 在核心包零依赖。store/stream 走 contract 构造（连接实例由
// 业务代码注入，「配置驱动参数，代码声明能力」分工：契约段声明类型与参数，
// DB/Redis 连接不进契约）。
//
// 段缺省语义：不装配（全局 audit.GetAuditor() 保持现状，默认 nop）。
//
// 生命周期约定（用户 Provider 遵守）：阶段 B（BeforeStart，契约装载校验后）
// Build 并由引擎全局注入（SetAuditor）；build 序在 database/cache 客户端
// 之后——store 后端需 *gorm.DB、stream 后端需 redis 客户端，资源必须先
// 就绪（scope.App 经 Database/Cache 取用）。停机 Effect（appkit:audit-restore）
// 逆序回放最后执行：先 cleanup（stream flush 尾批审计事件），再恢复装配前
// 全局 Auditor。audit 段不支持热更新（运行期热切走 R1-2 协调器）。
//
// 用法（Serve 路径，第三方 audit Provider 覆盖 std 默认——段路径末者胜）：
//
//	appkit.MustServe(ctx, std.Standard(), myAuditProvider(storeProvider), ...)
package appkit

import (
	"context"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/audit"
)

// 契约段 audit.backends 的取值（后端类型常量）。
const (
	// AuditTypeLog 是结构化日志后端（核心 LoggerAuditor，零依赖开箱
	// 即用，std 内置无需 Provider 注册）。
	AuditTypeLog = "log"
	// AuditTypeStore 是落库后端（contrib/audit-gorm，业务注入 gorm 连接）。
	AuditTypeStore = "store"
	// AuditTypeStream 是 Redis Stream 异步后端（contrib/audit-stream，
	// 业务注入 redis 客户端）。
	AuditTypeStream = "stream"
)

// AuditProvider 按契约 Audit 段装配审计后端。
//
// res 是**已完成前置装配的资源容器**（阶段 B 的 database/cache/storage 等
// 客户端）：审计后端若需 DB/Redis 连接，经此取用（如 store 后端取
// res.Resource("database.sql") 的 *gorm.DB、stream 后端取
// res.Resource("cache.redis")）。
//
// 为什么显式传 res（2026-09-28 修订）：审计后端与 DB/Redis 是「消费者依赖
// 生产者」——原实现把 buildAudit 排在 buildDatabases **之前**，后端构造时
// 资源必为 nil，业务被迫用「构造期捕获取值函数 + 请求期首次解析」的惰性
// 包装（lazyStoreAuditor）绕开。这与 T11/T12 修的「装配时序倒置」同族。
// 现 audit 段的 canonical 构建序排在数据/缓存客户端之后，且把资源容器
// 显式交给 provider——构造期即取真实实例，惰性包装失去存在理由。
//
// 返回 (auditor, cleanup)：cleanup 承载后端资源释放（如 stream 后台
// goroutine 停机 flush 尾批），由引擎挂 Effect 逆序回放。
type AuditProvider func(ctx context.Context, cfg *bootstrapv1.Audit, res *AppKit) (audit.Auditor, func(context.Context) error, error)

// auditState 承载阶段 B（BeforeStart）装配、停机期消费的句柄（构造期零值
// + 运行期接线，引擎 storeProduct 填充）。
type auditState struct {
	// prev 装配前的全局 Auditor（Effect 回放恢复，T1 纪律）。
	prev audit.Auditor
	// cleanup 后端资源释放（多后端聚合逆序回放，如 stream 停机 flush 尾批）。
	cleanup Cleanup
}
