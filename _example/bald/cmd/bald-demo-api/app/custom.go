package app

import (
	"github.com/spf13/cobra"

	usercmd "github.com/kalandramo/bald/example/bald/user"
)

// customSubcommands 返回挂到根命令的业务子命令（与默认 serve 并存）。
func customSubcommands() []*cobra.Command {
	return []*cobra.Command{usercmd.NewUserCommand()}
}
