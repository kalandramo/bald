// drift.go 实现节点级重载语义、配置变更的计划重算与漂移检测。
//
// 三种 ReloadPolicy 的边界（见 plan.go）：
//   - ReloadReconcile：框架在配置变更时重建节点实际态，使实际态追上期望态；
//   - ReloadRestart：明确告知需要进程重启（诊断 + 审计事件），不假装已生效；
//   - ReloadStatic：默认静默；仅当依赖集合变化时提示重启。
//
// Drift 是「期望态 vs 实际态」的只读视图：计划里的可协调节点若从未落账或
// 已落账摘要与当前期望不符，分别报 Missing / Stale；协调器挂载的组件若不在
// 计划里则报 Unexpected。Drift 不触发协调，供测试、doctor 与运维探针使用。
package appkit

import (
	"context"
	"sort"
	"strings"

	"github.com/kalandramo/bald/log"
)

// NodeReconciler 把一个可协调节点的实际态重建到 next 计划对应的期望态。
// 必须幂等：重复调用朝同一终态收敛；失败时不更新已应用摘要（Drift 保持
// Stale，下次变更/显式协调可重试）。
type NodeReconciler func(ctx context.Context, next *Plan) error

// NodeChange 是单个节点在两次计划之间的差异。
type NodeChange struct {
	Key         ResourceKey  `json:"key"`
	Kind        string       `json:"kind"`
	Policy      ReloadPolicy `json:"policy"`
	From        string       `json:"from,omitempty"`
	To          string       `json:"to,omitempty"`
	Added       bool         `json:"added,omitempty"`
	Removed     bool         `json:"removed,omitempty"`
	DepsChanged bool         `json:"depsChanged,omitempty"`
}

// ImperativeChange 是命令式槽位（effect/keyWatch/reconcile）在两次计划之间的
// 差异。闭包本身不可比，只报槽位身份——计划缓存要回答「装配变了什么」，槽位
// 的增删同样改变运行期行为。
type ImperativeChange struct {
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	Added   bool   `json:"added,omitempty"`
	Removed bool   `json:"removed,omitempty"`
}

// PlanChange 是一次计划重算的差异汇总（Restart 是明确需要重启的节点键）。
type PlanChange struct {
	Changes    []NodeChange       `json:"changes,omitempty"`
	Imperative []ImperativeChange `json:"imperative,omitempty"`
	Restart    []ResourceKey      `json:"restart,omitempty"`
}

// Empty 报告两次计划无实质差异（节点与命令式槽位都没有增删改）。
func (c PlanChange) Empty() bool {
	return len(c.Changes) == 0 && len(c.Imperative) == 0
}

// diffPlan 按节点 Key 对比新旧计划：新增/移除/期望态或依赖变化。节点顺序
// 不影响结果（按 Key 排序输出）。命令式槽位按 (Kind, Name) 单独对比——闭包
// 不可比，但槽位增删同样改变装配行为。
func diffPlan(old, next *Plan) PlanChange {
	oldByKey := map[ResourceKey]Node{}
	if old != nil {
		for _, n := range old.Nodes {
			oldByKey[n.Key] = n
		}
	}
	nextByKey := map[ResourceKey]Node{}
	if next != nil {
		for _, n := range next.Nodes {
			nextByKey[n.Key] = n
		}
	}
	keys := make([]ResourceKey, 0, len(oldByKey)+len(nextByKey))
	for k := range oldByKey {
		keys = append(keys, k)
	}
	for k := range nextByKey {
		if _, ok := oldByKey[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	var out PlanChange
	for _, k := range keys {
		o, inOld := oldByKey[k]
		n, inNew := nextByKey[k]
		switch {
		case !inOld:
			out.Changes = append(out.Changes, NodeChange{
				Key: k, Kind: n.Kind, Policy: n.Reload, To: n.DesiredHash, Added: true,
			})
		case !inNew:
			out.Changes = append(out.Changes, NodeChange{
				Key: k, Kind: o.Kind, Policy: o.Reload, From: o.DesiredHash, Removed: true,
			})
		default:
			deps := !equalKeys(o.DependsOn, n.DependsOn)
			if o.DesiredHash == n.DesiredHash && o.Reload == n.Reload && !deps {
				continue
			}
			out.Changes = append(out.Changes, NodeChange{
				Key: k, Kind: n.Kind, Policy: n.Reload,
				From: o.DesiredHash, To: n.DesiredHash, DepsChanged: deps,
			})
		}
	}
	out.Imperative = diffImperative(old, next)
	return out
}

// diffImperative 对比命令式槽位（按 (Kind, Name) 身份）。输入副本先排序：
// 缓存文件可能被手工编辑，不假设 Plan.Imperative 的排序前提。
func diffImperative(old, next *Plan) []ImperativeChange {
	var oldSlots, nextSlots []Imperative
	if old != nil {
		oldSlots = append(oldSlots, old.Imperative...)
	}
	if next != nil {
		nextSlots = append(nextSlots, next.Imperative...)
	}
	byKey := func(s []Imperative) {
		sort.Slice(s, func(i, j int) bool { return s[i].Key() < s[j].Key() })
	}
	byKey(oldSlots)
	byKey(nextSlots)
	var out []ImperativeChange
	i, j := 0, 0
	for i < len(oldSlots) || j < len(nextSlots) {
		switch {
		case j >= len(nextSlots) || (i < len(oldSlots) && oldSlots[i].Key() < nextSlots[j].Key()):
			out = append(out, ImperativeChange{Kind: oldSlots[i].Kind, Name: oldSlots[i].Name, Removed: true})
			i++
		case i >= len(oldSlots) || nextSlots[j].Key() < oldSlots[i].Key():
			out = append(out, ImperativeChange{Kind: nextSlots[j].Kind, Name: nextSlots[j].Name, Added: true})
			j++
		default:
			i++
			j++
		}
	}
	return out
}

// applyPlanChange 在配置变更后应用计划差异：协调可协调节点、显式标记需重启
// 节点（含从计划消失的节点——协调器没有拆除动作）、更新计划快照，最后驱动
// 业务协调器（期望态来自新配置）。
//
// 协调失败不回滚已生效的配置（与 R1 收敛语义一致）：摘要不更新，Drift 报
// Stale，下次变更或显式协调继续补齐。
func (a *App) applyPlanChange(ctx context.Context, next *Plan, settings map[string]any) PlanChange {
	if next == nil {
		return PlanChange{}
	}
	a.planMu.Lock()
	change := diffPlan(a.plan, next)
	a.plan = next
	reconcilers := make(map[ResourceKey]NodeReconciler, len(a.nodeReconcilers))
	for k, v := range a.nodeReconcilers {
		reconcilers[k] = v
	}
	a.planMu.Unlock()

	for _, ch := range change.Changes {
		switch ch.Policy {
		case ReloadReconcile:
			if ch.Removed {
				// 节点从计划消失 = 拓扑变化：协调器没有「拆除节点」的动作，旧
				// 资源会存活到进程退出。如实报重启，不假装已收敛。
				a.setAppliedHash(ch.Key, "")
				change.Restart = append(change.Restart, ch.Key)
				log.Warn(ctx, "appkit: config change removed node; restart required to release its resources",
					"node", string(ch.Key), "kind", ch.Kind)
				a.auditReconcile(ctx, "restart-required", string(ch.Key), nil)
				continue
			}
			if ch.Kind == "logger" {
				// logger 走专用热更协议（hotReload 已重建并按结果落账摘要）。
				continue
			}
			fn := reconcilers[ch.Key]
			if fn == nil {
				if ch.Kind == "component" {
					// 组件期望态由代码声明，配置变更不改成员；启动时已落账。
					continue
				}
				log.Warn(ctx, "appkit: reconcile node has no reconciler; drift will report stale",
					"node", string(ch.Key), "kind", ch.Kind)
				continue
			}
			if err := fn(ctx, next); err != nil {
				log.Error(ctx, "appkit: reconcile node failed; keeping previous applied state",
					"node", string(ch.Key), "error", err)
				a.auditReconcile(ctx, "reconcile", string(ch.Key), err)
				continue
			}
			a.setAppliedHash(ch.Key, ch.To)
			a.auditReconcile(ctx, "reconcile", string(ch.Key), nil)
		case ReloadRestart:
			change.Restart = append(change.Restart, ch.Key)
			log.Warn(ctx, "appkit: config change requires process restart",
				"node", string(ch.Key), "kind", ch.Kind)
			a.auditReconcile(ctx, "restart-required", string(ch.Key), nil)
		case ReloadStatic:
			if ch.DepsChanged {
				log.Info(ctx, "appkit: static node dependencies changed; restart required",
					"node", string(ch.Key))
			}
		}
	}

	// R1-2：用户协调器以新配置快照执行（配置变更总是触发一次全量协调）。
	a.runReconcilersWith(ctx, settings)
	return change
}

// DriftKind 是漂移类别。
type DriftKind string

const (
	DriftMissing    DriftKind = "missing"    // 期望有、实际无（未落账或组件未启动）
	DriftUnexpected DriftKind = "unexpected" // 实际有、计划无（协调器挂载的组件不在计划）
	DriftStale      DriftKind = "stale"      // 实际态摘要落后于期望态
)

// Drift 是一条漂移记录。
type Drift struct {
	Key     ResourceKey  `json:"key"`
	Kind    DriftKind    `json:"kind"`
	Policy  ReloadPolicy `json:"policy,omitempty"`
	Want    string       `json:"want,omitempty"`
	Have    string       `json:"have,omitempty"`
	Message string       `json:"message,omitempty"`
}

// Drift 报告当前计划的可协调节点与运行态之间的差异。仅在 Run 生效后可用
// （ErrNotRunning）；只读，不触发协调。结果按 (Key, Kind) 稳定排序。
func (a *App) Drift(_ context.Context) ([]Drift, error) {
	if !a.running.Load() {
		return nil, ErrNotRunning
	}
	a.planMu.RLock()
	p := a.plan
	applied := make(map[ResourceKey]string, len(a.applied))
	for k, v := range a.applied {
		applied[k] = v
	}
	a.planMu.RUnlock()
	if p == nil {
		return nil, nil
	}

	planned := make(map[ResourceKey]bool, len(p.Nodes))
	var out []Drift
	for _, n := range p.Nodes {
		planned[n.Key] = true
		if n.Reload != ReloadReconcile {
			continue
		}
		have, ok := applied[n.Key]
		if !ok || !a.reconcileNodePresent(n) {
			out = append(out, Drift{
				Key: n.Key, Kind: DriftMissing, Policy: n.Reload, Want: n.DesiredHash,
				Message: "desired reconcile node is not applied",
			})
			continue
		}
		if have != n.DesiredHash {
			out = append(out, Drift{
				Key: n.Key, Kind: DriftStale, Policy: n.Reload, Want: n.DesiredHash, Have: have,
				Message: "applied state is behind desired state",
			})
		}
	}

	// 协调器挂载的组件若不在计划里 = 逃逸出声明面（Unexpected）。
	a.reconMu.Lock()
	for recon, items := range a.reconItems {
		for _, it := range items {
			key := ResourceKey("component." + it.name)
			if planned[key] {
				continue
			}
			out = append(out, Drift{
				Key: key, Kind: DriftUnexpected, Policy: ReloadReconcile,
				Message: "component mounted by reconciler " + recon + " is not declared in the plan",
			})
		}
	}
	a.reconMu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

// installPlan 记录装配计划快照（启动期）。
func (a *App) installPlan(p *Plan) {
	a.planMu.Lock()
	a.plan = p
	a.planMu.Unlock()
}

// markAppliedByKey 把当前计划中该节点的期望态摘要记为已应用（仅可协调节点）。
// 节点不存在或非可协调节点时 no-op。
func (a *App) markAppliedByKey(key ResourceKey) {
	a.planMu.Lock()
	defer a.planMu.Unlock()
	if a.plan == nil {
		return
	}
	for _, n := range a.plan.Nodes {
		if n.Key != key {
			continue
		}
		if n.Reload == ReloadReconcile && n.DesiredHash != "" {
			a.applied[key] = n.DesiredHash
		}
		return
	}
}

// setAppliedHash 更新节点已应用摘要（空 = 未应用）。
func (a *App) setAppliedHash(key ResourceKey, hash string) {
	a.planMu.Lock()
	defer a.planMu.Unlock()
	if hash == "" {
		delete(a.applied, key)
		return
	}
	a.applied[key] = hash
}

// reconcileNodePresent 报告可协调节点的运行态是否存在（组件看已启动集合；
// 其它节点由已应用摘要代表）。
func (a *App) reconcileNodePresent(n Node) bool {
	if n.Kind != "component" {
		return true
	}
	name := strings.TrimPrefix(string(n.Key), "component.")
	a.startedMu.Lock()
	defer a.startedMu.Unlock()
	for _, c := range a.started {
		if c.Name() == name {
			return true
		}
	}
	return false
}

// equalKeys 比较两个资源键切片（顺序敏感，与 DependsOn 的声明语义一致）。
func equalKeys(a, b []ResourceKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
