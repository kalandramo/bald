package appkit

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMustServe_Reexec 以子进程真测 MustServe 的失败半边：Serve 报错 →
// reportServeFailure 兜底写 stderr → os.Exit(1)。主测试进程内触发会杀死整个
// 测试二进制，故 re-exec 自身（env 标记切到被测分支），断言退出码与 stderr 文案。
//
// 子进程在空临时目录里跑 MustServe：无 bald-app.yaml / --config → 预装载找不到
// app.name → fail-fast（确定性，不阻塞）。默认全局后端是 nopLogger（Enabled=
// false），reportServeFailure 必走 stderr 兜底，文案可断言。
func TestMustServe_Reexec(t *testing.T) {
	if os.Getenv("BALD_REEXEC_MUSTSERVE") == "1" {
		MustServe(context.Background(), WithHTTP(new(http.ServeMux)))
		return // 不可达：Serve 失败已 os.Exit
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMustServe_Reexec$")
	cmd.Dir = t.TempDir() // 空目录：无配置源 → app.name 缺失 fail-fast
	cmd.Env = append(os.Environ(), "BALD_REEXEC_MUSTSERVE=1")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("MustServe on Serve failure must exit 1, got err=%v out=%s", err, out)
	}
	if !strings.Contains(string(out), "appkit: serve failed") {
		t.Errorf("stderr must carry %q, got %s", "appkit: serve failed", out)
	}
}
