package codegen

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestCheckErr_Reexec 以子进程真测 CheckErr 的非 nil 半边：打印 stderr 并
// os.Exit(1)。主测试进程内触发会杀死整个测试二进制，故 re-exec 自身（env 标记
// 切到被测分支），断言退出码与 stderr 文案。
func TestCheckErr_Reexec(t *testing.T) {
	if os.Getenv("BALD_REEXEC_CHECKERR") == "1" {
		CheckErr(errors.New("boom"))
		return // 不可达：CheckErr 已 os.Exit
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckErr_Reexec$")
	cmd.Env = append(os.Environ(), "BALD_REEXEC_CHECKERR=1")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("CheckErr(non-nil) must exit 1, got err=%v out=%s", err, out)
	}
	if !strings.Contains(string(out), "error: boom") {
		t.Errorf("stderr must carry %q, got %s", "error: boom", out)
	}
}
