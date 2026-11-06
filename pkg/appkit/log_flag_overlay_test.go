package appkit

import (
	"context"
	"sync/atomic"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"

	bconf "github.com/kalandramo/bald/bconf"
)

// logFlagCfg 是 §7.39 用例的配置文件：契约里真有一条 slog 后端，
// 文件层取值 info 用来证明「flag 没传时不该被 flag 默认值顶掉」。
const logFlagCfg = "logger:\n  backends:\n    - type: slog\n      slog:\n        level: \"info\"\n"

// logFlagHarness 承载「命令行 → loadConfig → 阶段 B 前置」这条真链的产物，
// 热更用例复用同一个 App 与 cleanup 槽位。
type logFlagHarness struct {
	a    *App
	t    *testing.T
	cfg  *bootstrapv1.BootstrapConfig
	src  *countingLogSrc
	cc   atomic.Pointer[func()]
	spec *Decl
}

func newLogFlagHarness(t *testing.T, args []string) *logFlagHarness {
	t.Helper()
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfgFile := writeCfg(t, logFlagCfg)
	withArgs(t, append([]string{"--config=" + cfgFile}, args...))

	cfg := bconf.NewBootstrap()
	// StrictContract 是 §7.39 的触发条件：修复前 `--log.*` 进合并树 ⇒ 未知键 ⇒
	// loadConfig 直接返回错误（启动终止）。这里必须带着它跑，否则测的是另一条路。
	// 刻意不调 dynamicAddr：与 bconf.NewBootstrap() 预填深相等的段在 L3 段覆盖检查
	// 里算「已消费」（那是框架自己的预填），一改地址就会先在 syncBootstrap 因
	// 「server 无人消费」报错——本用例的主角是 logger 段，不是协议面。
	a := assembleForTest(t, cfg, StrictContract())
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	h := &logFlagHarness{a: a, t: t, cfg: cfg, src: &countingLogSrc{}, spec: &Decl{}}
	lg, cleanup, err := h.src.bootstrapLogger(context.Background())
	if err != nil {
		t.Fatalf("phase A logger: %v", err)
	}
	if cleanup == nil { // 镜像 assemble 的归一化（curCleanup 钩子恒非 nil）。
		cleanup = func() {}
	}
	log.SetLogger(lg)
	h.cc.Store(&cleanup)

	if err := syncBootstrap(h.a, h.cfg, h.spec, h.src, &h.cc); err != nil {
		t.Fatalf("syncBootstrap: %v", err)
	}
	return h
}

// rebuildLevelAt 取第 n 次 Logger 重建收到的契约 logger 段里的 level——
// 运行期取值是否真的走契约，看这个而不是只看契约对象。
func (h *logFlagHarness) rebuildLevelAt(n int) string {
	h.src.mu.Lock()
	defer h.src.mu.Unlock()
	if n >= len(h.src.args) {
		h.t.Fatalf("Logger 重建次数 = %d, want > %d", len(h.src.args), n)
	}
	return h.src.args[n].GetBackends()[0].GetSlog().GetLevel()
}

// TestLogFlagOverlay_ColdStart 钉住 §7.39 修复的冷启动面：
// `--log.*` 不再终止启动，且 CLI 终值落到契约 logger 段、并被重建 Logger 读到。
//
// 断言分三面，缺一面都会把缺陷放回来：
//  1. loadConfig/syncBootstrap 无错——修复前的伤害正是「一用即终止启动」；
//  2. 契约携带 CLI 终值（error/7），未传的键不写进契约（format 仍空）；
//  3. 重建 Logger 的入参就是覆盖后的契约段——否则 CLI 值进了契约但没进运行期，
//     仍是《设计判据》J9 说的「取值载体不止一个」。
func TestLogFlagOverlay_ColdStart(t *testing.T) {
	h := newLogFlagHarness(t, []string{"--log.level=error", "--log.rotate.max-size=7"})

	s := h.cfg.GetLogger().GetBackends()[0].GetSlog()
	if got := s.GetLevel(); got != "error" {
		t.Errorf(`契约 logger.backends[0].slog.level = %q, want "error"（CLI 覆盖未进契约）`, got)
	}
	if got := s.GetRotate().GetMaxSize(); got != 7 {
		t.Errorf(`契约 rotate.max_size = %d, want 7`, got)
	}
	if got := s.GetFormat(); got != "" {
		t.Errorf(`契约 slog.format = %q, want 空（未传的 flag 不该写进契约）`, got)
	}
	if got := h.rebuildLevelAt(1); got != "error" {
		t.Errorf(`重建 Logger 收到的 level = %q, want "error"（运行期取值未走契约）`, got)
	}
}

// TestLogFlagOverlay_NoFlagKeepsFileValue 是反向：命令行不出现 --log.* 时
// 契约必须原样保留文件取值，且不物化多余的 rotate 段。
// 这条挡住「flag 默认值静默压过配置文件」那类反向回归。
func TestLogFlagOverlay_NoFlagKeepsFileValue(t *testing.T) {
	h := newLogFlagHarness(t, nil)

	s := h.cfg.GetLogger().GetBackends()[0].GetSlog()
	if got := s.GetLevel(); got != "info" {
		t.Errorf(`logger.backends[0].slog.level = %q, want "info"（未传 flag 时文件取值生效）`, got)
	}
	if r := s.GetRotate(); r != nil {
		t.Errorf("未传 rotate flag 却物化了 rotate 段：%+v", r)
	}
	// flag 的默认值是 console——没出现过的键绝不能把它写进契约，否则
	// 「传了 --log.level」会连带把文件里的 format 改成 console。
	if got := s.GetFormat(); got != "" {
		t.Errorf(`logger.backends[0].slog.format = %q, want 空（未传 format flag 不该落默认值）`, got)
	}
	if got := h.rebuildLevelAt(1); got != "info" {
		t.Errorf(`重建 Logger 收到的 level = %q, want "info"`, got)
	}
}

// TestLogFlagOverlay_HotReloadKeepsCLI 钉住热更面：配置层改写 logger 段后，
// 命令行上出现过的 --log.* 仍然是终值。
//
// 为什么算「仍然」而不是「重放一次覆盖」：CLI 是操作者的显式意图，优先级高于
// 任何文件/远程层；热更只重算配置层。若这里退化成文件值，等于同一次进程生命
// 周期里出现两种优先级——热更把操作者的显式传参吃回去了。
func TestLogFlagOverlay_HotReloadKeepsCLI(t *testing.T) {
	h := newLogFlagHarness(t, []string{"--log.level=error"})
	before := h.src.calls()

	// 热更快照：文件层把 level 改成 warn——CLI 必须继续压住它。
	hotReload(h.a, h.cfg, h.spec, h.src, &h.cc, map[string]any{
		"logger": map[string]any{
			"backends": []any{
				map[string]any{"type": "slog", "slog": map[string]any{"level": "warn"}},
			},
		},
	})
	if h.src.calls() <= before {
		t.Fatalf("热更未重建 Logger（calls=%d, before=%d）", h.src.calls(), before)
	}
	if got := h.cfg.GetLogger().GetBackends()[0].GetSlog().GetLevel(); got != "error" {
		t.Errorf(`热更后契约 level = %q, want "error"（CLI 覆盖被热更吃回）`, got)
	}
	if got := h.rebuildLevelAt(h.src.calls() - 1); got != "error" {
		t.Errorf(`热更后重建 Logger 收到的 level = %q, want "error"`, got)
	}
}
