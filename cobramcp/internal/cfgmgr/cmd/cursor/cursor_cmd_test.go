package cursor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/bald/cobramcp/internal/cfgmgr/cmd/cursor"
)

func run(t *testing.T, commandName string, args ...string) error {
	t.Helper()
	root := cursor.Command(commandName, map[string]string{"DEFAULT_KEY": "default"})
	root.SetArgs(args)
	return root.Execute()
}

// TestCommandTree 覆盖 Command 构造并挂了三个子命令。
func TestCommandTree(t *testing.T) {
	root := cursor.Command("cursor", nil)
	if len(root.Commands()) != 3 {
		t.Fatalf("期望 3 个子命令，实得 %d", len(root.Commands()))
	}
}

// TestEnableHappy 覆盖 enable 成功路径：日志级别、默认 env、自定义 --env、显式 --server-name。
func TestEnableHappy(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	if err := run(t, "cursor", "enable",
		"--config-path", cfg,
		"--log-level", "info",
		"--server-name", "svc",
		"--env", "K=V",
	); err != nil {
		t.Fatalf("enable 成功路径失败: %v", err)
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Fatal("enable 未写入配置文件")
	}
}

// TestEnableDefaultServerName 覆盖 serverName=="" 时派生默认名的分支。
func TestEnableDefaultServerName(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	if err := run(t, "cursor", "enable", "--config-path", cfg); err != nil {
		t.Fatalf("enable（默认服务名）失败: %v", err)
	}
}

// TestEnableGetCmdPathError 覆盖 GetCmdPath 找不到 commandName 的错误分支。
func TestEnableGetCmdPathError(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	if err := run(t, "NOT_IN_PATH", "enable", "--config-path", cfg); err == nil {
		t.Fatal("期望 GetCmdPath 返回错误")
	}
}

// TestEnableManagerError 覆盖 NewCursorManager 解析非法 JSON 失败的错误分支。
func TestEnableManagerError(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(cfg, []byte("{ not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "cursor", "enable", "--config-path", cfg); err == nil {
		t.Fatal("期望 NewCursorManager 返回错误")
	}
}

// TestDisable 覆盖 disable 成功（显式名 + 默认名）与 NewCursorManager 错误。
func TestDisable(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	if err := run(t, "cursor", "enable", "--config-path", cfg, "--server-name", "svc"); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "cursor", "disable", "--config-path", cfg, "--server-name", "svc"); err != nil {
		t.Fatalf("disable 成功路径失败: %v", err)
	}
	if err := run(t, "cursor", "disable", "--config-path", cfg); err != nil {
		t.Fatalf("disable 默认名失败: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(bad, []byte("{ not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "cursor", "disable", "--config-path", bad); err == nil {
		t.Fatal("期望 disable 命中 NewCursorManager 错误")
	}
}

// TestList 覆盖 list 成功与 NewCursorManager 错误。
func TestList(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	if err := run(t, "cursor", "enable", "--config-path", cfg, "--server-name", "svc"); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "cursor", "list", "--config-path", cfg); err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(bad, []byte("{ not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "cursor", "list", "--config-path", bad); err == nil {
		t.Fatal("期望 list 命中 NewCursorManager 错误")
	}
}
