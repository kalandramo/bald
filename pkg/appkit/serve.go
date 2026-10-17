package appkit

// serve.go 是单一装配入口（装配层评估 §4.1 / D1-target-appkit.md §1）。
//
// Serve 内部完成「--config 解析 → 契约装载 → 命名空间解析 → 约定装配
// （assemble，bootstrap.go）→ Run（信号处理内建）」；MustServe 是 main 用的
// 薄壳：Serve 失败记日志后以非零码退出。原 FromBootstrap 已删除，其函数体
// 整体迁移为 assemble（内部实现，段驱动 Provider 化是下一波 W2b）。
//
// --config 内化的参考实现是 bald-admin preloadBootstrap 手写的 pflag 扫描
// （bald-admin/backend/cmd/bald-admin/app/registries.go）：flag 名 --config、
// 默认值取 ConfigFile 声明路径（与 loadConfig 的 --config 注册同一默认——
// 两阶段装载命中同一文件）、ContinueOnError + 未知 flag 白名单 + 解析错误忽略。
// 契约装载 = bconf.NewBootstrap 骨架 + 配置文件/env 基线预装载（文件与 Run 期
// loadConfig 命中同一文件——两阶段装载同源是启动正确性的前提；ConfigRegistry
// 的 Build 在装配期消费契约 config 段的引导信息，预装载使其在构造期可得）。

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
// 唯一装配入口：内部完成 --config 解析 → 契约装载（骨架 + 配置文件/env 基线
// 预装载）→ 命名空间解析 → 约定装配（段驱动 Provider 循环，见 provider.go/
// bootstrap.go；框架自有后端经 std.Standard() 声明）→ Run。业务只经 Option
// 声明配置表达不了的能力（WithHTTP/WithGRPC/钩子/第三方 Provider 等）。
//
// 命名空间必须经 ConfigNamespace 显式声明（原因见其注释——env 前缀自指）；
// 两遍合并修自指、删除该必填项是批 3（B6）的工作，本波保留必填约束。
//
// 运行期行为（信号处理、优雅停机、防重入）与 App.Run 一致。
func Serve(ctx context.Context, opts ...Option) error {
	d := newDecl(opts...)

	// 配置命名空间必须显式声明（ConfigNamespace）：契约 app.name 由配置
	// 装载填充，若以它派生 env 前缀则是自指——配置里的 app.name 会改变「能读
	// 到哪些环境变量」，症状为 <原前缀>_* env 覆盖静默失效（回落文件值，无报
	// 错）。不回退、大声失败。文案与原 FromBootstrap 检查逐字一致（测试钉住）。
	if d.ConfigNamespace == "" {
		return errors.New("appkit: config namespace not declared: " +
			"app.name is loaded from configuration and would be read before namespace " +
			"resolution (self-referential env prefix); " +
			`declare it explicitly: appkit.ConfigNamespace("<name>")`)
	}

	// --config 解析：内化业务侧手写 pflag 扫描（见文件头）。解析结果写回
	// ConfigFile，使 Run 期 loadConfig 的 --config 注册与预装载同源。
	d.ConfigFile = parseConfigFlag(d.ConfigFile)

	// 契约装载：bconf.NewBootstrap 骨架 + 配置文件/env 基线预装载。预装载是
	// 无 flag 层、无契约源层的基线装载，仅使契约 config 段的引导信息（远程源
	// 地址/凭据/dataId）在装配期可得；完整装载仍由 Run 期 loadConfig +
	// BeforeStart 的 Unmarshal 负责（合并结果覆盖同一契约指针），两阶段无冲突。
	cfg := bconf.NewBootstrap()
	if err := preloadContract(cfg, d.ConfigNamespace, d.ConfigFile); err != nil {
		return err
	}

	a, err := assemble(d, cfg)
	if err != nil {
		return err
	}
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

// parseConfigFlag 扫描 os.Args 解析 --config（bald-admin preloadBootstrap 的
// 行为内化，与 loadConfig 的 --config 注册同款容错）：ContinueOnError +
// 未知 flag 白名单 + 解析错误忽略（`_ =`）——未知/写错的 flag 不阻断启动，
// 由各自的消费方（业务 flag、cobra）负责报错。
func parseConfigFlag(defaultPath string) string {
	fs := pflag.NewFlagSet("bald", pflag.ContinueOnError)
	fs.ParseErrorsWhitelist.UnknownFlags = true
	var cfgFile string
	fs.StringVar(&cfgFile, "config", defaultPath, "config file")
	_ = fs.Parse(os.Args[1:])
	return cfgFile
}

// preloadContract 预装载配置基线进契约：baldconfig.Load（本地文件 + env，
// 不含 flag/远程——业务 flag 此时未注册，远程经 ConfigRegistry 在装配期
// Build）+ Unmarshal。文件缺失且未显式声明路径时按命名空间规则发现，找不到
// 则静默跳过文件层（discoverLocalFile 语义，与 loadConfig 一致）；显式路径
// 缺失 fail-fast。预装载 store 无 watch，Unmarshal 后立即释放。
func preloadContract(cfg *bootstrapv1.BootstrapConfig, ns, cfgFile string) error {
	s, err := baldconfig.Load(baldconfig.Options{Name: ns, ConfigFile: cfgFile})
	if err != nil {
		return fmt.Errorf("appkit: preload config: %w", err)
	}
	defer s.Close()
	if err := s.Unmarshal(cfg); err != nil {
		return fmt.Errorf("appkit: preload bootstrap config: %w", err)
	}
	return nil
}
