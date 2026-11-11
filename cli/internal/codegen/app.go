package codegen

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/kalandramo/bald/cobramcp"
	"google.golang.org/protobuf/proto"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// appOptions 是 gen app 叶子命令的领域配置。
type appOptions struct {
	OutRoot string // --out：产出根目录（不可命名 Out，避免遮蔽 ioStreams.Out）
	Spec    string // --spec：AppSpec JSON 路径（protojson）
	Preset  string // --preset：预置声明模板名（与 --spec 互斥；落一份 appspec.json 后由用户编辑）
	Adopt   bool   // --adopt：覆盖 owned 生成物并更新清单
	Check   bool   // --check：只校验不落盘（有差异即失败）

	ioStreams
}

// Validate 校验必填项与 flag 互斥。
func (o *appOptions) Validate() error {
	if o.Spec == "" && o.Preset == "" {
		// DX-7：cwd 自动发现——应用目录里跑 `bald gen app` 不该每次都带 --spec。
		// 只认精确的 appspec.json（不猜 *.json 通配：多候选时猜错比报错更糟）。
		if _, err := os.Stat("appspec.json"); err == nil {
			o.Spec = "appspec.json"
			fmt.Fprintln(o.Out, "using appspec.json from current directory")
		} else {
			return fmt.Errorf("--spec <AppSpec.json> is required (or --preset <name> to start from a template)")
		}
	}
	if o.Spec != "" && o.Preset != "" {
		return fmt.Errorf("--spec and --preset are mutually exclusive")
	}
	if o.Adopt && o.Check {
		return fmt.Errorf("--adopt and --check are mutually exclusive")
	}
	return nil
}

// Run 纯执行：--preset 先落声明文件（Seeded 语义），否则读取 AppSpec 生成目录树。
func (o *appOptions) Run() error {
	if o.Preset != "" {
		return runPreset(o.Preset, o.OutRoot, o.ioStreams)
	}
	return runGenAppSpec(o.Spec, o.OutRoot, o.Adopt, o.Check, o.ioStreams)
}

// runPreset 把预置声明写到产出根的 appspec.json：不存在才写（声明是 user 文件，
// 覆盖别人的声明越权）；已存在即拒绝并指引。写完给下一步指令（改 meta → gen app）。
func runPreset(name, outRoot string, streams ioStreams) error {
	raw, err := presetSpec(name)
	if err != nil {
		return err
	}
	root := outRoot
	if root == "" {
		root = "."
	}
	specPath := filepath.Join(root, "appspec.json")
	if _, err := os.Stat(specPath); err == nil {
		return fmt.Errorf("%s already exists, refusing to overwrite (edit it, or remove it first)", specPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", specPath, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(specPath, raw, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(streams.Out, "generated:", specPath, "(preset "+name+")")
	fmt.Fprintln(streams.Out, "next: edit meta.name/module (and addrs) in appspec.json, then run `bald gen app --spec appspec.json`")
	return nil
}

// CheckAppSpec 是 gen app --check 的程序化入口（零写入校验生成树与清单和声明
// 一致，有差异时返回带逐文件清单的错误）。convention check 的 gen-app-drift
// checker 复用同一道门——零差异语义只有一份实现，不出现第二种口径。
// outRoot 为空时取 AppSpec.layout.root；out/errOut 是诊断流，调用方不关心
// 过程时传 io.Discard（差异明细已随 error 返回）。
func CheckAppSpec(spec, outRoot string, out, errOut io.Writer) error {
	return runGenAppSpec(spec, outRoot, false, true, ioStreams{Out: out, ErrOut: errOut})
}

// genAppCmd 按 AppSpec 生成应用装配目录树。
//
// AppSpec 是唯一真相源：应用名、契约段（server/audit/...）与生成物路径均以
// 它为准，产物为 cmd/<app>/{main.go,app/command.go}、
// internal/bootstrap/{assemble.gen.go,domains.gen.go,seed/}、
// internal/apiserver/{apiserver.go,layering_invariant_test.go}、
// api/{buf.yaml,buf.gen.yaml}、configs/<app>.yaml、taskfile.yml。三道门
// （Gate1 schema / Gate2 契约键 / Gate3 StrictContract）全过后落盘，
// 归属三态（owned 覆盖 / seeded 跳过 kept: / user 永不触碰）。
func genAppCmd(streams ioStreams) *cobra.Command {
	o := &appOptions{ioStreams: streams}
	cmd := &cobra.Command{
		Use:   "app",
		Short: "按 AppSpec 生成应用装配目录树",
		Long: `以 AppSpec（protojson）为单一真相源生成整套可编译骨架。

应用名、契约段与生成物路径均以 AppSpec 为准：契约段唯一真相源是
AppSpec.bootstrap，输出根缺省取 layout.root。落盘前依次过三道门：Gate1
生成期 schema、Gate2 生成期契约键（描述符派生 + 回读复检）、Gate3 运行期
StrictContract。

默认拒绝覆盖已存在的 owned 生成物：--adopt 重写并更新清单（声明删掉的旧
产物同时退场），--check 零写入校验漂移（CI 用）。seeded 只生成一次归你维护，
user 文件生成器永不读写。`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			return o.Run()
		},
	}
	// MCP 面的 readOnly 标注：cobramcp 的 selector 已排除 adopt/preset 两个写
	// flag（cmd/bald/main.go），暴露出去的工具只有 --spec/--out/--check 的
	// 只读校验形态——注解在该工具面内是准确描述（CLI 本体仍可写）。
	cmd.Annotations = map[string]string{
		cobramcp.AnnotationReadOnly: "true",
	}
	cmd.Flags().StringVar(&o.OutRoot, "out", "", "产出根目录（缺省取 AppSpec.layout.root，再缺省为当前目录）")
	cmd.Flags().StringVar(&o.Spec, "spec", "", "AppSpec JSON 路径（protojson）")
	cmd.Flags().StringVar(&o.Preset, "preset", "", "从预置声明模板起步（"+strings.Join(PresetNames(), "/")+"），落 appspec.json 后编辑再 gen app")
	cmd.Flags().BoolVar(&o.Adopt, "adopt", false, "覆盖 owned 生成物并更新清单（默认拒绝）")
	cmd.Flags().BoolVar(&o.Check, "check", false, "只校验生成物与清单是否与声明一致（零写入，有差异即失败）")
	cmd.SetErr(streams.ErrOut)
	return cmd
}

// runGenAppSpec 读取 AppSpec 并生成目标目录树：三道门全过后落盘。
//
// 流水线：Gate1 schema 门（validateAppSpec）→ 契约段定位
// （contractBootstrap——bootstrap 是唯一真相源，未声明时按空配置派生默认键）→
// Gate2 契约门（contractGate：描述符派生配置树 + 未知键校验 + YAML 回读
// 复检）→ buildTreeView（段存在性派生，无字段转写）→ buildTree/emitTree
// （三态归属落盘）。模板直接吃 *appspecv1.AppSpec。
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
	bs := contractBootstrap(as)
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
	seeded, err := seedGoMod(root, as.GetMeta().GetModule(), streams)
	if err != nil {
		return err
	}
	fmt.Fprintln(streams.Out, "next: appspec.json is the declaration truth source; edit it and rerun with --check to verify, --adopt to regenerate owned files (seeded files are kept, user files are never touched, stale owned files are removed)")
	if seeded {
		// DX-1：新用户第一小时的必卡点——树有了但没有 go.mod，go build 报
		// "directory prefix does not contain main module" 且无出口。骨架只含
		// module/go 两行，require 由 go mod tidy 从 import 集推导（权威且免
		// 维护版本映射）。
		fmt.Fprintln(streams.Out, `next: run "go mod tidy" to resolve module requirements (needs network), then "go build ./..."`)
	}
	return nil
}

// seedGoMod 在生成树缺 go.mod 时补一份骨架（module + go 两行）。它**不进
// manifest**：go.mod 落盘即归用户维护（加依赖、改版本都是用户的日常），
// --check 与 adopt 都不追踪它——gen 只在缺失时补这一次（os.Stat 缺席才写）。
// go 版本取 runtime.Version()：编译出这把 cli 的 Go 必然 ≥ bald 模块要求的
// 版本（cli go.mod 的 go 指令是下界），用户的工具链不会低于它。
func seedGoMod(root, module string, streams ioStreams) (bool, error) {
	p := filepath.Join(root, "go.mod")
	if _, err := os.Stat(p); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat %s: %w", p, err)
	}
	content := fmt.Sprintf("module %s\n\ngo %s\n", module, runtime.Version())
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return false, err
	}
	fmt.Fprintln(streams.Out, "generated:", p, "(skeleton)")
	return true, nil
}

// roleContracts 为每个角色派生一份契约 YAML（每角色一份
// configs/<app>-<role>.yaml）。单角色直接复用 Gate2 已算出的 base——这是
// 「roles 为空时生成物逐字节不变」的保证点。
//
// 多角色时 RoleSpec.server 非 nil 即以它为准：server 段由 projectServer
// 覆盖（只覆盖该角色的投影，bootstrap 的其余段原样继承）。空 ServerSpec
// 投影为 nil 段——runner 的契约里因此没有 server 段。
// server 为 nil 的角色继承 bootstrap 的契约（不另开面）。
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
		roleBs.Server = projectServer(sv, bs.GetServer())
		inheritMiddleware(roleBs.Server, bs.GetServer())
		cy, err := contractGate(as, roleBs)
		if err != nil {
			return nil, fmt.Errorf("role %q: %w", slot.Name, err)
		}
		cfgs[slot.Name] = cy
	}
	return cfgs, nil
}

// inheritMiddleware 把 AppSpec.bootstrap.server 的两段 middleware 继承进角色的
// server 段：角色的 server 快捷方式只投影 addr/driver 的取舍，横切链的配置不在
// 其中。不继承就是「在 bootstrap 里配了限流，角色契约文件里静默消失」——配置面
// 与链之间又缺一次调用，正是 WithHTTPContract / WithGRPCOptionsContract 要消灭
// 的那类静默。
//
// 只在角色真开了那个面时继承：转码面（driver=grpc-gateway）接管 server.http 但
// 不挂 gin 链，把 HTTP 的 middleware 段带进它的契约就是「写进契约却没有消费者」。
func inheritMiddleware(roleSrv, baseSrv *bootstrapv1.Server) {
	if roleSrv == nil || baseSrv == nil {
		return
	}
	if h := roleSrv.GetHttp(); h != nil && h.Driver == "" {
		if mw := baseSrv.GetHttp().GetMiddleware(); mw != nil {
			h.Middleware = proto.Clone(mw).(*bootstrapv1.Server_Http_Middleware)
		}
	}
	if g := roleSrv.GetGrpc(); g != nil {
		if mw := baseSrv.GetGrpc().GetMiddleware(); mw != nil {
			g.Middleware = proto.Clone(mw).(*bootstrapv1.Server_Grpc_Middleware)
		}
	}
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
