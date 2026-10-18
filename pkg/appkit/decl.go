// decl.go 定义 Decl——**全部装配期声明的纯数据载体**（装配层评估 §5.2 批 2
// 第一波，起点设计 D1-target-appkit.md §5.2）。
//
// 背景：Option 原为 func(*App)，包外无法构造（App 字段未导出），
// mech 子包因此只能经 App 的导出注册方法（register.go）间接写入。批 2
// 把 Option 接口化为 interface{ Apply(*Decl) }（导出方法 + 导出参数类型，
// 包外可实现），Decl 承载全部装配期声明的写入面：
//   - NewApp 路径 Option 的全部写入（ID/Name/Config*/Bind/Servers/钩子/机制…）；
//   - Serve 路径 With* 的全部写入（WithHTTP/WithGRPC/Providers 段驱动
//     声明/WithLogDecorators/WithExtraServerFunc…）。
//
// 字段集由对账得出（逐 Option 逐一归属，见各字段注释），不含任何运行期
// 状态：效应账本的运行期追加（addEffect）、key 订阅的基线快照（last/armed）、
// 配置装载结果（*config.Store）等仍归 App 私有字段，构造期从 Decl 拷贝
// （newFromDecl）。App 不嵌入 Decl——声明（纯数据）与运行态（并发保护、
// 可逆账本、幂等跟踪）生命周期不同，嵌入会把运行态暴露成可外部改写的
// 导出字段；拷贝让引擎（Run/stopAll/UndoEffects/Resolve/协调器/热更）与
// 改造前逐字段同构，行为零变化。
//
// 唯一 Option 类型（批 2 W2a）：原 BootstrapOption/HealthOption 已删除，
// With* 构造器重定型为返回 Option——写入目标是同一份 Decl。
package appkit

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/bootstrap/config"
	"github.com/kalandramo/bald/health"
	"github.com/kalandramo/bald/log/bslog"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/registry"
	"github.com/kalandramo/bald/transport"
)

// Decl 汇集全部装配期声明（纯数据、全导出字段）。两条构造路径共用：
// NewApp 路径由 Option.Apply 逐项写入；Serve 路径由 With* 写入后，框架
// （assemble）再按契约补齐其余字段。
type Decl struct {
	// ---- 服务身份（ID/Name/Version Option；app 段回填） ----
	ID      string
	Name    string
	Version string
	// Env 运行环境（Env Option；契约 app.env）。
	Env string

	// ---- 配置源（ConfigFile/RemoteConfig/ConfigLayers/Env/WatchConfigFile/
	// StrictContract/OnConfigChange/Bind Option） ----
	// configNamespace 配置命名空间（env 前缀 / 多环境文件名来源）——**内部派生
	// 值**（B6，装配层评估 §5.1）：Serve 路径由契约 app.name 两遍合并派生，
	// NewApp 路径回退 Name。不再有公开声明口（原 ConfigNamespace Option 已删，
	// env 名不变）。
	configNamespace string
	ConfigFile      string
	WatchConfigFile bool
	RemoteConfig    config.RemoteSource
	// ConfigLayers 契约源层（ConfigLayers Option；约定装配经
	// ConfigRegistry.Build 产出后追加）。
	ConfigLayers []config.Layer
	// StrictContract 与 BusinessSections 成对：开关 + 应用自持顶层段白名单
	// （StrictContract 可多次调用追加白名单）。
	StrictContract   bool
	BusinessSections []string
	// OnConfigChange 热更新全量回调（单槽，后写覆盖——与原写入路径一致）。
	OnConfigChange func(map[string]any)
	// Binds 业务配置对象的 flag 绑定（Bind 追加）。
	Binds []BindDecl

	// ---- 注册中心与服务器（Registrar/Servers Option；WithExtraServerFunc
	// 声明运行期工厂） ----
	Registrar registry.Registrar
	// Servers 已构造的服务器实例（Servers 追加）。
	Servers []transport.Server
	// ServersFunc 运行期构造的附加服务器工厂（WithExtraServerFunc 追加；
	// Run 期 beforeStart 链末尾调用，可消费业务运行期资源）。
	ServersFunc []func(context.Context) (transport.Server, error)

	// ---- 生命周期钩子与超时（StopTimeout/BeforeStart/AfterStart/
	// BeforeStop/AfterStop Option） ----
	StopTimeout time.Duration
	BeforeStart []func(context.Context) error
	AfterStart  []func(context.Context) error
	BeforeStop  []func(context.Context) error
	AfterStop   []func(context.Context) error

	// ---- 机制层（导出构造面在 pkg/appkit/mech：mech.Effect/EffectTimeout/
	// Provides/Requires/Components/ComponentTimeout/OnKeyChange/Reconcile
	// 经 Apply 直写；引擎透传见 assemble 的未导出 *Opt 写入面） ----
	Effects []EffectDecl
	// EffectTimeout 效应撤销阶段的独立超时（单槽）。
	EffectTimeout time.Duration
	Provides      []string
	Requires      []RequireDecl
	Components    []Component
	// ComponentTimeout 组件 Start/Dispose 的独立超时（单槽）。
	ComponentTimeout time.Duration
	KeyWatches       []KeyWatchDecl
	Reconciles       []ReconcileDecl

	// ---- 审计（auditorOpt 写入；运行期 appkit 自身审计事件 sink） ----
	Auditor audit.Auditor

	// ---- 身份端口（Identity Option；appkit 审计事件 Subject/TenantID 取值） ----
	// IdentityFunc 是 appkit 对业务身份的最小端口（D1 §4.4）：nil = 空身份
	// （与今日 claims 为 nil 的行为一致）。适配（如 authn claims）由应用侧
	// 1 行 lambda 提供。
	Identity IdentityFunc

	// ---- 段驱动 Provider（Providers/WithUnimplementedSegments 声明；
	// std.Standard() 与第三方 Provider 经 Apply 追加，装配引擎见 provider.go） ----
	// Providers 是段驱动装配的全部 Provider（含 std.Standard() 打包的框架
	// 自有后端；按精确段路径末者胜——后声明覆盖先声明）。
	Providers []Provider
	// UnimplementedSegments 是 L3 覆盖检查的白名单（WithUnimplementedSegments
	// 追加）：契约已声明、框架尚无消费者的段，显式豁免启动期报错。
	UnimplementedSegments []string

	// ---- 日志装饰器（logDecoratorsOpt 写入，B7 取消导出：装饰归 provider
	// 自持——内置 slog Provider 消费本字段，第三方 logger Provider 自持装饰） ----
	logDecorators []bslog.Option

	// ---- 协议面（WithHTTP/WithGatewayRegister/WithGRPC/WithGRPCOptions
	// 声明；能力声明在代码，契约 server 段驱动装配） ----
	HTTPHandler http.Handler
	// GatewayRegister grpc-gateway 转码注册回调（server.http 的一种模式，
	// 由契约 server.http.driver 选择，见 WithGatewayRegister）。
	GatewayRegister func(context.Context, *grpc.ClientConn) (http.Handler, error)
	// GRPCRegister gRPC service 注册回调；GRPCUnary 拦截器链（值切片，
	// 调用点求值）。
	GRPCRegister func(*grpc.Server)
	GRPCUnary    []grpc.ServerOption
	// GRPCOptionsFn 运行期求值的 gRPC 选项工厂（WithGRPCOptions；与
	// GRPCUnary 累加，静态在前）。
	GRPCOptionsFn func() []grpc.ServerOption

	// ---- 健康检查（WithHealth/WithProbePaths/WithHealthPollInterval 声明；
	// 探针路径仅 WithHealth 置默认值，空值 = 未声明） ----
	Health         *health.Health
	ProbeLivePath  string
	ProbeReadyPath string
	// HealthInterval gRPC 侧就绪状态同步间隔（0 = 未覆盖，用默认）。
	HealthInterval time.Duration
}

// EffectDecl 是一条可逆效应声明（mech.Effect 的写入面；
// 运行期账本条目 effectEntry 与之同构，构造期拷贝）。
type EffectDecl struct {
	Name string
	Undo func(context.Context) error
}

// RequireDecl 是组件能力依赖声明（mech.Requires 的
// 写入面；引擎内部 requirement 与之同构，构造期拷贝）。
type RequireDecl struct {
	Component string
	Caps      []string
}

// KeyWatchDecl 是 key 级配置订阅声明（mech.OnKeyChange 的写入面；
// 运行期 keyWatcher 在其上携带基线状态）。
type KeyWatchDecl struct {
	Key string
	Fn  func(old, new string)
}

// ReconcileDecl 是期望态协调器声明（mech.Reconcile 的写入面；
// 引擎内部 reconciler 与之同构，构造期拷贝）。
type ReconcileDecl struct {
	Name string
	Fn   ReconcileFunc
}

// BindDecl 是一条业务配置对象的 flag 绑定声明（Bind 的写入面；
// proto.Message 走 bconf.BindFlags，PlainBinder 走 AddFlags，见 bindFlags）。
type BindDecl struct {
	Prefix string
	Opt    any
}

// defaultDecl 返回带缺省值的空声明（NewApp 与 assemble 的 kitOpts 共用的构造起点；
// 缺省值与旧 New 的结构体字面量逐字段一致：ID 随机实例后缀，元数据默认值，
// 超时默认值。探针路径刻意不预填——空值即「未声明」，由 WithHealth 置默认）。
func defaultDecl() Decl {
	return Decl{
		ID:               defaultInstanceID(),
		Name:             "bald-app",
		Version:          "v0.0.0",
		StopTimeout:      defaultStopTimeout,
		EffectTimeout:    defaultHookTimeout,
		ComponentTimeout: defaultHookTimeout,
	}
}

// newFromDecl 从声明构造 App（构造期拷贝：引擎消费的运行态字段从 Decl
// 逐字段拷入，运行期追加与并发保护仍归 App 私有字段）。效应账本按声明
// 序入账——运行期 addEffect 在同一账本继续追加，逆序回放顺序与旧实现一致。
func newFromDecl(d Decl) *App {
	a := &App{
		id:               d.ID,
		name:             d.Name,
		version:          d.Version,
		cfgNamespace:     d.configNamespace,
		stopTimeout:      d.StopTimeout,
		registrar:        d.Registrar,
		servers:          d.Servers,
		beforeStart:      d.BeforeStart,
		afterStart:       d.AfterStart,
		beforeStop:       d.BeforeStop,
		afterStop:        d.AfterStop,
		effectTimeout:    d.EffectTimeout,
		provides:         d.Provides,
		components:       d.Components,
		componentTimeout: d.ComponentTimeout,
		auditor:          d.Auditor,
		identity:         d.Identity,
		reconItems:       make(map[string][]reconItem),
		resources:        make(map[string]any),
		done:             make(chan struct{}),
		cfg: appConfig{
			cfgFile:          d.ConfigFile,
			remote:           d.RemoteConfig,
			layers:           d.ConfigLayers,
			env:              d.Env,
			watchFile:        d.WatchConfigFile,
			onChange:         d.OnConfigChange,
			bindings:         d.Binds,
			strictContract:   d.StrictContract,
			businessSections: d.BusinessSections,
		},
	}
	if len(d.Effects) > 0 {
		a.effects = make([]effectEntry, len(d.Effects))
		for i, e := range d.Effects {
			a.effects[i] = effectEntry{name: e.Name, undo: e.Undo}
		}
	}
	if len(d.Requires) > 0 {
		a.requires = make([]requirement, len(d.Requires))
		for i, r := range d.Requires {
			a.requires[i] = requirement{component: r.Component, caps: r.Caps}
		}
	}
	if len(d.KeyWatches) > 0 {
		a.keyWatchers = make([]keyWatcher, len(d.KeyWatches))
		for i, w := range d.KeyWatches {
			a.keyWatchers[i] = keyWatcher{key: w.Key, fn: w.Fn}
		}
	}
	if len(d.Reconciles) > 0 {
		a.reconcilers = make([]reconciler, len(d.Reconciles))
		for i, r := range d.Reconciles {
			a.reconcilers[i] = reconciler{name: r.Name, fn: r.Fn}
		}
	}
	return a
}
