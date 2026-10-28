// Package cli 构造示例进程的 cobra 入口（角色 → 命令树）。
//
// 角色与二进制一一对应：cmd/bald-demo（api 角色）与 cmd/bald-demo-worker
// （worker 角色）各自的 main.go → app.Execute → 本包——入口里没有装配知识，
// 与生成物 cmd/<app>/app/command.go 的分工一致（Execute 的唯一职责是把角色
// 交给 bootstrap.Options）。两个角色共用本包，故没有两份复制的命令树。
//
// CLI 机制（根命令构建、FParseErr 白名单、flag 错误 UX、--version、
// kubectl 风格插件转发）全部在 pkg/appkit/appcli——本包只声明挂哪些业务
// 子命令与跑哪个角色。插件前缀默认取二进制名：bald-demo-<name>。
package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/appkit/appcli"

	"github.com/kalandramo/bald/example/bald/internal/bootstrap"
	usercmd "github.com/kalandramo/bald/example/bald/user"
)

// Main 运行指定角色的根命令（serve 为默认行为）。
//
// use 是二进制名（cobra 的 Use），short 是一句话说明；/--config、--log.* 等
// 框架 flag 由 MustServe 内化的 parseConfigFlag 自行扫描 os.Args，不在此 Bind。
func Main(role, use, short string) error {
	return appcli.Main(use, short, func(*cobra.Command, []string) error {
		// 唯一装配入口：Options(role) 是装配投影（internal/bootstrap）。
		// 失败在 MustServe 内记日志并以非零码退出。
		appkit.MustServe(context.Background(), bootstrap.Options(role)...)
		return nil
	},
		// 存储后端切换演示子命令（业务子命令挂 root，与 serve 并存）。
		appcli.WithSubcommands(usercmd.NewUserCommand()),
		appcli.WithPlugins(),
		// doctor：与 serve 同一份装配 Options——报告必须与真实启动同源。
		appcli.WithDoctor(func(ctx context.Context) error {
			return appkit.Doctor(ctx, bootstrap.Options(role)...)
		}),
	)
}
