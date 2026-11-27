package appkit

// 本文件收拢 assemble（bootstrap.go）里四块可独立成函数的装配逻辑。
// 拆分纪律：只搬「输入/输出闭合」的阶段；前向捕获变量（a / domainPlan /
// domainReconcileKeys / curCleanup）与「注册序即执行序」的 kitOpts 核心序列
// 仍留在 assemble 本体。每块都刻意保留原错误文案与回滚动作，避免把
// 「失败时恢复全局 Logger」「Hook 资源释放」这类顺序敏感副作用悄悄改掉。

import (
	"context"
	"fmt"
	"sync/atomic"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
	log "github.com/kalandramo/bald/log"
)

// installBootLogger 装配阶段 A：装默认 Logger 并打印计划期诊断。
//
// 调用方必须在失败路径沿用本函数已做的「不切全局 Logger」语义；反过来，
// 成功路径的旧 Logger 恢复由 slots.effects 的停机 Effect 承担，这里不再
// 注册任何回滚（否则会双重释放）。
func installBootLogger(logSrc loggerSource, warnings []Diagnostic) (oldLogger log.Logger, bootCleanup func(), err error) {
	oldLogger = log.GetLogger()
	bootLogger, cleanup, err := logSrc.bootstrapLogger(context.Background())
	if err != nil {
		return nil, nil, fmt.Errorf("appkit: bootstrap logger: %w", err)
	}
	// 阶段 A 工厂可返回 nil（bslog 无资源），后续错误回滚、rebuildLogger
	// 兑现旧钩子、停机 Effect 释放均直接调用，不逐处判 nil。
	if cleanup == nil {
		cleanup = func() {}
	}
	log.SetLogger(bootLogger)
	// 计划期诊断在引导 logger 就绪后可见——与 --plan 输出同源（I10）。
	for _, w := range warnings {
		log.Warn(context.Background(), "appkit: provider plan warning",
			"code", w.Code, "subject", w.Subject, "message", w.Message)
	}
	return oldLogger, cleanup, nil
}

// buildConfigLayers 构造期阶段：config 段 Provider 产出配置层。
//
// scope.App 传 nil——此时 App 尚未构造。失败回滚（rollbackBuilds + 恢复旧
// Logger + bootCleanup）刻意留在函数内部：调用方只负责在成功后把
// layersCleanup 登记为 Effect，不重复兑现，否则会二次释放。
func buildConfigLayers(cfg *bootstrapv1.BootstrapConfig, spec *Decl, entries []providerEntry,
	oldLogger log.Logger, bootCleanup func()) (layers []baldconfig.Layer, layersCleanup func(), err error) {
	if len(entries) > 0 {
		built, err := runProviderNodes(context.Background(), entries,
			ApplyScope{Config: cfg, Decl: spec, Role: spec.Role}, nil)
		if err != nil {
			log.SetLogger(oldLogger)
			bootCleanup()
			return nil, nil, fmt.Errorf("appkit: config layers: %w", err)
		}
		for _, r := range built {
			ls, ok := r.product.([]baldconfig.Layer)
			if !ok {
				rollbackBuilds(built, context.Background())
				log.SetLogger(oldLogger)
				bootCleanup()
				return nil, nil, fmt.Errorf("appkit: config provider %v returned %T, want []config.Layer", r.node.Segments, r.product)
			}
			layers = append(layers, ls...)
			if c := noopCleanupFn(r.cleanup); c != nil {
				prev := layersCleanup
				layersCleanup = func() {
					if prev != nil {
						prev()
					}
					c()
				}
			}
		}
	}
	// 显式 ConfigLayers 与契约源层合流：契约层居首（权威来源），显式层追加
	// 在后（同列表内首元素最高，见 ConfigLayers）。不合并即 Serve 静默丢声明。
	layers = append(layers, spec.ConfigLayers...)
	return layers, layersCleanup, nil
}

// appendFrameworkTailOptions 框架停机 Effect 与框架 Option 序列。
//
// 本块顺序敏感：slots.effects 必须最先注册（其内部按 teardownOrder 逆序
// 生成 Effect，回放序即停机序），config-layers 次之，middleware-chain 最后。
// curCleanup 必须按**地址**传入——阶段 B/热更原子换槽后，停机 Effect 要读到
// 最新钩子；若按值传，替换后的后端 cleanup 永不兑现。
func appendFrameworkTailOptions(spec *Decl, cfg *bootstrapv1.BootstrapConfig,
	slots *cleanupSlots, oldLogger log.Logger, curCleanup *atomic.Pointer[func()],
	httpCfg *bootstrapv1.Server_Http, grpcCfg *bootstrapv1.Server_Grpc,
	layersCleanup func(), faces contractFaces) []Option {
	var opts []Option
	// 框架停机 Effect：按计划推导的停机序逆序注册（I5）——回放即停机序
	//（audit-restore 由 TeardownAfter ["*"] 排到最后：stream 尾批审计事件在
	// 指标/span flush 之后收集；无「恢复装配前全局」步骤——全局审计器
	// 已删除）。
	opts = append(opts, slots.effects(oldLogger, curCleanup)...)
	if httpCfg != nil {
		opts = append(opts, Bind("server.http", httpCfg))
	}
	if grpcCfg != nil {
		opts = append(opts, Bind("server.grpc", grpcCfg))
	}
	if spec.Registrar != nil {
		opts = append(opts, Registrar(spec.Registrar))
	}
	if spec.RemoteConfig != nil {
		opts = append(opts, RemoteConfig(spec.RemoteConfig))
	}
	for _, fn := range spec.AfterStart {
		opts = append(opts, AfterStart(fn))
	}
	if spec.ConfigFile != "" {
		opts = append(opts, ConfigFile(spec.ConfigFile))
	}
	if spec.WatchConfigFile {
		opts = append(opts, WatchConfigFile(true))
	}
	if layersCleanup != nil {
		opts = append(opts, effectOpt("appkit:config-layers", func(context.Context) error {
			layersCleanup()
			return nil
		}))
	}
	// 契约钩子建的资源（桥新建的限流器等）：登记为框架 Effect——不进 spec.Effects
	// （那是用户声明面，计划据此投影；框架自有 Effect 与 config-layers 同列）。
	if faces.close != nil {
		opts = append(opts, effectOpt("appkit:middleware-chain", func(context.Context) error {
			return faces.close()
		}))
	}
	return opts
}

// appendBusinessTailOptions 透传业务声明面的「尾部」Option：停机钩子、
// 超时与身份/审计端口、Effects（含 Deferred 分层）、协调器、键监听、
// 能力/组件声明、flag 绑定、严格契约校验。
//
// 纯读 spec、零前向捕获——这是它可整块搬出的前提。注册序即执行序，
// 块内 append 的先后是行为的一部分，调整任何一项都要复核顺序断言。
func appendBusinessTailOptions(spec *Decl) []Option {
	var opts []Option
	for _, fn := range spec.BeforeStop {
		opts = append(opts, BeforeStop(fn))
	}
	for _, fn := range spec.AfterStop {
		opts = append(opts, AfterStop(fn))
	}
	// 机制超时与身份/审计端口同为 Decl 声明面：kitOpts 重建时逐项透传，
	// 漏传即 Serve 路径静默丢声明（与 OnConfigChange 同类）。零值表示未声明，
	// 保留 defaultDecl 的默认超时。
	if spec.EffectTimeout > 0 {
		opts = append(opts, effectTimeoutOpt(spec.EffectTimeout))
	}
	if spec.ComponentTimeout > 0 {
		opts = append(opts, componentTimeoutOpt(spec.ComponentTimeout))
	}
	if spec.Identity != nil {
		opts = append(opts, Identity(spec.Identity))
	}
	if spec.Auditor != nil {
		opts = append(opts, auditorOpt(spec.Auditor))
	}
	for _, e := range spec.Effects {
		// Deferred 分层随声明透传（AR-1）：资源回收类在阶段 5 回放，不回阶段 0。
		if e.Deferred {
			opts = append(opts, deferredEffectOpt(e.Name, e.Undo))
			continue
		}
		opts = append(opts, effectOpt(e.Name, e.Undo))
	}
	for _, r := range spec.Reconciles {
		opts = append(opts, reconcileOpt(r.Name, r.Fn))
	}
	for _, k := range spec.KeyWatches {
		opts = append(opts, onKeyChangeOpt(k.Key, k.Fn))
	}
	if len(spec.Provides) > 0 {
		opts = append(opts, providesOpt(spec.Provides...))
	}
	for _, r := range spec.Requires {
		opts = append(opts, requiresOpt(r.Component, r.Caps...))
	}
	if len(spec.Components) > 0 {
		opts = append(opts, componentsOpt(spec.Components...))
	}
	// 业务配置 flag 绑定：与框架自身的 server.http/server.grpc 绑定同层，
	// 使业务 flag 进入装载 FlagSet 的 flag 层（最高优先级）。
	for _, b := range spec.Binds {
		opts = append(opts, Bind(b.Prefix, b.Opt))
	}
	// 严格契约校验（opt-in，见 StrictContract）：业务段白名单原样透传。
	if spec.StrictContract {
		opts = append(opts, StrictContract(spec.BusinessSections...))
	}
	return opts
}

// finalizeAssembly 装配末期：计划快照、节点协调器与域协调键。
//
// a 在此刻已由 NewApp 构造完成（非前向捕获），故可安全按值传入；其闭包
// 捕获的是同一个 *App 指针。planFrom 失败时不回滚全局 Logger——这是既有
// 行为，重构不得「顺手修」（错误路径的契约钩子释放仍由 assemble 的 defer
// 承担）。
func finalizeAssembly(a *App, spec *Decl, cfg *bootstrapv1.BootstrapConfig, settings map[string]any,
	plan *providerPlan, vp *variantPlan, slots *cleanupSlots) (fullPlan *Plan, reconcileKeys []ResourceKey, err error) {
	fullPlan, err = planFrom(context.Background(), spec, cfg, settings, plan, vp)
	if err != nil {
		return nil, nil, err
	}
	a.installPlan(fullPlan)
	for _, e := range plan.stageB {
		if e.node.Reload != ReloadReconcile {
			continue
		}
		prov, node := e.prov, e.node
		a.nodeReconcilers[node.Key] = func(ctx context.Context, _ *Plan) error {
			product, cleanup, err := prov.Build(ctx, ApplyScope{Config: cfg, Decl: spec, Role: spec.Role, App: a}, node)
			if err != nil {
				return fmt.Errorf("appkit: reconcile provider %v: %w", node.Segments, err)
			}
			return storeProductReplacing(ctx, a, buildResult{prov: prov, node: node, product: product, cleanup: cleanup}, slots)
		}
	}
	// 声明域的可协调节点：协调钩子归域自持（Domain.Reconcile），与 Provider
	// 节点共用同一 NodeReconciler 面——配置变更按同一 Key diff 分发。
	reconcileKeys = registerDomainReconcilers(a, plan, fullPlan)
	return fullPlan, reconcileKeys, nil
}
