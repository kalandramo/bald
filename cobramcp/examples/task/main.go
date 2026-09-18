package main

import (
	"os"
	"os/exec"

	"github.com/kalandramo/bald/cobramcp"
	"github.com/spf13/cobra"
)

func main() {
	if err := taskCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

type flags struct {
	Taskfile  string
	Directory string
}

func taskCmd() *cobra.Command {
	flags := &flags{}
	// Create the root task command
	cmd := &cobra.Command{
		Use:   "task [tasks...]",
		Short: "Execute task targets",
		RunE:  flags.run,
		Args:  cobra.ArbitraryArgs,
	}

	cmd.Flags().StringVarP(&flags.Taskfile, "taskfile", "t", "", "Choose which Taskfile to run")
	cmd.Flags().StringVarP(&flags.Directory, "dir", "d", "", "Directory in which Task will execute and look for a Taskfile")
	err := cmd.MarkFlagRequired("dir")
	if err != nil {
		panic(err)
	}

	// Add subcommands
	cmd.AddCommand(cobramcp.Command(nil))
	return cmd
}

func (f *flags) run(cmd *cobra.Command, args []string) error {
	// Flags precede the task names: `task -d DIR -t FILE <tasks...>`.
	// (Task 3.x also tolerates trailing flags, but leading flags are the
	// documented form and do not rely on that leniency.)
	var taskArgs []string
	if f.Directory != "" {
		taskArgs = append(taskArgs, "-d", f.Directory)
	}
	if f.Taskfile != "" {
		taskArgs = append(taskArgs, "-t", f.Taskfile)
	}
	taskArgs = append(taskArgs, args...)

	subCmd := exec.CommandContext(cmd.Context(), "task", taskArgs...)
	subCmd.Stdout = cmd.OutOrStdout()
	subCmd.Stderr = cmd.ErrOrStderr()
	return subCmd.Run()
}
