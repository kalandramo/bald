package codegen

import (
	"fmt"
	"go/format"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// appTmpl 是应用装配骨架模板（P12 第一步，见 docs/devel/zh-CN/架构优化路线.md §3）。
//
// 素材来源：kalandramo/bald-admin M10 落地后的真实装配形状——覆盖 appkit 全部
// 第二轮原语的推荐用法：S1 能力声明、T1 效应账本、C1 组件生命周期、R1 key 级
// 热更新订阅、bundle 横切接线注释。生成物为可编译的单文件 main.go，业务在
// 「填充点」注释处填入自己的 server/biz 即可。
//
// 设计原则：生成物只做装配、不含业务逻辑；每个原语都带一行「为什么」注释——
// 生成的代码就是最佳实践文档。
var appTmpl = `// {{.Name}} 应用装配骨架（bald gen app 生成）。
//
// 装配约定（对照 kalandramo/bald-admin M10 形状）：
//   - 填充点标记为 [FILL]：servers / 业务 biz / 组件实现，其余保持默认即合法。
//   - 停机五阶段由 appkit 自动编排：效应回放→BeforeStop→Server.Stop→AfterStop→
//     组件逆序 Dispose，无需手写任何清理顺序逻辑。
//   - 能力声明（Provides/Requires）让缺依赖在启动期 fail-fast，而非运行时 nil panic。
package main

import (
	"context"
	"os"
	"time"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/middleware/bundle"
	"github.com/kalandramo/bald/transport"
)

func main() {
	app := newApp()
	if err := app.Run(context.Background()); err != nil {
		os.Exit(1)
	}
}

func newApp() *appkit.AppKit {
	return appkit.New(
		appkit.Name("{{.Name}}"),
		appkit.Version("v0.1.0"),
		appkit.StopTimeout(15*time.Second),

		// 本地配置（configs/{{.Name}}.yaml）；flag/env/远程四源合并由 pkg/config 完成。
		appkit.ConfigFile("configs/{{.Name}}.yaml"),

		// ---- S1 能力声明：启动期 fail-fast ----
		// 声明本进程将建立的能力（BeforeStart 建立连接/初始化后即算提供），
		// 依赖方声明 Requires；漏写 Provides 时 Run 启动即报：
		//   unresolved capabilities: capability "db" (required by audit.store)
		appkit.Provides("db"),
		appkit.Requires("audit.store", "db"),

		// ---- T1 效应账本：全局写入配套逆操作 ----
		// 任何全局注册（RegisterTenant/SetAuditor...）都应
		// 配一条 Effect；停机阶段 0 逆序回放，e2e 测试用 UndoEffects 隔离全局状态。
		appkit.Effect("tenant-registration", func(ctx context.Context) error {
			// store.UnregisterTenant("tenant_id") // 与 RegisterTenant 配对的逆操作
			_ = ctx
			return nil
		}),

		// ---- C1 进程内组件：统一生命周期 ----
		// trace provider / metrics exporter / 审计后端连接等基础设施注册为组件，
		// 停机阶段 4 逆序 Dispose——「trace 忘 flush 丢批」类问题从文档变成结构保证。
		appkit.Components(
			appkit.ComponentFunc("trace.provider", func(ctx context.Context) error {
				// [FILL] 返回 obtrace/otel TracerProvider 的 Shutdown
				_ = ctx
				return nil
			}),
		),

		// ---- R1 配置细粒度热更新 ----
		// 与 OnConfigChange 全量重载互补：仅当该 key 值确实变化才触发。
		appkit.OnKeyChange("http.addr", func(old, new string) {
			// [FILL] 定点响应地址变更（记日志/重建 listener）
		}),

		// ---- servers（[FILL]）----
		// appkit.Servers(httpSrv, grpcSrv),
	)
}

// buildBundle 演示横切关注点接线（P10）：一次构造、双传输产出、链序由框架固化。
// gin 侧 router.Use(b.Gin()...)；gRPC 侧 grpcserver.NewGRPCServerWithRegister(cfg, b.GRPCChain(), ...).
// 依赖用构造器注入（替代全局 Set*）；Normalized() 一键启用 P9 归一化。
func buildBundle() *bundle.Bundle {
	return bundle.New(
		// bundle.Authn(authenticator),
		// bundle.Authz(authorizer),
		// bundle.Audit(auditor),
		// bundle.Metrics(recorder),
		bundle.Normalized(),
	)
}

// buildServers 演示 server 装配（[FILL] 业务 handler 后启用）。
func buildServers() []transport.Server {
	b := buildBundle()
	_ = b.Gin()        // gin 中间件链
	_ = b.GRPCChain()  // gRPC ServerOption
	return nil // [FILL] return []transport.Server{httpSrv, grpcSrv}
}
`

// appOptions 是 gen app 叶子命令的领域配置（模板模式 / AppSpec 方言模式共用）。
type appOptions struct {
	Name    string // 位置参数：应用名（spec 模式可省，以 AppSpec.meta.name 为准）
	OutFile string // --out（不可命名 Out，避免遮蔽 IOStreams.Out）
	Module  string // --module
	Spec    string // --spec：AppSpec JSON 路径，开启 P12 第二步方言生成
	Force   bool   // --force：覆盖已存在的目标文件（默认拒绝）

	IOStreams
}

// Complete 回填位置参数与派生默认值。spec 模式下输出路径以 AppSpec.meta.name
// 为准（runGenAppSpec 内决定），此处不预填。
func (o *appOptions) Complete(args []string) error {
	if len(args) > 0 {
		o.Name = args[0]
	}
	if o.Spec == "" && o.OutFile == "" && o.Name != "" {
		o.OutFile = filepath.Join("cmd", o.Name, "main.go")
	}
	_ = o.Module // 模板 import 固定为 bald 核心路径；业务 module 替换留给 go mod edit
	return nil
}

// Validate 校验必填项（模板模式必须给应用名；spec 模式由 cobra Args 放行）。
func (o *appOptions) Validate() error {
	if o.Spec == "" && o.Name == "" {
		return fmt.Errorf("the app name is required (or pass --spec <AppSpec.json>)")
	}
	return nil
}

// Run 纯执行：spec 非空走 P12 第二步方言生成，否则渲染硬编码模板骨架。
func (o *appOptions) Run() error {
	if o.Spec != "" {
		return runGenAppSpec(o.Spec, o.OutFile, o.Force, o.IOStreams)
	}
	raw, err := renderTmpl("app", appTmpl, map[string]string{"Name": o.Name})
	if err != nil {
		return err
	}
	formatted, err := format.Source(raw)
	if err != nil {
		return err
	}
	if err := writeFileGuarded(o.OutFile, formatted, o.Force, o.IOStreams); err != nil {
		return err
	}
	fmt.Fprintln(o.Out, "next: fill [FILL] points, then `go mod edit` your module imports if needed")
	return nil
}

// genAppCmd 生成应用装配骨架 main.go（P12：第一步模板生成 + 第二步 AppSpec 方言驱动）。
//
// 两种模式：
//   - gen app <name>                       → 第一版硬编码模板骨架（含 [FILL] 填充点）
//   - gen app <name> --spec <AppSpec.json> → 第二版方言驱动：AppSpec 单一真相源，
//     依 ServerSpec/ComponentSpec/CapabilitySpec/audit_backends 装配 appkit 全原语。
func genAppCmd(streams IOStreams) *cobra.Command {
	o := &appOptions{IOStreams: streams}
	cmd := &cobra.Command{
		Use:   "app [name]",
		Short: "生成应用装配骨架 main.go（appkit 全原语 + bundle 接线）",
		Long: `生成可编译的 cmd/<name>/main.go 装配骨架，覆盖 appkit 第二轮全部原语的推荐用法：

  - S1 能力声明（Provides/Requires，启动期 fail-fast）
  - T1 效应账本（Effect 登记全局写入的逆操作）
  - C1 组件生命周期（Components + ComponentFunc）
  - R1 配置 key 级热更新订阅（OnKeyChange）
  - P10 bundle 横切接线示例（buildBundle/buildServers）

填充点标记为 [FILL]。素材对照 kalandramo/bald-admin M10 装配形状。

指定 --spec <AppSpec.json> 时切换为 P12 第二步：以 AppSpec 方言（proto 单一真相源）
驱动装配，取代本模板的硬编码字段；此时 <name> 可省略——应用名与默认输出路径
（cmd/<AppSpec.meta.name>/main.go）均以 AppSpec 为准。`,
		Args: func(_ *cobra.Command, args []string) error {
			if o.Spec == "" && len(args) != 1 {
				// 模板模式必须给应用名（决定 cmd/<name>/main.go）。
				return fmt.Errorf("accepts 1 arg(s) (the app name), received %d", len(args))
			}
			if len(args) > 1 {
				return fmt.Errorf("accepts at most 1 arg(s), received %d", len(args))
			}
			return nil
		},
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if err := o.Complete(args); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			return o.Run()
		},
	}
	cmd.Flags().StringVar(&o.OutFile, "out", "", "输出文件（默认 cmd/<name>/main.go）")
	cmd.Flags().StringVar(&o.Module, "module", "", "业务 module 路径（提示用）")
	cmd.Flags().StringVar(&o.Spec, "spec", "", "AppSpec JSON 路径（protojson），开启 P12 第二步方言生成")
	cmd.Flags().BoolVar(&o.Force, "force", false, "覆盖已存在的目标文件（默认拒绝）")
	return cmd
}

// runGenAppSpec 读取 AppSpec 并用 appspec 模板渲染（P12 第二步）。
func runGenAppSpec(spec, out string, force bool, streams IOStreams) error {
	raw, err := os.ReadFile(spec)
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	as := &appspecv1.AppSpec{}
	if err := protojson.Unmarshal(raw, as); err != nil {
		return fmt.Errorf("parse AppSpec (protojson): %w", err)
	}
	if out == "" {
		// 未显式给 --out：默认 cmd/<AppSpec.meta.name>/main.go（与 spec 内命名一致，
		// 不取 CLI 位置参数——spec 方言下应用名由 AppSpec 决定）。
		out = filepath.Join("cmd", as.Meta.GetName(), "main.go")
	}
	data := appspecData{
		Meta:             as.Meta,
		Server:           as.Server,
		Capability:       as.Capability,
		Components:       as.Components,
		AuditBackends:    as.AuditBackends,
		BundleNormalized: as.BundleNormalized,
	}
	return renderAppSpec(data, out, force, streams)
}
