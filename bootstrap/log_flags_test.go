package bootstrap

import (
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log/bslog"
	"github.com/spf13/pflag"
)

// parseLogFlags 用**真注册器**造一个 FlagSet 并按 args 解析——flag 名与被测代码同源，
// 不手抄，也不会因为改名而测试先绿。
func parseLogFlags(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	bslog.NewOptions().AddFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return fs
}

func slogBackend(level, format string) *bootstrapv1.BootstrapConfig {
	return &bootstrapv1.BootstrapConfig{Logger: &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{{
			Type: "slog",
			Slog: &bootstrapv1.Logger_Slog{Level: level, Format: format},
		}},
	}}
}

func firstSlog(t *testing.T, cfg *bootstrapv1.BootstrapConfig) *bootstrapv1.Logger_Slog {
	t.Helper()
	bs := cfg.GetLogger().GetBackends()
	if len(bs) == 0 {
		t.Fatalf("logger.backends 为空，取不到 slog 段：%v", cfg.GetLogger())
	}
	if bs[0].GetSlog() == nil {
		t.Fatalf("backends[0] 没有 slog 段：%v", bs[0])
	}
	return bs[0].GetSlog()
}

func TestApplyLogFlagOverrides(t *testing.T) {
	t.Run("一个 flag 都没出现 ⇒ 契约零改动", func(t *testing.T) {
		cfg := slogBackend("warn", "json")
		ApplyLogFlagOverrides(cfg, parseLogFlags(t))
		if got := firstSlog(t, cfg); got.GetLevel() != "warn" || got.GetFormat() != "json" {
			t.Fatalf("未出现的 flag 改动了契约：%+v", got)
		}
		if n := len(cfg.GetLogger().GetBackends()); n != 1 {
			t.Fatalf("后端数量被改动：%d", n)
		}
		// 整段缺失时也不能被凭空物化——否则「没传 flag」与「传了 flag」不可区分。
		empty := &bootstrapv1.BootstrapConfig{}
		ApplyLogFlagOverrides(empty, parseLogFlags(t))
		if empty.GetLogger() != nil {
			t.Fatalf("无 flag 时物化出了 logger 段：%v", empty.GetLogger())
		}
	})

	t.Run("只覆盖出现过的键，其余保留契约值", func(t *testing.T) {
		cfg := slogBackend("warn", "json")
		ApplyLogFlagOverrides(cfg, parseLogFlags(t, "--log.level=error"))
		got := firstSlog(t, cfg)
		if got.GetLevel() != "error" {
			t.Fatalf("level 未被覆盖：%q", got.GetLevel())
		}
		if got.GetFormat() != "json" {
			t.Fatalf("未出现的 format 被 flag 默认值压掉了：%q", got.GetFormat())
		}
		// 终值必须真的到达 bslog.Options（否则只是改了个没人读的消息）。
		if o := LogOptions(cfg.GetLogger().GetBackends()[0]); o.Level != "error" || o.Format != "json" {
			t.Fatalf("LogOptions 没拿到覆盖后的值：%+v", o)
		}
	})

	t.Run("logger 段缺失时按 flag 物化 slog 后端", func(t *testing.T) {
		cfg := &bootstrapv1.BootstrapConfig{}
		ApplyLogFlagOverrides(cfg, parseLogFlags(t, "--log.level=debug"))
		o := LogOptions(cfg.GetLogger().GetBackends()[0])
		if o.Level != "debug" {
			t.Fatalf("物化后 level 不对：%+v", o)
		}
		if cfg.GetLogger().GetBackends()[0].GetType() != "slog" {
			t.Fatal("物化的后端类型应为 slog")
		}
	})

	t.Run("没有 slog 后端时跳过（不追加出口）", func(t *testing.T) {
		cfg := &bootstrapv1.BootstrapConfig{Logger: &bootstrapv1.Logger{
			Backends: []*bootstrapv1.Logger_Backend{{Type: "loki"}},
		}}
		ApplyLogFlagOverrides(cfg, parseLogFlags(t, "--log.level=error"))
		if n := len(cfg.GetLogger().GetBackends()); n != 1 || cfg.GetLogger().GetBackends()[0].GetSlog() != nil {
			t.Fatalf("给非 slog 部署追加了出口：n=%d %v", n, cfg.GetLogger())
		}
	})

	t.Run("rotate 与多输出全键覆盖", func(t *testing.T) {
		cfg := slogBackend("", "")
		ApplyLogFlagOverrides(cfg, parseLogFlags(t,
			"--log.output-paths=stdout,/tmp/bald.log",
			"--log.rotate.enabled=true", "--log.rotate.max-size=7",
			"--log.rotate.max-backups=3", "--log.rotate.max-age=5", "--log.rotate.compress=false"))
		s := firstSlog(t, cfg)
		if s.GetRotate().GetEnabled() != true || s.GetRotate().GetMaxSize() != 7 ||
			s.GetRotate().GetMaxBackups() != 3 || s.GetRotate().GetMaxAge() != 5 ||
			s.GetRotate().GetCompress() != false {
			t.Fatalf("rotate 覆盖不全：%+v", s.GetRotate())
		}
		o := LogOptions(cfg.GetLogger().GetBackends()[0])
		if len(o.OutputPaths) != 2 || o.OutputPaths[1] != "/tmp/bald.log" {
			t.Fatalf("多输出没落到 bslog.Options：%+v", o.OutputPaths)
		}
		if !o.Rotate.Enabled || o.Rotate.MaxSize != 7 || o.Rotate.MaxAge != 5 {
			t.Fatalf("轮转终值不对：%+v", o.Rotate)
		}
		// 契约带的是显式 false，但 LogOptions 的语义是「0/false＝不表态、保留默认」，
		// 所以 compress=false 压不过 bslog 的默认 true。这条不是本轮引入的映射瑕疵，
		// 是 proto3 标量无 presence 的既有代价——见登记册 §7.39「残余限制」。
		if s.GetRotate().GetCompress() != false {
			t.Fatalf("契约里 compress 应为显式 false：%+v", s.GetRotate())
		}
		if !o.Rotate.Compress {
			t.Fatal("默认 true 被 false 压掉了——与 LogOptions 的「0=默认」口径不符")
		}
	})

	t.Run("CLI 置真假不过契约置真（无 presence 的既有代价，如实钉住）", func(t *testing.T) {
		cfg := slogBackend("", "")
		cfg.GetLogger().GetBackends()[0].Slog.Rotate = &bootstrapv1.Logger_Slog_Rotate{
			Enabled: true, Compress: true,
		}
		ApplyLogFlagOverrides(cfg, parseLogFlags(t, "--log.rotate.enabled=false", "--log.rotate.compress=false"))
		r := firstSlog(t, cfg).GetRotate()
		// 契约被写成 false（CLI 确实表态了），但装配层的「false=不表态」让它压不过默认。
		if r.GetEnabled() || r.GetCompress() {
			t.Fatalf("覆盖没写进契约：%+v", r)
		}
		o := LogOptions(cfg.GetLogger().GetBackends()[0])
		if !o.Rotate.Enabled {
			t.Log("已知限制实测成立：--log.rotate.enabled=false 无法关闭契约里已开的轮转")
		}
	})

	t.Run("nil 入参不炸（契约未装载/无 flag 层的调用点）", func(t *testing.T) {
		ApplyLogFlagOverrides(nil, parseLogFlags(t, "--log.level=error"))
		cfg := slogBackend("warn", "json")
		ApplyLogFlagOverrides(cfg, nil)
		if firstSlog(t, cfg).GetLevel() != "warn" {
			t.Fatal("fs==nil 时改动了契约")
		}
	})
}

// TestLogFlagNamesMatchAppliedKeys 双向对账 LogFlagNames 与被 ApplyLogFlagOverrides
// 逐个读取的键：装配层拿前者把这些键摘出合并树（§7.39），后者把它们写进契约。
// 两份清单一旦分叉就有真实伤害——注册了但没摘 ⇒ 严格契约判未知键终止启动；
// 摘了但没覆盖 ⇒ flag 能解析、值永远不生效（比报错更难发现）。
func TestLogFlagNamesMatchAppliedKeys(t *testing.T) {
	applied := map[string]bool{
		flagLogLevel: true, flagLogFormat: true, flagLogOutputPaths: true,
		flagLogRotateEnabled: true, flagLogRotateMaxSize: true, flagLogRotateMaxBacks: true,
		flagLogRotateMaxAge: true, flagLogRotateCompress: true,
	}
	names := LogFlagNames()
	if len(names) == 0 {
		t.Fatal("LogFlagNames 返回空 ⇒ bslog 的注册面没被派生到，本闸门在空转")
	}
	for _, n := range names {
		if !applied[n] {
			t.Errorf(`注册的 flag %q 不在 ApplyLogFlagOverrides 认识的键里：它会被摘出合并树却永远不生效`, n)
		}
		delete(applied, n)
	}
	for n := range applied {
		t.Errorf(`ApplyLogFlagOverrides 读取 %q，但 bslog 没注册它：Changed 永远为 false，该旋钮静默失效`, n)
	}
}

// TestLogFlagNamesDerivedFromRegistrar 钉住 LogFlagNames 是派生而不是手抄：
// 它必须与直接调用注册器得到的名字集合逐项相等（顺序按 pflag 的字典序）。
func TestLogFlagNamesDerivedFromRegistrar(t *testing.T) {
	fs := pflag.NewFlagSet("expect", pflag.ContinueOnError)
	bslog.NewOptions().AddFlags(fs)
	var want []string
	fs.VisitAll(func(f *pflag.Flag) { want = append(want, f.Name) })

	got := LogFlagNames()
	if len(got) != len(want) {
		t.Fatalf("LogFlagNames 数量 = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("LogFlagNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if len(want) != 8 {
		t.Errorf("注册器派生到 %d 个 flag，与实测基线 8 不符：%v", len(want), want)
	}
}
