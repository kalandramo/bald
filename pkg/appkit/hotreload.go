// 运行期配置协调域（ADR-8 拆分自 bootstrap.go）：syncBootstrap 是阶段 B 前置
// 的契约装载+校验+Logger 重建；hotReload 是热更新路径的同构重放；两者共享
// composeOnConfigChange（框架与用户回调的组合）与 rebuildLogger（Logger 换芯）。
package appkit

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
	"github.com/kalandramo/bald/log"
	"google.golang.org/protobuf/proto"
)

// syncBootstrap 装载配置快照进契约、校验并重建 Logger（阶段 B 前置框架步骤
// ——必须在校验后重建，构造用的契约值已含文件/env 覆盖；registry 段的注册
// 中心构造已 Provider 化，在阶段 B 循环按 canonical 序进行）。
func syncBootstrap(a *App, cfg *bootstrapv1.BootstrapConfig, spec *Decl, logSrc loggerSource, curCleanup *atomic.Pointer[func()]) error {
	m := a.Settings()
	if m == nil {
		return errors.New("appkit: settings snapshot is nil")
	}
	if err := baldconfig.Unmarshal(m, cfg); err != nil {
		return fmt.Errorf("appkit: unmarshal bootstrap config: %w", err)
	}
	if err := bconf.Validate(cfg); err != nil {
		return fmt.Errorf("appkit: validate bootstrap config: %w", err)
	}
	// CLI 覆盖落到契约（§7.39）：`--log.*` 的键名不是契约路径，故不进合并树，而在这里
	// 按 Changed 写进 logger 段。必须在 compilePlan 与 Logger 重建之前——计划摘要与实建
	// 后端都得反映终值。
	ApplyLogFlagOverrides(cfg, a.cfg.flagSet)
	// 以合并后的契约（文件/env/flag/层）重算并安装计划：配置变更 diff 的
	// 基线必须是真实生效的契约，而非装配前的预装载形态。
	next, err := compilePlan(context.Background(), spec, cfg, m)
	if err != nil {
		return err
	}
	a.installPlan(next)
	if err := rebuildLogger(cfg, logSrc, curCleanup); err != nil {
		return err
	}
	for _, n := range next.Nodes {
		if n.Kind == "logger" {
			a.markAppliedByKey(n.Key)
			break
		}
	}
	return nil
}

// composeOnConfigChange 把框架热更新与用户全量回调合到单一 OnConfigChange
// 槽位（Decl 该槽单写）。框架先跑：试装载/校验/计划切换/Logger 重建；随后
// 用户回调才看到契约终值与新 Logger。框架拒绝候选配置时用户回调仍执行——
// 用户可能管理契约之外的段，且 key 订阅与协调器同样按配置存储的新值分发。
func composeOnConfigChange(framework, user func(map[string]any)) func(map[string]any) {
	if user == nil {
		return framework
	}
	return func(m map[string]any) {
		framework(m)
		user(m)
	}
}

// hotReload 热更新：默认值试装载 + 校验 + 计划重算，通过后整契约原子落盘、
// 重建 Logger 并按计划差异协调节点。任一步失败都降级记日志、保留旧
// 契约，不阻断热更新流程（与审计旁路同哲学）。
// Unmarshal 只做结构转换、不校验取值，坏值必须由 Validate 拦在落盘之前。
//
// candidate 从「默认值 + 全量设置快照」重建，而不是克隆当前契约再合并：
// settings 是 Store 的完整合并结果（等价于冷启动的输入），而 proto3 标量无
// presence，合并进旧契约时零值（false/0/""）无法覆盖旧的非零值——热更把
// 开关从 true 改回 false 会静默失效。从默认值重建则天然是「按新快照求值」。
func hotReload(a *App, cfg *bootstrapv1.BootstrapConfig, spec *Decl, logSrc loggerSource, curCleanup *atomic.Pointer[func()], m map[string]any) {
	const msg = "appkit: config hot-reload"
	ctx := context.Background()
	lg := log.GetLogger()

	candidate := bconf.NewBootstrap()
	if err := baldconfig.Unmarshal(m, candidate); err != nil {
		lg.Error(ctx, msg, "err", err)
		return
	}
	if err := bconf.Validate(candidate); err != nil {
		lg.Error(ctx, msg, "err", err)
		return
	}
	// 与冷启动同一处语义：CLI 覆盖在校验后、计划重算前落到候选契约（§7.39）。
	// 每次热更都要重新施加——候选契约是从合并树全新反序列化的，flag 不在树里，
	// 少这一次调用就会让第一次热更之后 `--log.*` 静默失效（配了以为生效，J7 那类）。
	ApplyLogFlagOverrides(candidate, a.cfg.flagSet)
	// 计划重算在落盘前完成：新契约若无法编译（段覆盖/能力冲突等），整次
	// 热更新被拒绝，旧契约与旧计划保持一致（不做半生效）。
	next, err := compilePlan(ctx, spec, candidate, m)
	if err != nil {
		lg.Error(ctx, msg, "err", err)
		return
	}
	// 原子落盘：整体替换 cfg 内容。禁止 `*cfg = *candidate`——proto 消息
	// 内嵌 MessageState（含 sync.Mutex），按值赋值会复制锁（vet copylocks）
	// 且破坏其 lazy 状态。Reset+Merge 是 protobuf 规范的整体替换写法；
	// candidate 为本函数私有副本（落盘后即弃），深拷贝成本可忽略。
	cfg.Reset()
	proto.Merge(cfg, candidate)

	// logger 走专用两阶段/热更协议：重建成功才落账已应用摘要，失败由 Drift
	// 如实报 Stale。
	loggerOK := rebuildLogger(cfg, logSrc, curCleanup) == nil
	if !loggerOK {
		lg.Error(ctx, msg, "err", errors.New("logger rebuild failed; drift will report stale"))
	}
	a.applyPlanChange(ctx, next, m)
	if loggerOK {
		for _, n := range next.Nodes {
			if n.Kind == "logger" {
				a.markAppliedByKey(n.Key)
				break
			}
		}
	}
}

// rebuildLogger 按契约 logger 段重建全局 Logger，并原子替换释放钩子。
//
// 换后端时旧 cleanup 被同步兑现（先切全局句柄、再冲刷旧后端）：带缓冲的
// 后端（loki batchSize 未满的尾批）在热更新切换时不丢日志。旧 cleanup 若
// 阻塞（远端不可达）由后端自身的 flush 超时上界保护。
func rebuildLogger(cfg *bootstrapv1.BootstrapConfig, logSrc loggerSource, curCleanup *atomic.Pointer[func()]) error {
	lg, cleanup, err := logSrc.contractLogger(context.Background(), cfg)
	if err != nil {
		return fmt.Errorf("appkit: build logger: %w", err)
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	// 先切换全局句柄：新日志直接进新后端，旧后端缓冲就此封闭。
	log.SetLogger(lg)
	// 再兑现旧后端 cleanup（阶段 A 钩子为 noop，首次调用无害）。
	if c := curCleanup.Load(); c != nil {
		(*c)()
	}
	curCleanup.Store(&cleanup)
	return nil
}
