// provider_test.go —— 段驱动装配引擎的测试装置。
//
// segProvider 是通用测试 Provider：固定段路径 + Build 闭包，可直接作为
// Option 传入 assembleForTest。registryProvider 把 bootstrap 各域 Registry
// 包成段驱动 Provider——「多引擎分派委托 Registry、产物 map[子段名]any」
// 的第三方引擎接入等价形态（生产侧范本见 contrib/store-gorm 的 Provider）。
package appkit

import (
	"context"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
)

// testNodePriority 是测试节点的任意非默认优先级（排在显式声明的 Provider
// 之后，用于钉住 (Priority, Key) 排序）。
const testNodePriority = 500

// segProvider 通用测试 Provider（v2：Plan 由段名派生节点，Build 闭包直接用
// ApplyScope 以减少测试样板）。
type segProvider struct {
	segs  []string
	build func(ctx context.Context, s ApplyScope) (any, Cleanup, error)
	plan  func(ctx context.Context, s PlanScope) ([]Node, error) // 可选：覆盖默认节点
}

func (p segProvider) Segments() []string { return p.segs }

// Apply 把自身追加进声明（Provider 即 Option）。
func (p segProvider) Apply(d *Decl) { d.Providers = append(d.Providers, p) }

func (p segProvider) Plan(ctx context.Context, s PlanScope) ([]Node, error) {
	if p.plan != nil {
		return p.plan(ctx, s)
	}
	segs := sortedUnique(p.segs)
	top := segmentTop(segs[0])
	n := Node{
		Key:      nodeKey(segs),
		Phase:    phaseFor(phaseByTop(top)),
		Priority: testSegmentPriority(top),
	}
	// 与 std Provider 一致的停机约束（测试装置镜像生产声明）。
	switch top {
	case "audit":
		n.TeardownAfter = []ResourceKey{"*"}
	case "metrics":
		n.TeardownAfter = []ResourceKey{"logger"}
	case "tracer":
		n.TeardownAfter = []ResourceKey{"logger", "metrics"}
	}
	return []Node{n}, nil
}

func (p segProvider) Build(ctx context.Context, s ApplyScope, _ Node) (any, Cleanup, error) {
	return p.build(ctx, s)
}

// testSegmentPriority 把旧 canonical 段序写成测试期望值（生产侧已无此表：
// 优先级由各 Provider 的 Plan 声明；测试装置镜像它们）。
func testSegmentPriority(top string) int {
	switch top {
	case "logger":
		return PriorityLogger
	case "registry":
		return PriorityRegistry
	case "tracer":
		return PriorityTracer
	case "metrics":
		return PriorityMetrics
	case "database":
		return PriorityDatabase
	case "cache":
		return PriorityCache
	case "storage":
		return PriorityStorage
	case "audit":
		return PriorityAudit
	case "ai":
		return PriorityAI
	case "workflow":
		return PriorityWorkflow
	case "broker":
		return PriorityBroker
	default:
		return 0
	}
}

// registryProvider 把一个 bootstrap 域 Registry 的 Build 包成段驱动
// Provider：sec 取契约子段，build 委托域 Registry（分派/回滚语义不变），
// 无参 cleanup 经 noopCleanup 适配。
func registryProvider[T any, B any](seg string, sec func(*bootstrapv1.BootstrapConfig) *T, build func(context.Context, *T) (B, func(), error)) segProvider {
	return segProvider{segs: []string{seg}, build: func(ctx context.Context, s ApplyScope) (any, Cleanup, error) {
		out, cleanup, err := build(ctx, sec(s.Config))
		return out, noopCleanup(cleanup), err
	}}
}

// dbTestProvider 等六个域包装：database/cache/storage/ai/workflow/broker/
// registry/config 各域测试经此把 bootstrap 域 Registry 接入段驱动装配
// （等价于旧 WithXRegistry，产物落 a.databases 等资源面）。
func dbTestProvider(reg *baldbootstrap.DatabaseRegistry) segProvider {
	return registryProvider("database", func(c *bootstrapv1.BootstrapConfig) *bootstrapv1.Database { return c.GetDatabase() }, reg.Build)
}

func cacheTestProvider(reg *baldbootstrap.CacheRegistry) segProvider {
	return registryProvider("cache", func(c *bootstrapv1.BootstrapConfig) *bootstrapv1.Cache { return c.GetCache() }, reg.Build)
}

func storageTestProvider(reg *baldbootstrap.StorageRegistry) segProvider {
	return registryProvider("storage", func(c *bootstrapv1.BootstrapConfig) *bootstrapv1.Storage { return c.GetStorage() }, reg.Build)
}

func aiTestProvider(reg *baldbootstrap.AiRegistry) segProvider {
	return registryProvider("ai", func(c *bootstrapv1.BootstrapConfig) *bootstrapv1.Ai { return c.GetAi() }, reg.Build)
}

func workflowTestProvider(reg *baldbootstrap.WorkflowRegistry) segProvider {
	return registryProvider("workflow", func(c *bootstrapv1.BootstrapConfig) *bootstrapv1.Workflow { return c.GetWorkflow() }, reg.Build)
}

func brokerTestProvider(reg *baldbootstrap.BrokerRegistry) segProvider {
	return registryProvider("broker", func(c *bootstrapv1.BootstrapConfig) *bootstrapv1.Broker { return c.GetBroker() }, reg.Build)
}

func registrarTestProvider(reg *baldbootstrap.RegistrarRegistry) segProvider {
	return registryProvider("registry", func(c *bootstrapv1.BootstrapConfig) *bootstrapv1.Registry { return c.GetRegistry() }, reg.Build)
}

// configTestProvider 把 bootstrap.ConfigRegistry 包成 config 段 Provider
// （构造期产层）。
func configTestProvider(reg *baldbootstrap.ConfigRegistry) segProvider {
	return registryProvider("config", func(c *bootstrapv1.BootstrapConfig) *bootstrapv1.BootstrapConfig { return c }, reg.Build)
}

// auditTestProvider 把 AuditProvider 函数值包成 audit 段 Provider（引擎按
// storeProduct 语义全局注入并恢复 prev）。
func auditTestProvider(p AuditProvider) segProvider {
	return segProvider{segs: []string{"audit"}, build: func(ctx context.Context, s ApplyScope) (any, Cleanup, error) {
		a, cleanup, err := p(ctx, s.Config.GetAudit(), s.App)
		return a, Cleanup(cleanup), err
	}}
}
