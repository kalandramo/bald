//go:build !windows

package codegen

import (
	"os/exec"
	"syscall"
	"testing"
)

// withE2ESysProcAttr POSIX 无需进程组隔离（SIGTERM 按进程号直达）。
func withE2ESysProcAttr(_ *exec.Cmd) {}

// shutdownE2EProcess 触发优雅停机：SIGTERM（判据 ④ 口径的原生信号）。
func shutdownE2EProcess(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()
	return cmd.Process.Signal(syscall.SIGTERM)
}
