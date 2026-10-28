package appkit

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bootstrap/config"
)

// bench_test.go 为 appkit 装配引擎/运行期编排的基准套件。本组全部
// 为仅基线项：接口化 Option/Decl、统一资源面、效应账本、key 订阅、
// 期望态协调均为 v0.25.0 新模型，无 v0.24.0 对应路径可比。
//
// 口径纪律：
//   - fixture（Option 集合、resources map、策略行、组件桩）在计时外构造；
//   - 热路径无 fmt/日志（fmt.Sprintf 只出现在计时外的 fixture 构造）；
//   - 涉及 App 未导出运行态的基准以包内测试形态直构 App，不经 Run。

// benchComponent 最小组件桩：Name/Start/Dispose 全 no-op，供组件注册与
// 协调器挂载归属使用。
type benchComponent struct{ name string }

func (c *benchComponent) Name() string                  { return c.name }
func (c *benchComponent) Start(context.Context) error   { return nil }
func (c *benchComponent) Dispose(context.Context) error { return nil }

// benchDeclOptions 构造 25 个混合 Option：标量字段、追加型切片（钩子/机制/
// KeyWatches/Reconciles/Providers/Binds）、单槽覆盖——覆盖 Decl 写入面的
// 全部形态。全部经 optionFunc / Provider 的 Apply(*Decl) 接口分发。
func benchDeclOptions() []Option {
	noop := func(context.Context) error { return nil }
	return []Option{
		ID("bench-app"),
		Name("bench"),
		Version("v0.25.0"),
		Env("bench"),
		ConfigFile("config.yaml"),
		WatchConfigFile(true),
		StrictContract("biz"),
		OnConfigChange(func(map[string]any) {}),
		StopTimeout(10 * time.Second),
		BeforeStart(noop),
		AfterStart(noop),
		BeforeStop(noop),
		AfterStop(noop),
		effectOpt("bench-effect-1", noop),
		effectOpt("bench-effect-2", noop),
		effectTimeoutOpt(5 * time.Second),
		onKeyChangeOpt("http.addr", func(string, string) {}),
		onKeyChangeOpt("log.level", func(string, string) {}),
		onKeyChangeOpt("audit.backends", func(string, string) {}),
		onKeyChangeOpt("cache.redis.addr", func(string, string) {}),
		reconcileOpt("bench-reconcile-1", func(context.Context, *ReconcileCtx) error { return nil }),
		reconcileOpt("bench-reconcile-2", func(context.Context, *ReconcileCtx) error { return nil }),
		componentsOpt(&benchComponent{name: "bench-comp"}),
		Bind("http", &bootstrapv1.Server_Http{Addr: ":8080"}),
		stdLoggerProvider{},
	}
}

// BenchmarkDeclApply_OptionFanout 度量接口化 Option（Apply(*Decl)）的
// 装配期写入面成本：25 个混合 Option 逐个 Apply 到一份 Decl——NewApp 装配
// 的声明收集阶段成本。Decl 基底在计时外经 defaultDecl() 构造（含
// os.Hostname 随机实例 ID），每 op 结构体拷贝后复用。
func BenchmarkDeclApply_OptionFanout(b *testing.B) {
	base := defaultDecl()
	opts := benchDeclOptions()
	b.ReportAllocs()
	for b.Loop() {
		d := base
		for _, o := range opts {
			o.Apply(&d)
		}
		if len(d.KeyWatches) != 4 || len(d.Providers) != 1 {
			b.Fatalf("decl fanout incomplete: keywatches=%d providers=%d", len(d.KeyWatches), len(d.Providers))
		}
	}
}

// benchResourceApp 构造统一资源面 fixture：整产物段（registry/audit 直落顶层
// 键）+ 六资源域按 "<顶层段>.<子段>" 展开的子段键（database 4 个子段供
// 顶层快照聚合）。
func benchResourceApp() *App {
	return &App{resources: map[string]any{
		"registry":            "registrar-product",
		"audit":               "auditor-product",
		"database.sql":        "sql-client",
		"database.mongodb":    "mongo-client",
		"database.clickhouse": "clickhouse-client",
		"database.es":         "es-client",
		"cache.redis":         "redis-client",
		"cache.local":         "local-cache",
		"storage.minio":       "minio-client",
		"storage.s3":          "s3-client",
		"ai.openai":           "openai-client",
		"workflow.temporal":   "temporal-client",
		"broker.kafka":        "kafka-client",
	}}
}

// BenchmarkResource_Lookup 度量 Resource 的整产物段精确命中路径：键直接命中
// resources map（resource.go:41），零聚合——Run 期取 registry/audit 产物的
// 常规成本。
func BenchmarkResource_Lookup(b *testing.B) {
	app := benchResourceApp()
	b.ReportAllocs()
	for b.Loop() {
		v, ok := app.Resource("registry")
		if !ok || v == nil {
			b.Fatal("registry lookup failed")
		}
	}
}

// BenchmarkResource_SubSegment 度量 Resource 的子段精确命中路径：
// "database.sql" 键直接命中 resources map——类型断言取实例的常规成本。
func BenchmarkResource_SubSegment(b *testing.B) {
	app := benchResourceApp()
	b.ReportAllocs()
	for b.Loop() {
		v, ok := app.Resource("database.sql")
		if !ok || v == nil {
			b.Fatal("subsegment lookup failed")
		}
	}
}

// BenchmarkResource_TopLevelSnapshot 度量 Resource 的顶层段副本快照路径：
// "database" 无精确键，遍历 resources 按 "database." 前缀聚合并拷贝出
// map[string]any（取代原 Databases() 的副本语义）——每次调用一次分配，
// 与 map 规模线性。4 个 database 子段。
func BenchmarkResource_TopLevelSnapshot(b *testing.B) {
	app := benchResourceApp()
	b.ReportAllocs()
	for b.Loop() {
		v, ok := app.Resource("database")
		if !ok {
			b.Fatal("top-level snapshot failed")
		}
		if snap, ok := v.(map[string]any); !ok || len(snap) != 4 {
			b.Fatalf("snapshot shape: %T len=%d", v, len(v.(map[string]any)))
		}
	}
}

// benchConfigReader 最小整文档源（bconfig.Reader）：固定 JSON 字节，
// 供 config.Load 构造带 store 的 App fixture。
type benchConfigReader struct{ data []byte }

func (r benchConfigReader) Load(context.Context, string) ([]byte, error) { return r.data, nil }

// benchWatchApp 构造已装载 config.Store 且带 64 个已注册 key 订阅的 App：
// 订阅键 k.000..k.063 对应 store 内嵌 JSON 文档的 64 个叶子，fn 为 no-op。
// store 关闭由返回的 cleanup 承担。
func benchWatchApp(b *testing.B) (*App, func()) {
	b.Helper()
	keys := make(map[string]any, 64)
	for i := 0; i < 64; i++ {
		keys[fmt.Sprintf("%03d", i)] = fmt.Sprintf("value-%03d", i)
	}
	doc, err := json.Marshal(map[string]any{"k": keys})
	if err != nil {
		b.Fatal(err)
	}
	st, err := config.Load(config.Options{
		Name: "bench",
		Layers: []config.Layer{
			{Name: "bench-fixture", Reader: benchConfigReader{data: doc}, Format: "json"},
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	a := &App{cfg: appConfig{store: st}}
	for i := 0; i < 64; i++ {
		a.keyWatchers = append(a.keyWatchers, keyWatcher{
			key: fmt.Sprintf("k.%03d", i),
			fn:  func(string, string) {},
		})
	}
	return a, func() { _ = st.Close() }
}

// BenchmarkKeyWatch_Dispatch 度量 key 级配置订阅的分发成本（dispatchKeyWatch，
// 锁内串行）：64 个订阅者全量扫描，每个订阅一次 GetString 点路径读取。
//   - same_value：全部订阅同值不触发（热更常态——绝大多数变更与订阅无关），
//     度量纯扫描成本；
//   - single_key_change：单键值变化触发一次 fn（含 last 基线回拨赋值）。
func BenchmarkKeyWatch_Dispatch(b *testing.B) {
	b.Run("same_value", func(b *testing.B) {
		a, done := benchWatchApp(b)
		defer done()
		a.armKeyWatchers() // 以当前装载值为基线
		b.ReportAllocs()
		for b.Loop() {
			a.dispatchKeyWatch()
		}
	})
	b.Run("single_key_change", func(b *testing.B) {
		a, done := benchWatchApp(b)
		defer done()
		a.armKeyWatchers()
		w := &a.keyWatchers[10]
		b.ReportAllocs()
		for b.Loop() {
			w.last = "stale-baseline" // 制造单键变更；一次字段赋值，远小于扫描成本
			a.dispatchKeyWatch()
		}
	})
}

// benchReconFn 返回标准「期望态 diff-apply」协调函数：从 Settings 读期望
// 组件清单，与 Mounted() 实际态做双向 diff——缺的挂载、多的卸载。稳态
// （期望==实际）下 Mount/Unmount 均不触发，只付 diff 判定成本。
func benchReconFn(key string) ReconcileFunc {
	return func(ctx context.Context, r *ReconcileCtx) error {
		want := r.StringSlice("recon." + key)
		mounted := map[string]bool{}
		for _, n := range r.Mounted() {
			mounted[n] = true
		}
		wantSet := map[string]bool{}
		for _, n := range want {
			wantSet[n] = true
		}
		for _, n := range want {
			if mounted[n] {
				continue
			}
			if err := r.Mount(ctx, n, &benchComponent{name: n}); err != nil {
				return err
			}
		}
		for n := range mounted {
			if !wantSet[n] {
				if err := r.Unmount(ctx, n); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

// BenchmarkReconcile_DiffApply 度量 R1-2 期望态协调的稳态热更成本：
// 3 个协调器 × 各管理 1-2 个已挂载组件，期望态==实际态（无漂移），
// 每次 runReconcilersWith = 协调器快照 + 3×(Settings 读取 + Mounted diff +
// 审计旁路 Nop.Record)。这是配置每次热更（wrapKeyWatch 尾部）固定支付的
// 成本；Mount/Unmount 的漂移路径含组件 Start/Dispose（业务成本），不入本口径。
func BenchmarkReconcile_DiffApply(b *testing.B) {
	a := &App{reconItems: map[string][]reconItem{}}
	wants := map[string][]string{
		"reconcile-cache":   {"cache.warm", "cache.gc"},
		"reconcile-audit":   {"audit.sink"},
		"reconcile-metrics": {"metrics.exporter"},
	}
	reconSettings := map[string]any{}
	for rname, comps := range wants {
		for _, c := range comps {
			a.reconItems[rname] = append(a.reconItems[rname], reconItem{name: c, comp: &benchComponent{name: c}})
		}
		reconSettings[rname] = comps
		a.reconcilers = append(a.reconcilers, reconciler{name: rname, fn: benchReconFn(rname)})
	}
	settings := map[string]any{"recon": reconSettings}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		a.runReconcilersWith(ctx, settings)
	}
}

// BenchmarkEffectUndo_Ledger 度量 T1 效应账本的登记与逆序回放成本：
// 每 op 新账本（App 字段直构）+ 32 条 addEffect 登记 + UndoEffects 逆序回放
// （每条独立超时 ctx + recover 隔离 + 幂等清空）。undo 为 no-op——度量的是
// 账本框架开销，不含业务清理成本；这是停机路径与启动失败回滚路径的共同成本。
func BenchmarkEffectUndo_Ledger(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		a := &App{effectTimeout: defaultHookTimeout}
		for i := 0; i < 32; i++ {
			a.addEffect("bench-effect", func(context.Context) error { return nil })
		}
		a.UndoEffects(ctx)
	}
}

// BenchmarkStdLoggerProvider_Build 度量内置 slog logger Provider 的契约重建
// 成本（standard.go stdLoggerProvider.Build）：按契约 logger.backends 构造
// slog handler + FilterLogger 并返回 cleanup——热更新路径每次配置变更都
// 全量重建 logger，本基准即该路径的单次成本。单 slog 后端 + stdout 输出
// （不落盘），每 op 调 cleanup 释放，与热更的换槽释放协议一致。
func BenchmarkStdLoggerProvider_Build(b *testing.B) {
	prov := StandardLoggerProvider()
	cfg := &bootstrapv1.BootstrapConfig{Logger: &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{{
			Type: "slog",
			Slog: &bootstrapv1.Logger_Slog{Level: "info", Format: "console", OutputPath: "stdout"},
		}},
	}}
	scope := ApplyScope{Config: cfg, Decl: &Decl{}}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		prod, cleanup, err := prov.Build(ctx, scope, Node{})
		if err != nil {
			b.Fatal(err)
		}
		if prod == nil {
			b.Fatal("nil logger product")
		}
		if cleanup != nil {
			_ = cleanup(ctx)
		}
	}
}
