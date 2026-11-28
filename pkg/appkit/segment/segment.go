// Package segment 提供段驱动 Provider 的数据式构造：四件套
// （Segments/Apply/Plan/Build）里三个机械方法的缺省由段路径的**段族表**
// 派生，人只写 Build 主体与例外元数据。
//
// 与引擎的关系：本包产出的仍是 appkit.Provider（接口不动——它是装配词汇表），
// 引擎按同一形状消费；std/mech 构造面不受影响。
//
// 段族表的知识来源：对仓内 18 个 contract 包现存节点声明逐字扫描
// （2026-11-28，评审会话）——同族后端的 Phase/Priority/Reload/EffectName
// 完全一致（EffectName 是停机账本的家族共享键，如 broker-clients），
// 这正是「每个包再抄一遍」的 J2 重复税。本表是节点形状的唯一事实源；
// segment_coverage.providerHints 是另一条知识轴（报错指路：段 → 契约包），
// 两者不合并。
//
// 判据：迁到本构造器的 contract 包，其既有测试一字不改仍绿——派生节点与
// 手写字面量逐字段一致（server 族 Reload 零值即 ReloadStatic）。
package segment

import (
	"context"
	"fmt"
	"strings"

	"github.com/kalandramo/bald/pkg/appkit"
)

// BuildFunc 是节点的构造主体，签名与 appkit.Provider.Build 对齐。
type BuildFunc func(ctx context.Context, s appkit.ApplyScope, n appkit.Node) (any, appkit.Cleanup, error)

// familyDefaults 是段族（顶层段名）派生的节点缺省。
type familyDefaults struct {
	phase    appkit.Phase
	priority int
	reload   appkit.ReloadPolicy
	// effect 是停机 Effect 名模板；"%s" 以段路径后缀填充（如
	// instrumentation.otlp → appkit:instrumentation-otlp），空串 = 不声明。
	effect string
	// teardown 是停机后置键（TeardownAfter），与手写 Plan 字面量同形。
	teardown []string
}

// families 是段族表。值逐字来自各 contract 包现行手写 Plan 的实测扫描。
// audit/app 两族（S4）：值逐字取自 pkg/appkit/standard.go 的 stdAuditProvider
// 与 bald-admin appBindingProvider 的手写字面量（不含 Origin/Kind——那是
// 声明者身份位，由调用方经 Kind Opt 显式给）。
var families = map[string]familyDefaults{
	"server":          {phase: appkit.PhaseServe},
	"broker":          {phase: appkit.PhaseBootstrap, priority: appkit.PriorityBroker, reload: appkit.ReloadRestart, effect: "appkit:broker-clients"},
	"cache":           {phase: appkit.PhaseBootstrap, priority: appkit.PriorityCache, reload: appkit.ReloadRestart, effect: "appkit:cache-clients"},
	"registry":        {phase: appkit.PhaseBootstrap, priority: appkit.PriorityRegistry, reload: appkit.ReloadRestart, effect: "appkit:registrar-client"},
	"instrumentation": {phase: appkit.PhaseBootstrap, priority: appkit.PriorityMetrics, reload: appkit.ReloadRestart, effect: "appkit:instrumentation-%s"},
	"storage":         {phase: appkit.PhaseBootstrap, priority: appkit.PriorityStorage, reload: appkit.ReloadRestart, effect: "appkit:storage-clients"},
	"tracer":          {phase: appkit.PhaseBootstrap, priority: appkit.PriorityTracer, reload: appkit.ReloadRestart, effect: "appkit:tracer-shutdown"},
	"client":          {phase: appkit.PhaseBootstrap, priority: appkit.PriorityClient, reload: appkit.ReloadStatic},
	"audit":           {phase: appkit.PhaseBootstrap, priority: appkit.PriorityAudit, reload: appkit.ReloadReconcile, effect: "appkit:audit-restore", teardown: []string{"*"}},
	"app":             {phase: appkit.PhaseBootstrap},
}

// Opt 单字段覆盖派生值（例外族/接管语义用；能派生就不要覆盖）。
type Opt func(*provider)

// Key 覆盖节点键（缺省 = 段路径）。
func Key(k string) Opt { return func(p *provider) { p.node.Key = appkit.ResourceKey(k) } }

// Kind 标注节点类别（server/database/logger/…；缺省不填，与既有手写一致）。
func Kind(k string) Opt { return func(p *provider) { p.node.Kind = k } }

// Segments 覆盖消费段列表（缺省 = [path]；同时消费多段时显式声明）。
// 声明面与节点面同步改——手写四件套里 Segments() 与 Node.Segments 恒一致。
func Segments(paths ...string) Opt {
	return func(p *provider) {
		p.paths = paths
		p.node.Segments = paths
	}
}

// At 覆盖阶段。
func At(ph appkit.Phase) Opt { return func(p *provider) { p.node.Phase = ph } }

// Priority 覆盖同阶段排序键。
func Priority(n int) Opt { return func(p *provider) { p.node.Priority = n } }

// Reload 覆盖热更策略。
func Reload(r appkit.ReloadPolicy) Opt { return func(p *provider) { p.node.Reload = r } }

// Effect 覆盖停机 Effect 名；传空串删除。
func Effect(name string) Opt { return func(p *provider) { p.node.EffectName = name } }

// Opaque 把节点标为计划逃逸舱（内部命令式闭包，期望态不可派生）。
func Opaque() Opt { return func(p *provider) { p.node.Opaque = true } }

// DependsOn 追加资源键依赖（决定 Apply 拓扑序）。
func DependsOn(keys ...string) Opt {
	return func(p *provider) {
		for _, k := range keys {
			p.node.DependsOn = append(p.node.DependsOn, appkit.ResourceKey(k))
		}
	}
}

// TeardownAfter 覆盖停机后置键（缺省从段族表取；audit 族带 ["*"]——
// stream 尾批审计事件在指标/span flush 之后收集）。
func TeardownAfter(keys ...string) Opt {
	return func(p *provider) {
		p.node.TeardownAfter = nil
		for _, k := range keys {
			p.node.TeardownAfter = append(p.node.TeardownAfter, appkit.ResourceKey(k))
		}
	}
}

// provider 是 Builder 产物：自持节点 + 构造闭包，实现 appkit.Provider。
type provider struct {
	paths []string
	node  appkit.Node
	build BuildFunc
}

func (p *provider) Segments() []string { return p.paths }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (p *provider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Plan 返回派生节点（恰好一个，与 contract 包手写形态一致）。
func (p *provider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{p.node}, nil
}

// Build 委托构造主体。
func (p *provider) Build(ctx context.Context, s appkit.ApplyScope, n appkit.Node) (any, appkit.Cleanup, error) {
	return p.build(ctx, s, n)
}

// New 按段路径构造段驱动 Provider：族缺省 + Opt 覆盖。
//
// path 的顶层段名必须命中段族表，否则 panic——接线期即失败（开发期错误），
// 不留到运行期静默错排。新族后端先补段族表再接线。
func New(path string, build BuildFunc, opts ...Opt) appkit.Provider {
	if path == "" {
		panic("segment: New(\"\") — 段路径不能为空")
	}
	if build == nil {
		panic("segment: New(" + path + ") — build 函数不能为 nil")
	}
	head, suffix := path, ""
	if i := strings.Index(path, "."); i >= 0 {
		head, suffix = path[:i], path[i+1:]
	}
	fam, ok := families[head]
	if !ok {
		panic(fmt.Sprintf("segment: New(%q) — 未知段族 %q（不在段族表里；先补段族表或用显式 Opt 声明的构造面另行评审）", path, head))
	}
	p := &provider{
		paths: []string{path},
		node: appkit.Node{
			Key:      appkit.ResourceKey(path),
			Segments: []string{path},
			Phase:    fam.phase,
			Priority: fam.priority,
			Reload:   fam.reload,
		},
		build: build,
	}
	for _, k := range fam.teardown {
		p.node.TeardownAfter = append(p.node.TeardownAfter, appkit.ResourceKey(k))
	}
	if fam.effect != "" {
		p.node.EffectName = strings.ReplaceAll(fam.effect, "%s", suffix)
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// 编译期断言：产物满足引擎词汇表接口。
var _ appkit.Provider = (*provider)(nil)
