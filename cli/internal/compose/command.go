package compose

// command.go 是 `bald compose` 命令组：list / config / up / stop / down / status。
//
// 命令面按「改变系统状态的方向」分两层：
//
//	list、config、status  只读（打印目录 / compose 定义 / 容器状态）；
//	up、stop、down        改本机容器状态。
//
// 整组都**不进 MCP 面**，只读的那三个也不进：本机容器状态是人的环境，起停由人
// 决定（端口冲突、残留容器都是不可回滚的副作用）；只读信息在 `bald compose list`
// 与文档里已经够用，没必要为它多一个工具格子。这与 gen 的写命令同一口径——工具
// 面克制，不靠 destructiveHint 让客户端兜底。

import (
	"github.com/kalandramo/bald/cli/internal/fsatomic"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// Streams 是命令组的输入输出（测试注入弃流，生产两路都落到进程标准流）。
type Streams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

// NewDefaultStreams 用进程标准流构造（CLI 入口用）。
func NewDefaultStreams() Streams {
	return Streams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
}

// commonOptions 是 up/stop/down/status 共用的运行时配置。
type commonOptions struct {
	Docker  string // --docker：docker CLI 名（podman 等兼容实现可替换）
	File    string // --file：用户的 compose 文件（空 = 用内嵌目录的临时文件）
	DryRun  bool   // --dry-run：只打印 argv，不执行（可审计、可在无 docker 的机器上用）
	Streams Streams
}

func (o *commonOptions) complete() {
	if o.Docker == "" {
		o.Docker = defaultDocker
	}
}

func (o *commonOptions) validate() error {
	if o.DryRun {
		return nil
	}
	if _, err := exec.LookPath(o.Docker); err != nil {
		return fmt.Errorf("%s not found in PATH: %w (use --dry-run to inspect the command, or --docker to point at a compatible CLI)", o.Docker, err)
	}
	return nil
}

// composeArgs 组装 `docker compose -f <file> -p <project> <verb> [svc...]`。
//
// 显式 -p：不加就用目录名当项目名，`down` 会连带清掉同目录下用户自己的容器。
// 显式 -f：内嵌目录落临时文件，用户的 compose.yaml 不会被改动或误用。
func (o *commonOptions) composeArgs(file, verb string, services []string) []string {
	args := []string{"compose", "-f", file, "-p", projectName, verb}
	return append(args, services...)
}

// run 执行（或打印）一条 compose 子命令，stdout/stderr 直连进程流。
func (o *commonOptions) run(args []string) error {
	if o.DryRun {
		fmt.Fprintln(o.Streams.Out, o.Docker+" "+strings.Join(args, " "))
		return nil
	}
	cmd := exec.Command(o.Docker, args...)
	cmd.Stdout = o.Streams.Out
	cmd.Stderr = o.Streams.ErrOut
	cmd.Stdin = o.Streams.In
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", o.Docker, strings.Join(args, " "), err)
	}
	return nil
}

// filePath 返回本次调用使用的 compose 文件：用户的（--file）或内嵌目录的临时文件。
// 返回的第二值是清理函数（临时文件用完即删，不在仓库里留垃圾）。
func (o *commonOptions) filePath(services []service) (string, func(), error) {
	if o.File != "" {
		if _, err := os.Stat(o.File); err != nil {
			return "", nil, fmt.Errorf("compose file %s: %w", o.File, err)
		}
		return o.File, func() {}, nil
	}
	f, err := os.CreateTemp("", "bald-compose-*.yaml")
	if err != nil {
		return "", nil, fmt.Errorf("create temp compose file: %w", err)
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := f.WriteString(renderYAML(services)); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, fmt.Errorf("write temp compose file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close temp compose file: %w", err)
	}
	return name, cleanup, nil
}

// NewCommand 构造 compose 命令树。
func NewCommand(streams Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compose [command]",
		Short: "本地集成依赖的 compose 定义与生命周期（etcd / redis / nacos）",
		Long: `把本地集成依赖收敛成一份 compose 定义，并代为调用 docker compose。

可起的服务（与 CI integration 作业的 services 块逐项同源：镜像 tag、端口映射、
env 一致）：

  etcd     quay.io/coreos/etcd:v3.5.14      127.0.0.1:2379
  consul   hashicorp/consul:1.19.2          127.0.0.1:8500
  redis    redis:7-alpine                   127.0.0.1:6379
  nacos    nacos/nacos-server:v2.3.2        127.0.0.1:8848 (+9848 gRPC)

端口只绑 127.0.0.1（不对外暴露），项目名固定为 ` + "`" + projectName + "`" + `（down 一次清干净，
不与用户自己的容器混）。up 之后不等待健康——探活由测试自己做，环境没起来不该
被 Skip 掩埋。

  bald compose list                       # 目录（名字 / 镜像 / 本地地址）
  bald compose config                     # 打印 compose 定义（可 --out 落盘改）
  bald compose up [etcd redis]            # 起（缺省全起）
  bald compose status                     # 容器状态（docker compose ps）
  bald compose stop [etcd]                # 停（保留容器）
  bald compose down                       # 删除本项目的容器与网络

--dry-run 打印将要执行的 docker argv 而不执行：既能审计（AI 会话里先看清楚再跑），
也能在没有 docker 的机器上验证命令装配。`,
		SilenceUsage: true,
		RunE:         func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newListCmd(streams), newConfigCmd(streams), newUpCmd(streams),
		newStatusCmd(streams), newStopCmd(streams), newDownCmd(streams))
	cmd.SetErr(streams.ErrOut)
	return cmd
}

// newListCmd 打印目录（只读，可进 MCP 面）。
func newListCmd(streams Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出可起的服务（名字 / 镜像 / 本地地址）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprint(streams.Out, listText())
			return nil
		},
	}
	return cmd
}

// newConfigCmd 打印或落盘 compose 定义（只读；--out 才写用户指定的文件）。
func newConfigCmd(streams Streams) *cobra.Command {
	var out string
	var only []string
	cmd := &cobra.Command{
		Use:   "config",
		Short: "打印 compose 定义（--out 落盘成可定制的文件）",
		Long: `打印内嵌的 compose 定义。

不带 --out 打到 stdout（可 >> compose.yaml 再手改）；带 --out 写文件，已存在即
拒绝（要覆盖请先删——生成器不猜「你想不想覆盖」）。定制后配 --file 使用：

  bald compose config --out compose.yaml
  bald compose up --file compose.yaml`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			services, err := selectServices(only)
			if err != nil {
				return err
			}
			text := renderYAML(services)
			if out == "" {
				fmt.Fprint(streams.Out, text)
				return nil
			}
			if _, err := os.Stat(out); err == nil {
				return fmt.Errorf("%s already exists, refusing to overwrite (remove it or pick another --out)", out)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("stat %s: %w", out, err)
			}
			if err := fsatomic.WriteFile(out, []byte(text), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", out, err)
			}
			fmt.Fprintln(streams.Out, "generated:", out)
			fmt.Fprintln(streams.Out, "next: edit it, then `bald compose up --file "+out+"`")
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "落盘路径（默认打印到 stdout）")
	cmd.Flags().StringSliceVar(&only, "service", nil, "只输出这些服务（默认全部）")
	return cmd
}

// newUpCmd 起容器（写动作，不进 MCP 面）。
func newUpCmd(streams Streams) *cobra.Command {
	o := &commonOptions{Streams: streams}
	cmd := &cobra.Command{
		Use:   "up [service...]",
		Short: "起本地集成依赖（缺省全起，后台运行）",
		Long: `起容器（docker compose up -d）。

缺省起全部；给名字只起这些。起完不等待健康——探活是测试的事（与 CI 的
「探不到即失败，不放行成 Skip」同一口径）。服务地址见 ` + "`bald compose list`" + `。`,
		RunE: func(_ *cobra.Command, args []string) error {
			o.complete()
			services, err := selectServices(args)
			if err != nil {
				return err
			}
			if err := o.validate(); err != nil {
				return err
			}
			file, cleanup, err := o.filePath(services)
			if err != nil {
				return err
			}
			defer cleanup()
			names := sortedNames(services)
			if err := o.run(o.composeArgs(file, "up", append([]string{"-d"}, names...))); err != nil {
				return err
			}
			if !o.DryRun {
				fmt.Fprintln(o.Streams.Out, "up:", strings.Join(names, " "), "(check with `bald compose status`)")
			}
			return nil
		},
	}
	bindCommonFlags(cmd, o)
	return cmd
}

// newStatusCmd 打印容器状态（只读，但需要 docker）。
func newStatusCmd(streams Streams) *cobra.Command {
	o := &commonOptions{Streams: streams}
	cmd := &cobra.Command{
		Use:   "status [service...]",
		Short: "打印本项目容器的状态（docker compose ps）",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			o.complete()
			services, err := selectServices(args)
			if err != nil {
				return err
			}
			if err := o.validate(); err != nil {
				return err
			}
			file, cleanup, err := o.filePath(services)
			if err != nil {
				return err
			}
			defer cleanup()
			// 空服务集 = 全项目（compose ps 不带名字时列本项目所有容器）。
			verb := []string{"ps"}
			if len(args) > 0 {
				verb = append(verb, sortedNames(services)...)
			}
			return o.run(o.composeArgs(file, verb[0], verb[1:]))
		},
	}
	bindCommonFlags(cmd, o)
	return cmd
}

// newStopCmd 停容器（写动作）。
func newStopCmd(streams Streams) *cobra.Command {
	o := &commonOptions{Streams: streams}
	cmd := &cobra.Command{
		Use:   "stop [service...]",
		Short: "停容器但保留（缺省全停）",
		RunE: func(_ *cobra.Command, args []string) error {
			o.complete()
			services, err := selectServices(args)
			if err != nil {
				return err
			}
			if err := o.validate(); err != nil {
				return err
			}
			file, cleanup, err := o.filePath(services)
			if err != nil {
				return err
			}
			defer cleanup()
			return o.run(o.composeArgs(file, "stop", sortedNames(services)))
		},
	}
	bindCommonFlags(cmd, o)
	return cmd
}

// newDownCmd 删除本项目容器（写动作）。
func newDownCmd(streams Streams) *cobra.Command {
	o := &commonOptions{Streams: streams}
	cmd := &cobra.Command{
		Use:   "down",
		Short: "删除本项目的容器与网络（项目名固定，不碰用户自己的容器）",
		Long: `删除本项目（` + projectName + `）的容器与网络。

compose 的 down 不接受服务名——要单独停一个用 ` + "`bald compose stop <service>`" + `。`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			o.complete()
			if err := o.validate(); err != nil {
				return err
			}
			file, cleanup, err := o.filePath(catalog)
			if err != nil {
				return err
			}
			defer cleanup()
			if err := o.run(o.composeArgs(file, "down", []string{"--remove-orphans"})); err != nil {
				return err
			}
			if !o.DryRun {
				fmt.Fprintln(o.Streams.Out, "down: project", projectName, "removed")
			}
			return nil
		},
	}
	bindCommonFlags(cmd, o)
	return cmd
}

// bindCommonFlags 挂共用 flag（--docker/--file/--dry-run）。
func bindCommonFlags(cmd *cobra.Command, o *commonOptions) {
	cmd.Flags().StringVar(&o.Docker, "docker", defaultDocker, "docker CLI（podman 等兼容实现可替换）")
	cmd.Flags().StringVar(&o.File, "file", "", "使用指定的 compose 文件（默认用内嵌目录生成的临时文件）")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "只打印将要执行的 docker argv，不执行")
	_ = cmd.MarkFlagFilename("file", "yaml", "yml")
	cmd.SilenceUsage = true
	cmd.SetErr(o.Streams.ErrOut)
}
