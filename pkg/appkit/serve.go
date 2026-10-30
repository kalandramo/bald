package appkit

// serve.go 是单一装配入口。
//
// Serve 内部完成「--config 解析 → 契约两遍预装载（含命名空间派生）→ 约定装配
// （assemble，bootstrap.go）→ Run（信号处理内建）」；MustServe 是 main 用的
// 薄壳：Serve 失败记日志后以非零码退出。
//
// --config 解析：flag 名 --config、
// 默认值取 ConfigFile 声明路径（与 loadConfig 的 --config 注册同一默认——
// 两阶段装载命中同一文件）、ContinueOnError + 未知 flag 白名单 + 解析错误忽略。
//
// --plan 解析走同款容错：--plan 打印装配计划（text）并返回 nil（退出码 0），
// --plan-format=json 输出机读格式；打印发生在任何副作用之前（见 plan.go）。
//
// 配置命名空间（B6）：命名空间（env 前缀 NAME_* 与多环境
// 文件名来源）由**契约 app.name 派生**，不再有显式声明口。
//
//	第一遍：预装载文件/远程层（env 层延迟）→ Unmarshal 解析契约 app.name；
//	第二遍：以 app.name 为前缀挂 env 层（Store.MountEnv）全量重合并 → 契约定形。
//
// 自指由两遍顺序消解：env 前缀在 env 层挂载前已从文件层定死，配置里的
// app.name 不再改变「能读到哪些环境变量」——现有用户的 NAME_ 前缀 env 原样
// 生效。Run 期 loadConfig 以派生命名空间整体装载（含 env 层），与预装载同源。
// 无 --config 时的本地文件发现以 Name Option 为键（缺省 "bald-app"）；依赖
// 命名空间命名文件发现的使用方式改传 --config 或 appkit.Name。

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/pflag"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
	log "github.com/kalandramo/bald/log"
)

// Serve 完成契约驱动的约定装配并阻塞运行到优雅停机。
//
// 唯一装配入口：内部完成 --config 解析 → 契约两遍预装载（文件/远程层 → 解析
// app.name 派生命名空间 → 挂 env 层重合并定形）→ 约定装配（段驱动 Provider
// 循环，见 provider.go/bootstrap.go；框架自有后端经 std.Standard() 声明）→
// Run。业务只经 Option 声明配置表达不了的能力（WithHTTP/WithGRPC/钩子/
// 第三方 Provider 等）。
//
// 契约无 app.name 即 fail-fast（错误指明 app.name 必填）——env 前缀与多环境
// 文件名都由它派生。
//
// 运行期行为（信号处理、优雅停机、防重入）与 App.Run 一致。
func Serve(ctx context.Context, opts ...Option) error {
	d := newDecl(opts...)

	// --config 解析：内化业务侧手写 pflag 扫描（见文件头）。解析结果写回
	// ConfigFile，使 Run 期 loadConfig 的 --config 注册与预装载同源。
	d.ConfigFile = parseConfigFlag(d.ConfigFile)

	// --plan：在任何副作用（阶段 A Logger、Provider Build、钩子）之前打印
	// 装配计划并返回 nil（MustServe 路径据此以 0 退出）。编译复用 compileDecl
	// ——与真实启动同一份声明/契约，预检错误在启动前暴露。
	if out := parsePlanFlag(); out.enabled {
		p, err := compileDecl(ctx, d)
		if err != nil {
			return err
		}
		return writePlan(os.Stdout, p, out.format)
	}

	// 契约两遍预装载：第一遍文件/远程层 → app.name 派生命名空间；第二遍挂
	// env 层全量重合并 → 契约定形（含 env 覆盖）。预装载是无 flag 层、无契约
	// 源层的基线装载，仅使契约引导信息在装配期可得；完整装载仍由 Run 期
	// loadConfig + BeforeStart 的 Unmarshal 负责（合并结果覆盖同一契约指针），
	// 两阶段无冲突。
	cfg := bconf.NewBootstrap()
	ns, settings, err := preloadContract(cfg, d.Name, d.ConfigFile)
	if err != nil {
		return err
	}
	d.configNamespace = ns

	a, err := assemble(d, cfg, settings)
	if err != nil {
		return err
	}
	// 计划缓存：记录启动基线（仅本地文件，失败只告警——缓存不是执行依据）。
	a.recordPlanCache(ctx, d.PlanCache)
	return a.Run(ctx)
}

// MustServe 运行 Serve，失败记日志并以非零码退出（os.Exit）。
// 供 main 使用——main 之外不调用（退出不可返回、不可测试）。
func MustServe(ctx context.Context, opts ...Option) {
	if err := Serve(ctx, opts...); err != nil {
		log.Error(ctx, "appkit: serve failed", "error", err.Error())
		os.Exit(1)
	}
}

// parseConfigFlag 扫描 os.Args 解析 --config（与 loadConfig 的 --config
// 注册同款容错）：ContinueOnError +
// 未知 flag 白名单 + 解析错误忽略（下划线赋值丢弃）——未知/写错的 flag 不阻断
// 启动，由各自的消费方（业务 flag、cobra）负责报错。
func parseConfigFlag(defaultPath string) string {
	fs := pflag.NewFlagSet("bald", pflag.ContinueOnError)
	fs.ParseErrorsWhitelist.UnknownFlags = true
	var cfgFile string
	fs.StringVar(&cfgFile, "config", defaultPath, "config file")
	_ = fs.Parse(os.Args[1:])
	return cfgFile
}

// planFlagOutput 是 --plan 的解析结果（enabled=false 时 Serve 走正常装配）。
type planFlagOutput struct {
	enabled bool
	format  string
}

// parsePlanFlag 扫描 os.Args 解析 --plan / --plan-format（与 --config 同款
// pflag 容错：ContinueOnError + 未知 flag 白名单 + 解析错误忽略）。
func parsePlanFlag() planFlagOutput {
	fs := pflag.NewFlagSet("bald", pflag.ContinueOnError)
	fs.ParseErrorsWhitelist.UnknownFlags = true
	out := planFlagOutput{format: "text"}
	fs.BoolVar(&out.enabled, "plan", false, "print the assembly plan and exit")
	fs.StringVar(&out.format, "plan-format", out.format, "plan output format: text or json")
	_ = fs.Parse(os.Args[1:])
	return out
}

// preloadContract 两遍预装载配置进契约并返回派生的配置命名空间与合并快照：
//
//	第一遍：baldconfig.Load（本地文件 + 远程层，DeferEnv——env 层延迟，前缀
//	依赖尚未知的 app.name）→ Unmarshal 探针契约 → 解析 app.name。文件缺失且
//	未显式声明路径时按 Name 规则发现，找不到则静默跳过文件层（discoverLocalFile
//	语义，与 loadConfig 一致）；显式路径缺失 fail-fast。app.name 缺失 fail-fast
//	且错误可行动（指明必填与供给途径）。
//	第二遍：Store.MountEnv(app.name) 挂 env 层全量重合并 → Unmarshal 正式契约
//	（env 覆盖生效）。预装载 store 无 watch，Unmarshal 后立即释放；返回的快照
//	（含业务段）供计划期 `biz.<path>` 期望态摘要使用（无 flag 层——flag 只在
//	运行期完整装载，计划会在 BeforeStart 以合并后的快照重算）。
func preloadContract(cfg *bootstrapv1.BootstrapConfig, name, cfgFile string) (string, map[string]any, error) {
	s, err := baldconfig.Load(baldconfig.Options{Name: name, ConfigFile: cfgFile, DeferEnv: true})
	if err != nil {
		return "", nil, fmt.Errorf("appkit: preload config: %w", err)
	}
	defer s.Close()

	// 第一遍：探针契约只用于解析 app.name（不落入正式契约，避免二次
	// Unmarshal 的合并语义纠缠）。
	probe := &bootstrapv1.BootstrapConfig{}
	if err := s.Unmarshal(probe); err != nil {
		return "", nil, fmt.Errorf("appkit: preload bootstrap config: %w", err)
	}
	ns := probe.GetApp().GetName()
	if ns == "" {
		return "", nil, errors.New("appkit: bootstrap.app.name is required: the config namespace " +
			"(env prefix NAME_* and multi-env file name) is derived from it; " +
			"set app.name in the config file loaded via --config")
	}

	// 第二遍：env 层按派生前缀挂载，重合并后契约定形。
	s.MountEnv(ns)
	if err := s.Unmarshal(cfg); err != nil {
		return "", nil, fmt.Errorf("appkit: preload bootstrap config: %w", err)
	}
	return ns, s.Settings(), nil
}
