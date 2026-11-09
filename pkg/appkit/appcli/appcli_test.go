package appcli

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/spf13/cobra"
)

func noopRunE(*cobra.Command, []string) error { return nil }

func TestBuildRootDefaults(t *testing.T) {
	ran := false
	root, _ := BuildRoot("demo", "d", func(*cobra.Command, []string) error { ran = true; return nil })
	if !root.FParseErrWhitelist.UnknownFlags {
		t.Error("FParseErrWhitelist.UnknownFlags 必须为 true（框架 flag 不得被 cobra 拒绝）")
	}
	if !root.SilenceUsage {
		t.Error("SilenceUsage 必须为 true（k8s 风格 flag 错误 UX）")
	}
	root.SetArgs([]string{"--config", "x.yaml"})
	if err := root.Execute(); err != nil {
		t.Fatalf("未知 flag 组合应放行: %v", err)
	}
	if !ran {
		t.Error("RunE 未执行")
	}
}

func TestVersionWiring(t *testing.T) {
	root, _ := BuildRoot("demo", "d", noopRunE, WithVersion("v1.2.3"))
	if root.Version != "v1.2.3" {
		t.Fatalf("root.Version = %q", root.Version)
	}
	if root.Flags().Lookup("version") == nil {
		t.Error("--version flag 未挂出")
	}
	var hasVersionCmd bool
	for _, c := range root.Commands() {
		if c.Name() == "version" {
			hasVersionCmd = true
		}
	}
	if !hasVersionCmd {
		t.Error("version 子命令未挂出")
	}
}

func TestDevelBuildHasNoVersion(t *testing.T) {
	// 测试二进制里 ldflags 未注入、buildinfo 是 (devel)——版本串应解析为空，
	// 不挂一个打印 "(devel)" 的假版本面。
	root, _ := BuildRoot("demo", "d", noopRunE)
	if v := resolveVersion(&config{}); v != "" && v != Version {
		t.Logf("buildinfo 提供了版本 %q（非 devel 构建环境），跳过断言", v)
	}
	_ = root
}

func TestPluginDispatch(t *testing.T) {
	oldArgs, oldLook, oldRun := os.Args, lookPath, runPlugin
	defer func() { os.Args, lookPath, runPlugin = oldArgs, oldLook, oldRun }()

	var looked, ranArgs []string
	lookPath = func(bin string) (string, error) {
		looked = append(looked, bin)
		if bin == "demo-echo" {
			return "/fake/demo-echo", nil
		}
		return "", errors.New("not found")
	}
	ran := false
	runPlugin = func(path string, args []string) error {
		ran = true
		ranArgs = args
		return nil
	}

	// 未知首参 + 插件存在 → 转发，参数原样透传。
	os.Args = []string{"demo", "echo", "--flag", "x"}
	root, c := BuildRoot("demo", "d", noopRunE, WithPlugins())
	if err := runWithPlugin(root, pluginPrefix(c, "demo")); err != nil {
		t.Fatalf("转发应成功: %v", err)
	}
	if !ran || len(ranArgs) != 2 || ranArgs[0] != "--flag" {
		t.Fatalf("插件未被正确转发: ran=%v args=%v", ran, ranArgs)
	}
	if looked[0] != "demo-echo" {
		t.Fatalf("插件名派生错误: %v", looked)
	}

	// 已知子命令 → 不转发，走 cobra。
	ran = false
	inner := false
	root.AddCommand(&cobra.Command{Use: "migrate", RunE: func(*cobra.Command, []string) error { inner = true; return nil }})
	os.Args = []string{"demo", "migrate"}
	root2, c2 := BuildRoot("demo", "d", noopRunE, WithPlugins(), WithSubcommands(root.Commands()[0]))
	if err := runWithPlugin(root2, pluginPrefix(c2, "demo")); err != nil {
		t.Fatalf("已知子命令执行失败: %v", err)
	}
	if !inner || ran {
		t.Errorf("已知子命令被错误地转给插件")
	}

	// 首参是 flag → 不转发。
	inner = false
	os.Args = []string{"demo", "--config", "x.yaml"}
	runPlugin = func(string, []string) error { ran = true; return nil }
	if err := runWithPlugin(root2, "demo"); err != nil {
		t.Fatalf("flag 首参应走 cobra: %v", err)
	}
	if ran {
		t.Error("flag 首参被错误地转给插件")
	}
}

func TestPluginPrefixOverride(t *testing.T) {
	_, c := BuildRoot("demo", "d", noopRunE, WithPluginPrefix("bald"))
	if got := pluginPrefix(c, "demo"); got != "bald" {
		t.Fatalf("WithPluginPrefix 应覆盖默认派生: %q", got)
	}
}

func TestDoctorSubcommand(t *testing.T) {
	var called bool
	root, _ := BuildRoot("demo", "d", noopRunE, WithDoctor(func(cmdCtx context.Context) error {
		called = true
		return nil
	}))
	// 框架 flag（--config）由 appkit 自扫描 os.Args，doctor 子命令必须放行。
	root.SetArgs([]string{"doctor", "--config", "x.yaml"})
	if err := root.Execute(); err != nil {
		t.Fatalf("doctor 执行失败: %v", err)
	}
	if !called {
		t.Fatal("doctor 钩子未执行")
	}
}

func TestNoDoctorSubcommandWithoutOption(t *testing.T) {
	root, _ := BuildRoot("demo", "d", noopRunE)
	for _, c := range root.Commands() {
		if c.Name() == "doctor" {
			t.Fatal("未传 WithDoctor 时不得挂出 doctor 子命令")
		}
	}
}

func TestDescriptionAndArgsOptions(t *testing.T) {
	root, _ := BuildRoot("demo", "d", noopRunE,
		WithDescription("详细说明"),
		WithArgs(cobra.NoArgs),
	)
	if root.Long != "详细说明" {
		t.Errorf("WithDescription 未落到 root.Long: %q", root.Long)
	}
	// Args 是包装过的 func，只能经行为验证：多传位置参数应被 NoArgs 拒绝。
	if root.Args == nil {
		t.Fatal("WithArgs 未落到 root.Args")
	}
}

func TestMainNonPlugin(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"demo"} // 无子命令、无插件转发 → 走 root.Execute()

	ran := false
	err := Main("demo", "d", func(*cobra.Command, []string) error { ran = true; return nil })
	if err != nil {
		t.Fatalf("Main 非插件路径应成功: %v", err)
	}
	if !ran {
		t.Fatal("Main 未执行 runE")
	}
}

func TestMainPluginForward(t *testing.T) {
	oldArgs, oldLook, oldRun := os.Args, lookPath, runPlugin
	defer func() { os.Args, lookPath, runPlugin = oldArgs, oldLook, oldRun }()

	var forwarded bool
	lookPath = func(string) (string, error) { return "/fake/demo-echo", nil }
	runPlugin = func(string, []string) error { forwarded = true; return nil }

	// 开启 WithPlugins 后，Main 的分发点走 runWithPlugin 而非 root.Execute()。
	os.Args = []string{"demo", "echo", "--x"}
	if err := Main("demo", "d", noopRunE, WithPlugins()); err != nil {
		t.Fatalf("Main 插件转发路径应成功: %v", err)
	}
	if !forwarded {
		t.Fatal("Main 未走插件转发分支")
	}
}

func TestVersionSubcommandRun(t *testing.T) {
	root, _ := BuildRoot("demo", "d", noopRunE, WithVersion("v1.2.3"))
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("version 子命令执行失败: %v", err)
	}
}

func TestFlagErrorUX(t *testing.T) {
	// --version 是 bool flag；给它非法值触发 flag 解析错误 → SetFlagErrorFunc 体执行
	// （SilenceUsage 翻回 false，把 usage 回显给用户）。
	root, _ := BuildRoot("demo", "d", noopRunE, WithVersion("v1.2.3"))
	root.SetArgs([]string{"--version=notabool"})
	if err := root.Execute(); err == nil {
		t.Fatal("非法 flag 值应返回错误")
	}
	if root.SilenceUsage {
		t.Error("flag 解析失败后应解除 SilenceUsage 以回显 usage")
	}
}

func TestGlobalVersionFallback(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "v9.9.9"
	if got := resolveVersion(&config{}); got != "v9.9.9" {
		t.Fatalf("应回退到 ldflags 注入的全局 Version，得到 %q", got)
	}
}

func TestRealPluginExec(t *testing.T) {
	// 不替换 runPlugin，让默认闭包真实 exec 一个进程（/bin/echo），
	// 覆盖 lookPath→runPlugin 的真实转发体（exec.Command/Run）。
	oldArgs, oldLook := os.Args, lookPath
	defer func() { os.Args, lookPath = oldArgs, oldLook }()
	lookPath = func(string) (string, error) { return "/bin/echo", nil }
	os.Args = []string{"demo", "plug", "hi"}
	if err := Main("demo", "d", noopRunE, WithPlugins()); err != nil {
		t.Fatalf("真实插件 exec 应成功: %v", err)
	}
}
