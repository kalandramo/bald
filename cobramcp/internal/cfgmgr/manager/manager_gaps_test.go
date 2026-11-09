package manager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/bald/cobramcp/internal/cfgmgr/manager/cursor"
	"github.com/kalandramo/bald/cobramcp/internal/cfgmgr/manager/vscode"
	"github.com/stretchr/testify/require"
)

// 空 configPath 回退到平台默认 workspace 路径（.vscode/mcp.json）。
func TestNewVSCodeManager_EmptyPathFallback(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m, err := NewVSCodeManager("", true)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, ".vscode", "mcp.json"), m.configPath)
}

// NewCursorManager：显式路径 + 空路径回退（.cursor/mcp.json），并走一遍启停。
func TestNewCursorManager(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor.json")

	m, err := NewCursorManager(path, false)
	require.NoError(t, err)
	require.Equal(t, path, m.configPath)

	server := cursor.Server{Type: "stdio", Command: "/bin/app", Args: []string{"mcp"}}
	require.NoError(t, m.EnableServer("c1", server))
	require.True(t, m.config.HasServer("c1"))
	require.NoError(t, m.DisableServer("c1"))
	require.False(t, m.config.HasServer("c1"))

	t.Chdir(dir)
	m2, err := NewCursorManager("", true)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, ".cursor", "mcp.json"), m2.configPath)
}

// DisableServer 目标不存在 → 警告并返回 nil。
func TestDisableServer_NotExist(t *testing.T) {
	m, err := NewVSCodeManager(filepath.Join(t.TempDir(), "cfg.json"), false)
	require.NoError(t, err)
	require.NoError(t, m.DisableServer("ghost"))
}

// Print 委托到底层 config。
func TestManagerPrint(t *testing.T) {
	m, err := NewVSCodeManager(filepath.Join(t.TempDir(), "cfg.json"), false)
	require.NoError(t, err)
	m.Print()
}

// loadConfig：configPath 是目录 → ReadFile 失败（EISDIR）。
func TestLoadConfig_ReadError(t *testing.T) {
	dir := t.TempDir()
	_, err := NewVSCodeManager(dir, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to read configuration file")
}

// loadConfig：非法 JSON → Unmarshal 失败。
func TestLoadConfig_UnmarshalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))
	_, err := NewVSCodeManager(path, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to parse configuration file")
}

// saveConfig：父目录是常规文件 → MkdirAll 失败，错误经 EnableServer 透传。
func TestSaveConfig_MkdirError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	m := &Manager[vscode.Server, *vscode.Config]{
		config:     &vscode.Config{},
		configPath: filepath.Join(blocker, "cfg.json"),
	}
	err := m.EnableServer("s", vscode.Server{Type: "stdio", Command: "/bin/app"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to create directory")
}

// saveConfig：backup 目标路径被目录占位 → Create 失败，错误经 backupConfig 透传。
func TestSaveConfig_BackupError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o644))
	// backup 目标 cfg.backup.json 占位为目录 → os.Create EISDIR。
	require.NoError(t, os.Mkdir(filepath.Join(dir, "cfg.backup.json"), 0o755))

	m, err := NewVSCodeManager(path, false)
	require.NoError(t, err)
	err = m.EnableServer("s", vscode.Server{Type: "stdio", Command: "/bin/app"})
	require.Error(t, err)
}

// saveConfig：config 文件只读 → backup 可读成功，WriteFile 写失败（EACCES）。
// root 绕过权限位，该分支不可复现时如实跳过。
func TestSaveConfig_WriteError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 绕过文件权限位，WriteFile 失败分支不可复现")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o644))
	require.NoError(t, os.Chmod(path, 0o444))
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	m, err := NewVSCodeManager(path, false)
	require.NoError(t, err)
	err = m.EnableServer("s", vscode.Server{Type: "stdio", Command: "/bin/app"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to write configuration file")
}

// backupConfig：configPath 是自指符号链接 → Stat/Open 均 ELOOP（非 IsNotExist），
// Open 错误分支被触发。
func TestBackupConfig_OpenError(t *testing.T) {
	dir := t.TempDir()
	loop := filepath.Join(dir, "loop.json")
	require.NoError(t, os.Symlink("loop.json", loop))

	m := &Manager[vscode.Server, *vscode.Config]{config: &vscode.Config{}, configPath: loop}
	require.Error(t, m.backupConfig())
}

// 结构性零覆盖（登记备查）：
//   manager.go:127-128 —— json.MarshalIndent(m.config) 错误分支：config 仅
//     map[string]Server（字符串/切片/映射字段），Marshal 恒成功（manifest.go:59 同判）。
