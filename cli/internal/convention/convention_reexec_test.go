package convention

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckCmd_FailOnFindings_Reexec 以子进程真测 checkCmd 的 os.Exit(1) 半边：
// 漂移树 + --fail-on-findings ⇒ printCheck 返回 true ⇒ 退 1。主测试进程内触发
// 会杀死测试二进制，故 re-exec 自身（env 传入漂移树绝对路径切到被测分支）。
// 默认（不带 --fail-on-findings）发现 finding 仍零退出的口径由 TestCommandTree 覆盖。
func TestCheckCmd_FailOnFindings_Reexec(t *testing.T) {
	if dir := os.Getenv("BALD_REEXEC_CHECK_FINDINGS"); dir != "" {
		root := NewCommand(Streams{In: strings.NewReader(""), Out: os.Stdout, ErrOut: os.Stderr})
		root.SetArgs([]string{"check", "--fail-on-findings", "--dir", dir})
		if err := root.Execute(); err != nil {
			os.Exit(2) // runCheck 错误（非预期）：与 finding 退 1 区分
		}
		return // 不可达：有 finding 时 RunE 已 os.Exit(1)
	}

	drifted := genAppTree(t)
	if err := os.Remove(filepath.Join(drifted, "cmd", "conv-demo", "main.go")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckCmd_FailOnFindings_Reexec$")
	cmd.Env = append(os.Environ(), "BALD_REEXEC_CHECK_FINDINGS="+drifted)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("check --fail-on-findings on drift must exit 1, got err=%v out=%s", err, out)
	}
	if !strings.Contains(string(out), "codegen-r03") {
		t.Errorf("output must report the codegen-r03 finding, got %s", out)
	}
}
