//go:build windows

package codegen

import (
	"os/exec"
	"syscall"
	"testing"
)

// createNewProcessGroup 让被测进程自立控制台进程组（Windows 控制台事件的
// 投递单位是进程组；不隔离会把事件广播给 go test 自己）。
const createNewProcessGroup = 0x00000200

// withE2ESysProcAttr 为计时脚本的被测进程设置 Windows 进程组属性。
func withE2ESysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// shutdownE2EProcess 触发优雅停机：向被测进程组投递 CTRL_BREAK 控制台事件
// ——Go 运行时把它转成 os.Interrupt，命中 App.Run 的 signal.Notify(SIGINT,
// SIGTERM) 优雅停机分支（POSIX SIGTERM 的 Windows 等价实现）。
func shutdownE2EProcess(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")
	r1, _, callErr := proc.Call(1 /* CTRL_BREAK_EVENT */, uintptr(cmd.Process.Pid))
	if r1 == 0 {
		return callErr
	}
	return nil
}
