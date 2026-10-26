// Package appcli 是业务二进制的 CLI 机制层——单轨：每个项目都要写的
// cobra 壳（根命令构建、flag 错误 UX、--version、kubectl 风格插件转发）
// 收敛在这里，生成物与业务入口只声明「这个角色跑什么」。
//
// 边界（与 appkit 的分工）：配置/契约/装配生命周期全部归 appkit
// （MustServe 内化的 parseConfigFlag 自行扫描 --config / --log.* / server.*
// 等框架 flag）；appcli 不碰配置、不碰装配，只造命令树并执行。
//
// 版本注入：-ldflags -X github.com/kalandramo/bald/pkg/appkit/appcli.Version=v1.2.3；
// 未注入时回退 debug.ReadBuildInfo 的模块版本（go install 场景），再不行
// 不挂 --version（开发构建里它没有意义）。
package appcli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// Version 由构建时 -ldflags -X 注入；WithVersion 的显式值优先于它。
var Version = ""

type config struct {
	sub          []*cobra.Command
	long         string
	args         cobra.PositionalArgs
	plugins      bool
	pluginPrefix string
	version      string
}

// Option 是 Main 的可选行为。
type Option func(*config)

// WithSubcommands 挂业务子命令（与默认 serve 行为并存）。
func WithSubcommands(cmds ...*cobra.Command) Option {
	return func(c *config) { c.sub = append(c.sub, cmds...) }
}

// WithDescription 设根命令 Long（Use 之后的详细说明）。
func WithDescription(long string) Option {
	return func(c *config) { c.long = long }
}

// WithArgs 设位置参数校验（如 cobra.NoArgs）。
func WithArgs(a cobra.PositionalArgs) Option {
	return func(c *config) { c.args = a }
}

// WithPlugins 开启 kubectl 风格插件转发：首参不是已知子命令时，执行
// PATH 里的 <前缀>-<首参> 可执行文件；前缀默认取二进制名（use）。
func WithPlugins() Option {
	return func(c *config) { c.plugins = true }
}

// WithPluginPrefix 指定插件前缀（隐含开启转发）。
func WithPluginPrefix(prefix string) Option {
	return func(c *config) { c.plugins = true; c.pluginPrefix = prefix }
}

// WithVersion 显式设版本串（优先于 ldflags 注入与构建信息）。
func WithVersion(v string) Option {
	return func(c *config) { c.version = v }
}

// BuildRoot 只造根命令（不执行）——测试与需要自持命令树的入口用。
func BuildRoot(use, short string, runE func(*cobra.Command, []string) error, opts ...Option) (*cobra.Command, *config) {
	c := &config{}
	for _, o := range opts {
		o(c)
	}
	root := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  c.long,
		RunE:  runE,
		Args:  c.args,
	}
	// --config / --log.* 等框架 flag 由 MustServe 内化的 parseConfigFlag
	// 自行扫描 os.Args，cobra 不得拒绝它们。
	root.FParseErrWhitelist.UnknownFlags = true
	// k8s 风格 flag 错误 UX（借 onexstack/pkg/app 的做法）：平时静默
	// usage——运行期错误只留报错本身；仅 flag 解析失败时回显 usage。
	root.SilenceUsage = true
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		cmd.SilenceUsage = false
		return err
	})
	root.Flags().SortFlags = true
	if v := resolveVersion(c); v != "" {
		root.Version = v // cobra 据此自动挂 --version
		root.InitDefaultVersionFlag() // 构建期即挂，不拖到 Execute
		root.AddCommand(&cobra.Command{
			Use:   "version",
			Short: "Print the version",
			Args:  cobra.NoArgs,
			Run: func(cmd *cobra.Command, _ []string) {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", use, v)
			},
		})
	}
	root.AddCommand(c.sub...)
	return root, c
}

// Main 构造根命令并执行（含插件转发）。返回 cobra 的执行错误，
// 调用方决定退出码（生成物入口 err 非 nil 时 os.Exit(1)）。
func Main(use, short string, runE func(*cobra.Command, []string) error, opts ...Option) error {
	root, c := BuildRoot(use, short, runE, opts...)
	if c.plugins {
		return runWithPlugin(root, pluginPrefix(c, use))
	}
	return root.Execute()
}

func resolveVersion(c *config) string {
	if c.version != "" {
		return c.version
	}
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return ""
}

func pluginPrefix(c *config, use string) string {
	if c.pluginPrefix != "" {
		return c.pluginPrefix
	}
	return use
}

// lookPath 与 runPlugin 是转发决策的两个可测缝。
var (
	lookPath  = exec.LookPath
	runPlugin = func(path string, args []string) error {
		cmd := exec.Command(path, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
)

func runWithPlugin(root *cobra.Command, prefix string) error {
	if len(os.Args) > 1 {
		sub := os.Args[1]
		if !strings.HasPrefix(sub, "-") && !isKnownCommand(root, sub) {
			bin := prefix + "-" + sub
			if path, err := lookPath(bin); err == nil {
				return runPlugin(path, os.Args[2:])
			}
			// 未找到插件：交回 cobra 打原生 unknown command 提示。
		}
	}
	return root.Execute()
}

func isKnownCommand(root *cobra.Command, name string) bool {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return true
		}
	}
	return false
}
