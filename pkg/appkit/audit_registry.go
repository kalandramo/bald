// audit_registry.go 实现审计后端（audit 契约段）的装配注册表。
//
// 与 TracerRegistry / MetricsRegistry 同模式：显式注册（不用 init()+blank
// import，主程序在 main() 里逐个 MustRegister）；段存在但 backends 空/
// 重复项/未注册均 fail-fast；后端 cleanup 聚合逆序回放，挂 Effect。
// audit 段按 backends **列表**分发（契约语义：audit.backends 取
// {"log", "store", "stream"} 的非空子列，v0.6.0 起多选——与 R1-2 协调器
// 期望态键 audit.backends、appspec audit_backends 同名同值：契约面管
// 启动期一次性装配，协调器面管运行期收敛热切，双轨并存）。
//
// 与 tracer/metrics 的差异：backends 含 "log" 零注册即得——LoggerAuditor
// 在核心包零依赖，appkit import 无负担（对齐 codegen buildAuditBackend
// 的 log 开箱即用语义）。store/stream 走 contract 注册
// （contrib/audit-{store,stream}/contract），连接实例由业务代码注入
// （「配置驱动参数，代码声明能力」分工：契约段声明类型与参数，DB/Redis
// 连接不进契约）。
//
// 多后端按列表序组装 MultiAuditor（顺序即广播序）；任一项构造失败
// 逆序回滚已构造项的 cleanup（对齐 bootstrap 六域 Registry 的 Build
// 模式）。
//
// 段缺省语义：不装配（全局 audit.GetAuditor() 保持现状，默认 nop）。
//
// 生命周期：阶段 B（BeforeStart，契约装载校验后）Build 并 SetAuditor
// 全局注入；停机 Effect 逆序回放——先 cleanup（stream flush 尾批审计
// 事件），再恢复装配前的全局 Auditor（T1 纪律：全局写入配套逆操作）。
// audit 段不支持热更新（运行期热切走 R1-2 协调器的 audit.backends
// 期望态，双轨并存——与「全局路径 vs 构造器注入」哲学一致）。
//
// 用法（FromBootstrap 路径）：
//
//	ar := appkit.NewAuditRegistry()
//	ar.MustRegister(storecontract.TypeStore, storecontract.NewStoreProvider(db))
//	ar.MustRegister(streamcontract.TypeStream, streamcontract.NewStreamProvider(rdb))
//	app, err := appkit.FromBootstrap(cfg, appkit.WithAuditRegistry(ar), ...)
package appkit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/audit"
)

// 契约段 audit.backends 的取值（后端类型常量）。
const (
	// AuditTypeLog 是结构化日志后端（核心 LoggerAuditor，零依赖开箱
	// 即用，无需注册 Registry）。
	AuditTypeLog = "log"
	// AuditTypeStore 是落库后端（contrib/audit-gorm，业务注入 gorm 连接）。
	AuditTypeStore = "store"
	// AuditTypeStream 是 Redis Stream 异步后端（contrib/audit-stream，
	// 业务注入 redis 客户端）。
	AuditTypeStream = "stream"
)

// AuditProvider 按契约 Audit 段装配审计后端。
// 返回 (auditor, cleanup)：cleanup 承载后端资源释放（如 stream 后台
// goroutine 停机 flush 尾批），由 FromBootstrap 挂 Effect 逆序回放。
type AuditProvider func(ctx context.Context, cfg *bootstrapv1.Audit) (audit.Auditor, func(context.Context) error, error)

// AuditRegistry 是审计后端 Provider 的显式注册表（audit.backends 多选；
// log 内置于 Build，无需注册）。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
type AuditRegistry struct {
	mu        sync.Mutex
	providers map[string]AuditProvider
}

// NewAuditRegistry 创建空注册表。
func NewAuditRegistry() *AuditRegistry {
	return &AuditRegistry{providers: make(map[string]AuditProvider)}
}

// Register 注册一个审计后端 Provider；重名/空名/nil 均 fail-fast。
func (r *AuditRegistry) Register(typ string, p AuditProvider) error {
	if typ == "" {
		return errors.New("appkit: audit type is empty")
	}
	if p == nil {
		return fmt.Errorf("appkit: audit provider for %q is nil", typ)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]AuditProvider)
	}
	if _, dup := r.providers[typ]; dup {
		return fmt.Errorf("appkit: audit provider %q already registered", typ)
	}
	r.providers[typ] = p
	return nil
}

// MustRegister 注册审计后端 Provider，失败 panic。
func (r *AuditRegistry) MustRegister(typ string, p AuditProvider) {
	if err := r.Register(typ, p); err != nil {
		panic(err)
	}
}

// Build 按契约 audit.backends 列表装配审计后端（v0.6.0 起多选）。
// 段为 nil → 不装配（nil, nil, nil）；列表空、含重复项或项未注册均
// fail-fast（与 TracerRegistry 同款主开关语义）；log 内置返回核心
// LoggerAuditor（无需注册）；多后端按列表序组装 MultiAuditor（顺序即
// 广播序），任一项构造失败逆序回滚已构造项的 cleanup；成功后 cleanup
// 聚合逆序回放（后构造先释放，对齐 Effect 逆序纪律），无 cleanup 项
// 则返回 nil。
func (r *AuditRegistry) Build(ctx context.Context, cfg *bootstrapv1.Audit) (audit.Auditor, func(context.Context) error, error) {
	if cfg == nil {
		return nil, nil, nil // 段缺省：不装配
	}
	backends := cfg.GetBackends()
	if len(backends) == 0 {
		return nil, nil, errors.New(`appkit: audit.backends is required when audit section is present (expect "log", "store" or "stream")`)
	}
	r.mu.Lock()
	provs := make(map[string]AuditProvider, len(r.providers))
	for k, v := range r.providers {
		provs[k] = v
	}
	r.mu.Unlock()

	var (
		auditors []audit.Auditor
		seen     = make(map[string]struct{}, len(backends))
		cleanups []func(context.Context) error
		rollback = func() {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i](ctx) // 失败路径尽力释放，错误忽略
			}
		}
	)
	for _, typ := range backends {
		if _, dup := seen[typ]; dup {
			rollback()
			return nil, nil, fmt.Errorf("appkit: duplicate backend %q in audit.backends", typ)
		}
		seen[typ] = struct{}{}
		if typ == AuditTypeLog {
			auditors = append(auditors, audit.NewLoggerAuditor()) // 内置：零依赖开箱即用
			continue
		}
		p, ok := provs[typ]
		if !ok {
			rollback()
			return nil, nil, fmt.Errorf("appkit: audit provider %q not registered (import the backend contract package and MustRegister it)", typ)
		}
		a, cleanup, err := p(ctx, cfg)
		if err != nil {
			rollback()
			return nil, nil, fmt.Errorf("appkit: build audit backend %q: %w", typ, err)
		}
		auditors = append(auditors, a)
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}

	var a audit.Auditor
	if len(auditors) == 1 {
		a = auditors[0] // 单后端直接返回（类型断言友好）
	} else {
		a = audit.NewMultiAuditor(auditors...) // 列表序 = 广播序
	}
	var aggregated func(context.Context) error
	if len(cleanups) > 0 {
		aggregated = func(ctx context.Context) error {
			var errs []error
			for i := len(cleanups) - 1; i >= 0; i-- { // 逆序：后构造先释放
				if err := cleanups[i](ctx); err != nil {
					errs = append(errs, err)
				}
			}
			return errors.Join(errs...)
		}
	}
	return a, aggregated, nil
}

// ---------------------------------------------------------------------------
// FromBootstrap 集成（阶段 B 构建 + Effect 回放）
// ---------------------------------------------------------------------------

// auditState 承载阶段 B（BeforeStart）装配、停机期消费的句柄
// （构造期零值 + 运行期接线，observabilityState 同模式）。
type auditState struct {
	// prev 装配前的全局 Auditor（Effect 回放恢复，T1 纪律）。
	prev audit.Auditor
	// cleanup 后端资源释放（多后端聚合逆序回放，如 stream 停机 flush 尾批）。
	cleanup func(context.Context) error
}

// buildAudit 在阶段 B（契约装载校验后）按 audit 段装配审计后端并注入
// 全局。段缺失为 no-op（全局保持现状）；backends 全为 log 零注册即得
// （Build 内置路径），含 store/stream 需对应 Provider 已注册，否则
// fail-fast（配置意图无法兑现）。返回停机句柄，由 FromBootstrap 挂 Effect。
func buildAudit(cfg *bootstrapv1.BootstrapConfig, spec *bootstrapSpec) (*auditState, error) {
	acfg := cfg.GetAudit()
	if acfg == nil {
		return nil, nil // 段缺省：不装配
	}
	// 未接线 Registry 时用空表装配：含非 log 项会因查不到 Provider
	// fail-fast，错误直指缺失的后端（比「no AuditRegistry wired」更
	// 可操作）；纯 log 列表零注册即得（决策④语义由 Build 内置兑现）。
	r := spec.auditRegistry
	if r == nil {
		r = NewAuditRegistry()
	}
	a, cleanup, err := r.Build(context.Background(), acfg)
	if err != nil {
		return nil, fmt.Errorf("appkit: build audit: %w", err)
	}

	prev := audit.GetAuditor()
	audit.SetAuditor(a)
	return &auditState{prev: prev, cleanup: cleanup}, nil
}
