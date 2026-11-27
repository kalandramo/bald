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
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	doctor       func(context.Context) error
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

// WithDoctor 挂 doctor 子命令：fn 由应用提供（典型为 appkit.Doctor +
// 同一份装配 Options——doctor 报告必须与真实启动同源）。未挂时不出现该命令。
func WithDoctor(fn func(context.Context) error) Option {
	return func(c *config) { c.doctor = fn }
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
	// k8s 风格 flag 错误 UX：平时静默
	// usage——运行期错误只留报错本身；仅 flag 解析失败时回显 usage。
	root.SilenceUsage = true
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		cmd.SilenceUsage = false
		return err
	})
	root.Flags().SortFlags = true
	if v := resolveVersion(c); v != "" {
		root.Version = v              // cobra 据此自动挂 --version
		root.InitDefaultVersionFlag() // 构建期即挂，不拖到 Execute
		root.AddCommand(&cobra.Command{
			Use:   "version",
			Short: "Print the version",
			Args:  cobra.NoArgs,
			Run: func(cmd *cobra.Command, _ []string) {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", use, v)
				writeBuildInfo(cmd.OutOrStdout())
			},
		})
	}
	if c.doctor != nil {
		root.AddCommand(&cobra.Command{
			Use:   "doctor",
			Short: "Print assembly plan diagnostics (summary, opaque nodes, segment coverage)",
			// --config 等框架 flag 由 appkit 自行扫描 os.Args（与 serve 同路径），
			// cobra 不定义它们：关闭 flag 解析，参数原样留给 appkit（Args 不校验）。
			DisableFlagParsing: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return c.doctor(cmd.Context())
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
		return bi.Main.Version //cov:excl go test 二进制里 buildinfo 恒为 (devel)，仅 go install 真构建可达
	}
	return ""
}

// writeBuildInfo 打印本地可核的构建 provenance（DX-2）：二进制路径 +
// Go module 路径 + VCS revision/time/dirty。用于回答「PATH 命中的是哪一个
// 二进制、它从哪个提交构建」——真实 GOPATH 下两个同名二进制并存时是
// 第一诊断项。checklist 中的「与远端最新 tag 差距」不在这里做：框架
// 二进制不应在只读的 version 命令里联网（J3 出站有界 + doctor 不联网），
// 远端 tag 由发布流程（CHANGELOG / release plan）负责，本命令只给可核事实。
func writeBuildInfo(w io.Writer) {
	exe, err := os.Executable()
	if err != nil {
		exe = "(unknown)"
	}
	abs, err := filepath.Abs(exe)
	if err == nil {
		exe = abs
	}
	fmt.Fprintf(w, "binary: %s\n", exe)

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	fmt.Fprintf(w, "module: %s\n", bi.Main.Path)
	var revision, commitTime, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			commitTime = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if revision != "" {
		fmt.Fprintf(w, "vcs.revision: %s\n", revision)
	}
	if commitTime != "" {
		fmt.Fprintf(w, "vcs.time: %s\n", commitTime)
	}
	if modified != "" {
		fmt.Fprintf(w, "vcs.modified: %s\n", modified)
	}
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
