package appkit

// L3 段覆盖告警的单测（批 1：告警级）。
//
// 覆盖：含 script 段的合法配置 → 告警出现且启动不受影响；全部出现的段
// 都被消费 → 无告警；端到端走配置文件装载路径（script 由 config 层贡献）；
// 零能力声明下一对钉子：预填默认 server 段不告警（框架自填非用户意图），
// 用户覆盖 server 子字段则照实告警。

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bootstrap"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
	log "github.com/kalandramo/bald/log"
)

// warnLogger 记录 Warn 调用的全局 logger 桩（经 LogRegistry 的 probe 后端
// 注入阶段 B——告警发生在契约重建后的 Logger 上）。
type warnLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *warnLogger) Debug(context.Context, string, ...any) {}
func (l *warnLogger) Info(context.Context, string, ...any)  {}
func (l *warnLogger) Error(context.Context, string, ...any) {}

func (l *warnLogger) Warn(_ context.Context, msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg+" "+fmt.Sprintf("%v", args))
}

func (l *warnLogger) Enabled(log.Level) bool { return true }

func (l *warnLogger) With(args ...any) log.Logger { return l }

// probeLoggerOption 把 wl 装成契约 logger 段的 probe 后端（阶段 B 生效）。
func probeLoggerOption(wl *warnLogger) BootstrapOption {
	reg := bootstrap.NewLogRegistry()
	reg.MustRegister("probe", func(context.Context, *bootstrapv1.Logger_Backend) (log.Logger, func(), error) {
		return wl, nil, nil
	})
	return WithLogRegistry(reg)
}

func probeCfg(cfg *bootstrapv1.BootstrapConfig) {
	cfg.Logger = &bootstrapv1.Logger{Backends: []*bootstrapv1.Logger_Backend{{Type: "probe"}}}
}

func (l *warnLogger) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.warns...)
}

// 含 script 段的合法配置：告警出现、列出的就是 script、启动不受影响。
func TestFromBootstrap_WarnsUnconsumedScript(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	wl := &warnLogger{}
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	probeCfg(cfg)
	cfg.Script = &bootstrapv1.Script{Engine: bootstrapv1.Script_LUA}

	a, err := FromBootstrap(cfg,
		WithConfigNamespace(testConfigNamespace), WithHTTP(new(http.ServeMux)),
		probeLoggerOption(wl),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	runBriefly(t, a, 50*time.Millisecond) // Run 无错 = 启动不受影响

	var found string
	for _, line := range wl.lines() {
		if len(found) == 0 && strings.Contains(line, "no consumer") {
			found = line
		}
	}
	if len(found) == 0 {
		t.Fatalf("expected unconsumed-section warning, got %v", wl.lines())
	}
	if !strings.Contains(found, "sections script") {
		t.Fatalf("warning should list exactly script, got: %s", found)
	}
	if !strings.Contains(found, "hint 该段无消费者") {
		t.Fatalf("warning should carry actionable hint, got: %s", found)
	}
	t.Logf("captured warning: %s", found)
}

// 全部出现的段都被消费：无告警。
func TestFromBootstrap_NoWarnWhenAllConsumed(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	wl := &warnLogger{}
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	probeCfg(cfg)
	cfg.Database = &bootstrapv1.Database{
		Sql: &bootstrapv1.Database_SQL{Driver: "sqlite", Source: ":memory:"},
	}
	dr := bootstrap.NewDatabaseRegistry()
	dr.MustRegister("sql", func(context.Context, *bootstrapv1.Database) (any, func(), error) {
		return "db", nil, nil
	})

	a, err := FromBootstrap(cfg,
		WithConfigNamespace(testConfigNamespace), WithHTTP(new(http.ServeMux)),
		WithDatabaseRegistry(dr), probeLoggerOption(wl),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	runBriefly(t, a, 50*time.Millisecond)

	for _, line := range wl.lines() {
		if strings.Contains(line, "no consumer") {
			t.Fatalf("all present sections are consumed, unexpected warning: %s", line)
		}
	}
}

// 端到端（配置文件装载路径）：script 段由 config 层贡献（非代码内联），
// 装载完成后告警如实列出。
func TestFromBootstrap_WarnsScriptFromConfigLayer(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	wl := &warnLogger{}
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	probeCfg(cfg)

	reg := bootstrap.NewConfigRegistry()
	reg.MustRegister("fake", func(context.Context, *bootstrapv1.BootstrapConfig) (*baldconfig.Layer, func(), error) {
		return &baldconfig.Layer{
			Name:   "fake",
			Reader: &fakeReader{data: []byte("script:\n  options:\n    enabled: true\n")},
		}, nil, nil
	})

	a, err := FromBootstrap(cfg,
		WithConfigNamespace(testConfigNamespace), WithHTTP(new(http.ServeMux)),
		WithConfigRegistry(reg), probeLoggerOption(wl),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	runBriefly(t, a, 50*time.Millisecond)

	if got := cfg.GetScript().GetOptions().GetEnabled(); !got {
		t.Fatal("script section should be loaded from config layer")
	}
	var found string
	for _, line := range wl.lines() {
		if strings.Contains(line, "no consumer") {
			found = line
		}
	}
	if len(found) == 0 {
		t.Fatalf("expected unconsumed-section warning for file-contributed script, got %v", wl.lines())
	}
	if !strings.Contains(found, "sections script") {
		t.Fatalf("warning should list exactly script, got: %s", found)
	}
	t.Logf("captured warning: %s", found)
}

// 零能力声明（一对钉子，按「是否偏离框架预填默认值」收窄 server 告警）：
//
// ①未改动的预填默认 server 段（bconf.NewBootstrap 的 http :8080 / grpc
// :9090）→ 不告警——预填是框架自己的行为，对其告警属自相矛盾的噪音；
// ②用户覆盖了任一 server 子字段 → 照实告警——该段配置确实不产生任何
// 服务器（未声明能力），告警可行动（等待实现或从契约移除）。
func TestFromBootstrap_ZeroCapabilityDefaultServerNoWarn(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	wl := &warnLogger{}
	cfg := bconf.NewBootstrap()
	probeCfg(cfg)

	a, err := FromBootstrap(cfg, WithConfigNamespace(testConfigNamespace), probeLoggerOption(wl))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	runBriefly(t, a, 30*time.Millisecond)

	for _, line := range wl.lines() {
		if strings.Contains(line, "no consumer") {
			t.Fatalf("preset-default server section should not warn, got: %s", line)
		}
	}
}

func TestFromBootstrap_ZeroCapabilityOverriddenServerWarns(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	wl := &warnLogger{}
	cfg := bconf.NewBootstrap()
	probeCfg(cfg)
	cfg.GetServer().GetHttp().Addr = "127.0.0.1:18080" // 用户覆盖：偏离预填默认

	a, err := FromBootstrap(cfg, WithConfigNamespace(testConfigNamespace), probeLoggerOption(wl))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	runBriefly(t, a, 30*time.Millisecond)

	var found string
	for _, line := range wl.lines() {
		if strings.Contains(line, "no consumer") {
			found = line
		}
	}
	if len(found) == 0 {
		t.Fatalf("overridden server section should warn for zero-capability config, got %v", wl.lines())
	}
	if !strings.Contains(found, "sections server") {
		t.Fatalf("warning should list exactly server, got: %s", found)
	}
	t.Logf("captured warning: %s", found)
}
