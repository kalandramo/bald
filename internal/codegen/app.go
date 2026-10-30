package codegen

import (
	"fmt"
	"go/format"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// appTmpl 是应用装配骨架模板（MustServe 单入口惯用法）。
//
// 生成物为可编译的单文件 main.go：装配收敛为一次 MustServe 声明式调用——
// 框架自有后端经 std.Standard() 打包注入，业务能力经 Option 声明，填充点
// 标记 [FILL]。素材覆盖 appkit 原语的推荐用法：S1 能力声明、T1 效应
// 账本、C1 组件生命周期、R1 key 级热更新订阅、P10 bundle 横切接线。
//
// 设计原则：生成物只做装配、不含业务逻辑；每个原语都带一行「为什么」注释——
// 生成的代码就是最佳实践文档。
var appTmpl = `// {{.Name}} 应用装配骨架（bald gen app 生成）。
//
// 装配约定（对照 _example/bald/internal/bootstrap/assemble.go）：
//   - 唯一装配入口 MustServe：内部完成 --config 解析 → 契约装载 → 命名空间
//     解析 → 段驱动 Provider 装配（std.Standard() 提供框架自有后端）→ Run
//     （信号处理内建）；失败记日志后以非零码退出。
//   - 停机五阶段由框架自动编排：效应回放→BeforeStop→Server.Stop→AfterStop→
//     组件逆序 Dispose，无需手写任何清理顺序逻辑。
//   - 能力声明（Provides/Requires）让缺依赖在启动期 fail-fast，而非运行时 nil panic。
package main

import (
	"context"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/appkit/mech"
	"github.com/kalandramo/bald/pkg/appkit/std"
	"github.com/kalandramo/bald/pkg/middleware/bundle"
	"github.com/kalandramo/bald/pkg/store"
)

func main() {
	appkit.MustServe(context.Background(), options()...)
}

// options 声明业务能力（配置表达不了的部分）。契约里已有形状的部分
// （server 地址/TLS、日志、app 元数据、配置源）由框架按契约装配，业务
// 无需也不应手写 Bind/server 构造。
func options() []appkit.Option {
	opts := []appkit.Option{
		// 框架自有后端的默认 Provider 集合：server.http/grpc 服务器构造
		// （按能力声明）与 audit 的 log 后端（契约 audit.backends 驱动）。
		std.Standard(),

		// 配置命名空间由契约 app.name 派生（B6 两遍合并）——契约里的 app.name
		// 必填，缺失启动 fail-fast 且报错可行动。
		// appkit.ConfigFile("configs/{{.Name}}.yaml"), // [FILL] 本地配置文件（显式路径缺失启动即报错）

		// ---- S1 能力声明：启动期 fail-fast ----
		// 声明本进程将建立的能力（BeforeStart 建立连接/初始化后即算提供），
		// 依赖方声明 Requires；漏写 Provides 时启动即报：
		//   unresolved capabilities: capability "db" (required by audit.store)
		mech.Provides("db"),
		mech.Requires("audit.store", "db"),

		// ---- 效应账本：全局写入配套逆操作 ----
		// 任何全局注册（RegisterTenant/RegisterDataScope...）都应
		// 配一条 Effect；停机阶段 0 逆序回放，e2e 测试用 UndoEffects 隔离全局状态。
		// 下例：配合业务 BeforeStart 里的 store.RegisterTenant("tenant_id", ...)，
		// 提供对偶的 UnregisterTenant 逆操作——缺了它，注册会泄漏到进程重启之后
		// 不可见、但 e2e 用例之间可互相污染。
		mech.Effect("tenant-registration", func(ctx context.Context) error {
			store.UnregisterTenant("tenant_id") // 与 RegisterTenant 配对的逆操作
			return nil
		}),

		// ---- 进程内组件：统一生命周期 ----
		// trace provider / metrics exporter / 审计后端连接等基础设施注册为组件，
		// 停机阶段 4 逆序 Dispose——「trace 忘 flush 丢批」类问题从文档变成结构保证。
		mech.Components(
			mech.ComponentFunc("trace.provider", func(ctx context.Context) error {
				// [FILL] 返回 obtrace/otel TracerProvider 的 Shutdown
				_ = ctx
				return nil
			}),
		),

		// ---- 配置 key 级热更新 ----
		// 与 OnConfigChange 全量重载互补：仅当该 key 值确实变化才触发。
		mech.OnKeyChange("server.http.addr", func(old, new string) {
			// [FILL] 定点响应地址变更（记日志/重建 listener）
		}),

		// ---- 传输能力与第三方后端（[FILL]）----
		// HTTP：appkit.WithHTTP(newRouter())            // 业务 handler 必供
		// gRPC：appkit.WithGRPC(registerService, chain) // service 注册 + 拦截器链
		// 第三方后端 Provider（加法式，范本 contrib/store-gorm）：
		//   storegorm.AppProvider(), // 契约 database.sql 段 → *gorm.DB
	}
	return opts
}

// buildBundle 演示横切关注点接线：一次构造、双传输产出、链序由框架固化。
// gin 侧 router.Use(b.Gin()...)；gRPC 侧 appkit.WithGRPC(register, b.GRPCChain()...)。
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
`

// appOptions 是 gen app 叶子命令的领域配置（模板模式 / AppSpec 方言模式共用）。
type appOptions struct {
	Name    string // 位置参数：应用名（spec 模式可省，以 AppSpec.meta.name 为准）
	OutFile string // --out（不可命名 Out，避免遮蔽 ioStreams.Out）
	Module  string // --module
	Spec    string // --spec：AppSpec JSON 路径，开启方言生成
	Force   bool   // --force：模板模式覆盖已存在目标文件（默认拒绝）
	Adopt   bool   // --adopt：spec 模式覆盖 owned 生成物（--force 的替代，B5）
	Check   bool   // --check：spec 模式只校验不落盘（I8；有差异即失败）

	ioStreams
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

// Validate 校验必填项与模式互斥（模板模式必须给应用名；spec 模式由 cobra Args 放行）。
func (o *appOptions) Validate() error {
	if o.Spec == "" && o.Name == "" {
		return fmt.Errorf("the app name is required (or pass --spec <AppSpec.json>)")
	}
	if o.Spec == "" {
		if o.Adopt || o.Check {
			return fmt.Errorf("--adopt/--check apply to spec mode (--spec <AppSpec.json>); template mode uses --force")
		}
		return nil
	}
	if o.Force {
		return fmt.Errorf("--force is retired for spec mode; use --adopt to regenerate owned files")
	}
	if o.Adopt && o.Check {
		return fmt.Errorf("--adopt and --check are mutually exclusive")
	}
	return nil
}

// Run 纯执行：spec 非空走 AppSpec 方言生成，否则渲染硬编码模板骨架。
func (o *appOptions) Run() error {
	if o.Spec != "" {
		return runGenAppSpec(o.Spec, o.OutFile, o.Adopt, o.Check, o.ioStreams)
	}
	raw, err := renderTmpl("app", appTmpl, map[string]string{"Name": o.Name})
	if err != nil {
		return err
	}
	formatted, err := format.Source(raw)
	if err != nil {
		return err
	}
	if err := writeGuarded(o.OutFile, formatted, owned, o.Force, o.ioStreams); err != nil {
		return err
	}
	fmt.Fprintln(o.Out, "next: fill [FILL] points, then `go mod edit` your module imports if needed")
	return nil
}

// genAppCmd 生成应用装配骨架（第一步模板生成 + 第二步 AppSpec 目标目录树）。
//
// 两种模式：
//   - gen app <name>                       → 第一版硬编码模板骨架（含 [FILL] 填充点）
//   - gen app <name> --spec <AppSpec.json> → 方言驱动：AppSpec
//     单一真相源，生成目标目录树（D2 §2.1）——cmd/<app>/{main.go,app/command.go}、
//     internal/bootstrap/{assemble.gen.go,domains.gen.go,seed/}、
//     internal/apiserver/{apiserver.go,domains.gen.go,layering_invariant_test.go}、
//     api/{buf.yaml,buf.gen.yaml}、configs/<app>.yaml、taskfile.yml；三道门
//     （Gate1 schema / Gate2 契约键 / Gate3 StrictContract）全过后落盘，
//     归属三态（owned 覆盖 / seeded 跳过 kept: / user 永不触碰）。
func genAppCmd(streams ioStreams) *cobra.Command {
	o := &appOptions{ioStreams: streams}
	cmd := &cobra.Command{
		Use:   "app [name]",
		Short: "生成应用装配骨架 main.go（appkit 全原语 + bundle 接线）",
		Long: `生成可编译的 cmd/<name>/main.go 装配骨架，覆盖 appkit 全部原语的推荐用法：

  - S1 能力声明（Provides/Requires，启动期 fail-fast）
  - 效应账本（Effect 登记全局写入的逆操作）
  - 组件生命周期（Components + ComponentFunc）
  - R1 配置 key 级热更新订阅（OnKeyChange）
  - bundle 横切接线示例（buildBundle/buildServers）

填充点标记为 [FILL]。素材对照 kalandramo/bald-admin 的装配形状。

指定 --spec <AppSpec.json> 时切换为目录树模式：以 AppSpec
方言（proto 单一真相源）驱动整套骨架，此时 <name> 可省略——应用名与目录树
均以 AppSpec 为准。旧字段 server/audit_backends/bundle_normalized 是快捷
方式：bootstrap 段为空时归一化进契约投影，非空时与其投影不一致生成期报错。`,
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
	cmd.Flags().StringVar(&o.OutFile, "out", "", "输出根目录（模板模式默认 cmd/<name>/main.go；spec 模式默认 layout.root 或 .）")
	cmd.Flags().StringVar(&o.Module, "module", "", "业务 module 路径（提示用）")
	cmd.Flags().StringVar(&o.Spec, "spec", "", "AppSpec JSON 路径（protojson），开启方言生成（目标目录树）")
	cmd.Flags().BoolVar(&o.Force, "force", false, "模板模式：覆盖已存在的目标文件（默认拒绝）")
	cmd.Flags().BoolVar(&o.Adopt, "adopt", false, "spec 模式：覆盖 owned 生成物并更新清单（--force 的替代）")
	cmd.Flags().BoolVar(&o.Check, "check", false, "spec 模式：只校验生成物与清单是否与声明一致（零写入，有差异即失败）")
	cmd.SetErr(streams.ErrOut)
	return cmd
}

// runGenAppSpec 读取 AppSpec 并生成目标目录树（第二步）：三道门全过后落盘。
//
// 流水线：Gate1 schema 门（validateAppSpec）→ 快捷方式归一化
// （normalizeBootstrap——bootstrap 为空时旧 6 字段升格为唯一契约投影）→
// Gate2 契约门（contractGate：描述符派生配置树 + 未知键校验 + YAML 回读
// 复检）→ buildTreeView（段存在性派生，无字段转写）→ buildTree/emitTree
// （三态归属落盘）。appspecData 手工转写已删除：模板直接吃 *appspecv1.AppSpec。
func runGenAppSpec(spec, out string, adopt, check bool, streams ioStreams) error {
	raw, err := os.ReadFile(spec)
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	as := &appspecv1.AppSpec{}
	if err := protojson.Unmarshal(raw, as); err != nil {
		return fmt.Errorf("parse AppSpec (protojson): %w", err)
	}
	if err := guardErrs(validateAppSpec(as)); err != nil {
		return fmt.Errorf("Gate1 (spec schema): %w", err)
	}
	bs := normalizeBootstrap(as)
	cfgYAML, err := contractGate(as, bs)
	if err != nil {
		return err
	}
	view := buildTreeView(as, bs)
	cfgs, err := roleContracts(as, bs, cfgYAML, view)
	if err != nil {
		return err
	}
	root := out
	if root == "" {
		root = as.GetLayout().GetRoot() // layout.root 默认 "."（产出根 = CWD）
	}
	files, err := buildTree(view, cfgs)
	if err != nil {
		return err
	}
	_, manifestRaw, err := buildManifest("bald gen app", spec, files)
	if err != nil {
		return err
	}
	if check {
		return checkTree(root, appManifestName, files, manifestRaw, streams)
	}
	if err := emitTree(root, files, adopt, streams); err != nil {
		return err
	}
	if adopt {
		// 声明删掉的旧产物必须退场（生成物是声明的函数）；只删上次清单
		// 记为本生成器 owned 的文件，seeded/user 不动。
		if err := pruneStaleOwned(root, appManifestName, files, streams); err != nil {
			return err
		}
	}
	if err := writeManifest(root, appManifestName, manifestRaw, streams); err != nil {
		return err
	}
	fmt.Fprintln(streams.Out, "next: appspec.json is the declaration truth source; edit it and rerun with --check to verify, --adopt to regenerate owned files (seeded files are kept, user files are never touched, stale owned files are removed)")
	return nil
}

// roleContracts 为每个角色派生一份契约 YAML（决策 D2=b：每角色一份
// configs/<app>-<role>.yaml）。单角色直接复用 Gate2 已算出的 base——这是
// 「roles 为空时生成物逐字节不变」的保证点。
//
// 多角色时 RoleSpec.server 非 nil 即以它为准：server 段由 projectServer
// 覆盖（normalizeBootstrap 只在契约段整体为空时才投影快捷方式，这里不能
// 依赖它）。空 ServerSpec 投影为 nil 段——runner 的契约里因此没有 server 段。
// server 为 nil 的角色继承 AppSpec 的契约（不另开面）。
func roleContracts(as *appspecv1.AppSpec, bs *bootstrapv1.BootstrapConfig, base []byte, view *treeView) (map[string][]byte, error) {
	cfgs := make(map[string][]byte, len(view.Roles))
	for _, slot := range view.Roles {
		if slot.Name == "" {
			cfgs[""] = base // 单角色：零额外派生
			continue
		}
		sv := roleServer(as, slot.Name)
		if sv == nil {
			cfgs[slot.Name] = base // 未声明 server = 继承 AppSpec 的契约
			continue
		}
		roleBs := proto.Clone(bs).(*bootstrapv1.BootstrapConfig)
		roleBs.Server = projectServer(sv)
		cy, err := contractGate(as, roleBs)
		if err != nil {
			return nil, fmt.Errorf("role %q: %w", slot.Name, err)
		}
		cfgs[slot.Name] = cy
	}
	return cfgs, nil
}

// roleServer 返回指定角色声明的 server 快捷方式（不存在时返回 nil = 继承）。
func roleServer(as *appspecv1.AppSpec, role string) *appspecv1.ServerSpec {
	for _, r := range as.GetMeta().GetRoles() {
		if r.GetName() == role {
			return r.GetServer()
		}
	}
	return nil
}
