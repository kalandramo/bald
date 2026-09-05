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

// TestBind_FlagOverridesFile 验证阶段 0 的修复：经 appkit.Bind 注册的业务 flag
// 进入配置装载 flag 层，能压过本地配置文件。
//
// 修复前：业务 flag 只注册到 pflag.CommandLine，config.Load 拿不到它们，
// 优先级链中的 flag 层实际为空（见配置中心设计文档 9.1 节）。
func TestBind_FlagOverridesFile(t *testing.T) {
	cfgFile := writeCfg(t, "http:\n  addr: \":8080\"\ngrpc:\n  addr: \":9090\"\n")
	withArgs(t, []string{"--config=" + cfgFile, "--http.addr=:18080"})

	httpOpts := &bootstrapv1.Server_Http{}
	grpcOpts := &bootstrapv1.Server_Grpc{}

	a := New(Name("bald-test"),
		Bind("http", httpOpts),
		Bind("grpc", grpcOpts),
	)

	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	// flag 应压过本地文件。
	if got := a.Config().GetString("http.addr"); got != ":18080" {
		t.Errorf("http.addr = %q, want :18080 (flag 应压过本地文件)", got)
	}
	// 未通过 flag 覆盖的段仍取自本地文件。
	if got := a.Config().GetString("grpc.addr"); got != ":9090" {
		t.Errorf("grpc.addr = %q, want :9090 (来自本地文件)", got)
	}
}

// TestBind_EnvOverridesFile 验证 env 仍压过本地文件（Bind 不应破坏既有优先级）。
func TestBind_EnvOverridesFile(t *testing.T) {
	cfgFile := writeCfg(t, "http:\n  addr: \":8080\"\n")
	withArgs(t, []string{"--config=" + cfgFile})

	t.Setenv("BALD_TEST_HTTP_ADDR", ":28080")

	a := New(Name("bald-test"), Bind("http", &bootstrapv1.Server_Http{}))
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := a.Config().GetString("http.addr"); got != ":28080" {
		t.Errorf("http.addr = %q, want :28080 (env 应压过本地文件)", got)
	}
}

// TestBind_FlagOverridesEnv 验证完整优先级链：flag > env > 本地文件。
func TestBind_FlagOverridesEnv(t *testing.T) {
	cfgFile := writeCfg(t, "http:\n  addr: \":8080\"\n")
	withArgs(t, []string{"--config=" + cfgFile, "--http.addr=:38080"})
	t.Setenv("BALD_TEST_HTTP_ADDR", ":28080")

	a := New(Name("bald-test"), Bind("http", &bootstrapv1.Server_Http{}))
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := a.Config().GetString("http.addr"); got != ":38080" {
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

	a := New(Name("bald-test"), Bind("grpc", &bootstrapv1.Server_Grpc{}))
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := a.Config().GetString("grpc.addr"); got != ":9090" {
		t.Errorf("grpc.addr = %q, want :9090", got)
	}
	if got := a.Config().GetString("grpc.reflection"); got != "true" {
		t.Errorf("grpc.reflection = %q, want true", got)
	}
}

// TestBind_PlainBinder 验证无前缀 binder（如 log.Options 固定注册 --log.*）。
func TestBind_PlainBinder(t *testing.T) {
	cfgFile := writeCfg(t, "log:\n  level: \"info\"\n")
	withArgs(t, []string{"--config=" + cfgFile, "--log.level=debug"})

	a := New(Name("bald-test"), Bind("", &plainBinder{}))
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := a.Config().GetString("log.level"); got != "debug" {
		t.Errorf("log.level = %q, want debug", got)
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
			a := New(Name("bald-test"), Bind(tc.prefix, tc.opt))
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
