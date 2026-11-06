package appkit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// 保存并恢复 os.Args，避免测试间互相污染。
func withArgs(t *testing.T, args []string) {
	t.Helper()
	old := os.Args
	os.Args = append([]string{"bald-test"}, args...)
	t.Cleanup(func() { os.Args = old })
}

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bald-test.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

// TestBind_FlagOverridesFile 验证经 appkit.Bind 注册的业务 flag
// 进入配置装载 flag 层，能压过本地配置文件。
//
// 若业务 flag 只注册到 pflag.CommandLine，config.Load 拿不到它们，
// 优先级链中的 flag 层会实际为空（见 docs/devel/zh-CN/Bald 配置系统设计.md §3.6）。
func TestBind_FlagOverridesFile(t *testing.T) {
	cfgFile := writeCfg(t, "http:\n  addr: \":8080\"\ngrpc:\n  addr: \":9090\"\n")
	withArgs(t, []string{"--config=" + cfgFile, "--http.addr=:18080"})

	httpOpts := &bootstrapv1.Server_Http{}
	grpcOpts := &bootstrapv1.Server_Grpc{}

	a := NewApp(Name("bald-test"),
		Bind("http", httpOpts),
		Bind("grpc", grpcOpts),
	)

	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	// flag 应压过本地文件。
	if got := readSetting(a, "http.addr"); got != ":18080" {
		t.Errorf("http.addr = %q, want :18080 (flag 应压过本地文件)", got)
	}
	// 未通过 flag 覆盖的段仍取自本地文件。
	if got := readSetting(a, "grpc.addr"); got != ":9090" {
		t.Errorf("grpc.addr = %q, want :9090 (来自本地文件)", got)
	}
}

// TestBind_EnvOverridesFile 验证 env 仍压过本地文件（Bind 不应破坏既有优先级）。
func TestBind_EnvOverridesFile(t *testing.T) {
	cfgFile := writeCfg(t, "http:\n  addr: \":8080\"\n")
	withArgs(t, []string{"--config=" + cfgFile})

	t.Setenv("BALD_TEST_HTTP_ADDR", ":28080")

	a := NewApp(Name("bald-test"), Bind("http", &bootstrapv1.Server_Http{}))
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := readSetting(a, "http.addr"); got != ":28080" {
		t.Errorf("http.addr = %q, want :28080 (env 应压过本地文件)", got)
	}
}

// TestBind_FlagOverridesEnv 验证完整优先级链：flag > env > 本地文件。
func TestBind_FlagOverridesEnv(t *testing.T) {
	cfgFile := writeCfg(t, "http:\n  addr: \":8080\"\n")
	withArgs(t, []string{"--config=" + cfgFile, "--http.addr=:38080"})
	t.Setenv("BALD_TEST_HTTP_ADDR", ":28080")

	a := NewApp(Name("bald-test"), Bind("http", &bootstrapv1.Server_Http{}))
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := readSetting(a, "http.addr"); got != ":38080" {
		t.Errorf("http.addr = %q, want :38080 (flag 应压过 env)", got)
	}
}

// TestBind_KeyPathAligned 验证 flag 名 / 配置键 / 配置文件键三者路径一致。
// 这是 flag 能生效的前提：Bind 用配置键前缀注册 flag，不额外叠加应用名。
func TestBind_KeyPathAligned(t *testing.T) {
	// bootstrapv1 形态下 grpc.reflection 是 bool 字段（legacy 的 http.tls.enabled
	// 在新契约里是消息形态 Server_TLS，非 nil 即启用，无 enabled 开关）。
	cfgFile := writeCfg(t, "grpc:\n  addr: \":9090\"\n  reflection: false\n")
	withArgs(t, []string{"--config=" + cfgFile, "--grpc.reflection=true"})

	a := NewApp(Name("bald-test"), Bind("grpc", &bootstrapv1.Server_Grpc{}))
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := readSetting(a, "grpc.addr"); got != ":9090" {
		t.Errorf("grpc.addr = %q, want :9090", got)
	}
	if got := readSetting(a, "grpc.reflection"); got != "true" {
		t.Errorf("grpc.reflection = %q, want true", got)
	}
}

// TestBind_PlainBinder 验证「无前缀 binder」这类自带键前缀的 flag 的落点：
// `--log.*` 的顶层键 `log` 不是契约路径，进合并树会被严格契约校验判成未知键
// 并终止启动（§7.39），所以它**必须摘出树**；同时 CLI 的终值还得能从 App 保留的
// 全量 FlagSet 读到 Changed——那是框架把 CLI 值写进契约 logger 段的唯一来源
// （baldbootstrap.ApplyLogFlagOverrides，见 bootstrap/log_flags.go）。
// 两个断言缺一侧都会退回缺陷：只查「不进树」会放行「flag 彻底失效」，
// 只查「能读到」会放行原来的「一用即终止启动」。
func TestBind_PlainBinder(t *testing.T) {
	cfgFile := writeCfg(t, "logger:\n  backends:\n    - type: slog\n      slog:\n        level: \"info\"\n")
	withArgs(t, []string{"--config=" + cfgFile, "--log.level=debug"})

	a := NewApp(Name("bald-test"),
		StrictContract(),
		Bind("", &plainBinder{}),
	)
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// ① 契约外路由不进合并树；文件里的契约段原样保留。
	// （顶层键判据而非点路径：config.At 不索引 repeated，
	// `logger.backends[0].slog.level` 无法按字符串路径读取。）
	snap := a.Settings()
	if _, ok := snap["log"]; ok {
		t.Error(`Settings() 含顶层键 "log"：契约外 flag 路由未摘净，严格契约会终止启动`)
	}
	if _, ok := snap["logger"]; !ok {
		t.Error(`Settings() 缺顶层键 "logger"：文件层的契约取值不该被摘掉`)
	}
	// ② 覆盖来源仍在：全量 FlagSet 带 Changed 与终值。
	if !a.cfg.flagSet.Changed("log.level") {
		t.Error("flagSet.Changed(log.level) = false, want true（CLI 覆盖来源丢失）")
	}
	if v, err := a.cfg.flagSet.GetString("log.level"); err != nil || v != "debug" {
		t.Errorf("flagSet 的 log.level = %q (err %v), want debug", v, err)
	}
}

// TestBind_Errors 验证非法 Bind 入参被明确拒绝，而非静默失效。
func TestBind_Errors(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		opt    any
	}{
		{"未实现任何 AddFlags", "http", struct{}{}},
		{"nil", "http", nil},
		{"PlainBinder 传了非空 prefix", "log", &plainBinder{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withArgs(t, []string{})
			a := NewApp(Name("bald-test"), Bind(tc.prefix, tc.opt))
			if err := a.loadConfig(); err == nil {
				t.Errorf("loadConfig succeeded, want error")
			}
		})
	}
}

// plainBinder 模拟 log.Options 这类「键前缀内置」的配置对象。
type plainBinder struct {
	Level string
}

func (p *plainBinder) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&p.Level, "log.level", p.Level, "log level")
}
