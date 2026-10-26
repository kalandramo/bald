// Package app 是 bald-demo-worker 的入口（对照生成物 cmd/<app>/app/command.go）：
// 只把角色交给装配层，自身不含装配知识。
package app

import (
	"github.com/kalandramo/bald/example/bald/internal/bootstrap"
	"github.com/kalandramo/bald/example/bald/internal/cli"
)

// Execute 运行 worker 角色的根命令（serve 为默认行为）。
func Execute() error {
	return cli.Main(bootstrap.RoleWorker, "bald-demo-worker", "bald 服务框架示例（worker 角色：只开 gRPC 面）")
}
