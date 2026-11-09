package vscode_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/bald/cobramcp/internal/cfgmgr/cmd/vscode"
)

// commandName 传 "vscode" 时，enable 子命令的 CommandPath 为 "vscode enable"，
// GetCmdPath 能在其中定位 "vscode"，故走成功路径；传不在路径里的串则触发其错误分支。
func run(t *testing.T, commandName string, args ...string) error {
	t.Helper()
	root := vscode.Command(commandName, map[string]string{"DEFAULT_KEY": "default"})
	root.SetArgs(args)
	return root.Execute()
}

// TestCommandTree 覆盖 Command 构造并挂了三个子命令。
func TestCommandTree(t *testing.T) {
	root := vscode.Command("vscode", nil)
	var names []string
	for _, c := range root.Commands() {
		names = append(names, c.Name())
	}
	if len(names) != 3 {
		t.Fatalf("期望 3 个子命令，实得 %v", names)
	}
}

// TestEnableHappy 覆盖 enable 成功路径：日志级别、默认 env、自定义 --env、显式 --server-name。
func TestEnableHappy(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	if err := run(t, "vscode", "enable",
		"--config-path", cfg,
		"--log-level", "debug",
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
	if err := run(t, "vscode", "enable", "--config-path", cfg); err != nil {
		t.Fatalf("enable（默认服务名）失败: %v", err)
	}
}

// TestEnableWorkspaceFlag 覆盖 --workspace 分支（仍传显式 config-path 保证隔离）。
func TestEnableWorkspaceFlag(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	if err := run(t, "vscode", "enable", "--config-path", cfg, "--workspace"); err != nil {
		t.Fatalf("enable --workspace 失败: %v", err)
	}
}

// TestEnableGetCmdPathError 覆盖 GetCmdPath 找不到 commandName 的错误分支。
func TestEnableGetCmdPathError(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	if err := run(t, "NOT_IN_PATH", "enable", "--config-path", cfg); err == nil {
		t.Fatal("期望 GetCmdPath 返回错误")
	}
}

// TestEnableManagerError 覆盖 NewVSCodeManager 解析非法 JSON 失败的错误分支。
func TestEnableManagerError(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(cfg, []byte("{ not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "vscode", "enable", "--config-path", cfg); err == nil {
		t.Fatal("期望 NewVSCodeManager 返回错误")
	}
}

// TestDisable 覆盖 disable 成功（显式名 + 默认名两分支）与 NewVSCodeManager 错误。
func TestDisable(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	// 先 enable 落盘，再 disable 命中已有服务
	if err := run(t, "vscode", "enable", "--config-path", cfg, "--server-name", "svc"); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "vscode", "disable", "--config-path", cfg, "--server-name", "svc"); err != nil {
		t.Fatalf("disable 成功路径失败: %v", err)
	}
	// 默认服务名分支
	if err := run(t, "vscode", "disable", "--config-path", cfg, "--workspace"); err != nil {
		t.Fatalf("disable 默认名/工作区失败: %v", err)
	}
	// NewVSCodeManager 错误
	bad := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(bad, []byte("{ not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "vscode", "disable", "--config-path", bad); err == nil {
		t.Fatal("期望 disable 命中 NewVSCodeManager 错误")
	}
}

// TestList 覆盖 list 成功与 NewVSCodeManager 错误。
func TestList(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	if err := run(t, "vscode", "enable", "--config-path", cfg, "--server-name", "svc"); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "vscode", "list", "--config-path", cfg); err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(bad, []byte("{ not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "vscode", "list", "--config-path", bad); err == nil {
		t.Fatal("期望 list 命中 NewVSCodeManager 错误")
	}
}
