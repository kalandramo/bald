// provider_test.go —— 段驱动装配引擎的测试装置。
//
// segProvider 是通用测试 Provider：固定段路径 + Build 闭包，可直接作为
// Option 传入 assembleForTest。stub*Provider 是单段/单子段的产物构造
// 装置——「产物 map[子段名]any 或整段实例」的第三方引擎接入等价形态
// （生产侧范本见 contrib/store-gorm 的 Provider）。
package appkit

import (
	"context"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
	"github.com/kalandramo/bald/registry"
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

// stubDomainProvider 构造单子段的域 Provider（database/cache/storage/ai/
// workflow/broker/registry）：契约段存在时产出 map[string]any{子段名: 实例}，
// 段缺失返回 nil（无产物，引擎跳过该节点产物）。cleanup 在停机时回放。
func stubDomainProvider(seg, sub string, present func(*bootstrapv1.BootstrapConfig) bool, product any, cleanup func()) segProvider {
	return segProvider{segs: []string{seg}, build: func(_ context.Context, s ApplyScope) (any, Cleanup, error) {
		if !present(s.Config) {
			return nil, nil, nil
		}
		return map[string]any{sub: product}, noopCleanup(cleanup), nil
	}}
}

// stubRegistrarProvider 构造 registry 段 Provider：产物是 registry.Registrar。
func stubRegistrarProvider(r registry.Registrar, cleanup func()) segProvider {
	return segProvider{segs: []string{"registry"}, build: func(_ context.Context, s ApplyScope) (any, Cleanup, error) {
		if s.Config.GetRegistry() == nil {
			return nil, nil, nil
		}
		return r, noopCleanup(cleanup), nil
	}}
}

// stubConfigProvider 构造 config 段 Provider（构造期产层）。
func stubConfigProvider(layers []baldconfig.Layer, cleanup func()) segProvider {
	return segProvider{segs: []string{"config"}, build: func(_ context.Context, _ ApplyScope) (any, Cleanup, error) {
		return layers, noopCleanup(cleanup), nil
	}}
}

// auditTestProvider 把 AuditProvider 函数值包成 audit 段 Provider（引擎按
// storeProduct 语义全局注入并恢复 prev）。
func auditTestProvider(p AuditProvider) segProvider {
	return segProvider{segs: []string{"audit"}, build: func(ctx context.Context, s ApplyScope) (any, Cleanup, error) {
		a, cleanup, err := p(ctx, s.Config.GetAudit(), s.App)
		return a, Cleanup(cleanup), err
	}}
}
