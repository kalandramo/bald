package cursor

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// captureStdout 截获 f 的 stdout 输出，供 Print 类用例断言文案。
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	f()
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(b)
}

// workspace 路径锚定当前工作目录；用户级路径来自平台默认目录。
func TestConfigPath(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	workspace := ConfigPath(true)
	if want := filepath.Join(wd, ".cursor", "mcp.json"); workspace != want {
		t.Errorf("workspace path = %q, want %q", workspace, want)
	}

	user := ConfigPath(false)
	if !strings.HasSuffix(user, filepath.Join(".cursor", "mcp.json")) {
		t.Errorf("user path = %q, want .cursor/mcp.json suffix", user)
	}
	if user == workspace {
		t.Errorf("user path must differ from workspace path: %q", user)
	}
}

// Getwd 失败时回退相对路径（不 panic、不返回空串）。macOS 的 __getcwd 对
// 已删除目录仍返回路径，该分支只在 Linux 触发；不可达平台如实 Skip。
func TestConfigPath_WorkspaceFallback(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir, err := os.MkdirTemp("", "bald-cursor-cwd-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		os.RemoveAll(dir)
		t.Fatalf("chdir: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		_ = os.Chdir(orig)
		t.Fatalf("remove cwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	if _, err := os.Getwd(); err == nil {
		t.Skip("platform resolves a deleted cwd; Getwd fallback unreachable here")
	}
	if got := getDefaultWorkspaceConfigPath(); got != filepath.Join(".cursor", "mcp.json") {
		t.Errorf("fallback workspace path = %q", got)
	}
}

// UserHomeDir 失败时按平台回退到 USER/USERPROFILE 拼路径。
func TestConfigPath_UserFallback(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USER", "fallback-user")
	t.Setenv("USERPROFILE", "fallback-profile")

	var want string
	switch runtime.GOOS {
	case "darwin":
		want = filepath.Join("/Users", "fallback-user", ".cursor", "mcp.json")
	case "windows":
		want = filepath.Join("fallback-profile", ".cursor", "mcp.json")
	default:
		want = filepath.Join("/home", "fallback-user", ".cursor", "mcp.json")
	}
	if got := getDefaultCursorUserConfigPath(); got != want {
		t.Errorf("fallback user path = %q, want %q", got, want)
	}
}

func TestConfigPrint_Empty(t *testing.T) {
	out := captureStdout(t, func() { (&Config{}).Print() })
	if !strings.Contains(out, "No MCP servers are currently configured.") {
		t.Errorf("output = %q", out)
	}
}

func TestConfigPrint_WithServers(t *testing.T) {
	cfg := &Config{}
	cfg.AddServer("alpha", Server{
		Type:    "stdio",
		Command: "/bin/alpha",
		Args:    []string{"--flag"},
		Env:     map[string]string{"TOKEN": "x"},
		URL:     "https://alpha.example/mcp",
		Headers: map[string]string{"Authorization": "Bearer t"},
	})

	out := captureStdout(t, cfg.Print)
	for _, want := range []string{
		"Server: alpha",
		"Type: stdio",
		"Command: /bin/alpha",
		"URL: https://alpha.example/mcp",
		"Args: [--flag]",
		"Environment:",
		"TOKEN: x",
		"Headers:",
		"Authorization: Bearer t",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// 最小 Server 只打印 Type，空字段不产生多余行。
func TestServerPrint_Minimal(t *testing.T) {
	out := captureStdout(t, func() { Server{Type: "sse"}.Print() })
	if got := strings.TrimSpace(out); got != "Type: sse" {
		t.Errorf("output = %q, want only Type line", out)
	}
}
