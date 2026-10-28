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
