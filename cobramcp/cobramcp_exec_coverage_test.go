package cobramcp

import (
	"context"
	"runtime"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// overrideExecutable 临时把 executablePath 指向一个安全的系统二进制，避免测试
// 里 execSubprocess 递归重跑测试自身。返回还原函数。
func overrideExecutable(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("依赖 /bin 系统二进制，windows 跳过")
	}
	old := executablePath
	executablePath = path
	t.Cleanup(func() { executablePath = old })
}

// ---------------------------------------------------------------------------
// applyAuthFlags（auth.go:104）
// ---------------------------------------------------------------------------

func TestApplyAuthFlags_Branches(t *testing.T) {
	// config nil 早返回
	applyAuthFlags(nil, "tok", "res", []string{"a"})

	// 仅 authToken
	c := &Config{}
	applyAuthFlags(c, "tok123", "", nil)
	if c.AuthToken != "tok123" {
		t.Errorf("AuthToken = %q, want tok123", c.AuthToken)
	}
	if c.OAuthProtectedResource != nil {
		t.Error("empty oauthResource should not create metadata")
	}

	// oauthResource 非空 + authServers：首次创建 metadata
	c2 := &Config{}
	applyAuthFlags(c2, "", "https://res.example.com", []string{"https://issuer"})
	if c2.OAuthProtectedResource == nil || c2.OAuthProtectedResource.Resource != "https://res.example.com" {
		t.Fatalf("metadata not set: %+v", c2.OAuthProtectedResource)
	}
	if len(c2.OAuthProtectedResource.AuthorizationServers) != 1 {
		t.Errorf("AuthorizationServers = %v, want 1 entry", c2.OAuthProtectedResource.AuthorizationServers)
	}

	// 已存在 metadata：无 authServers 时仅覆盖 Resource，保留其余字段
	applyAuthFlags(c2, "", "https://res2.example.com", nil)
	if c2.OAuthProtectedResource.Resource != "https://res2.example.com" {
		t.Errorf("resource not overwritten: %q", c2.OAuthProtectedResource.Resource)
	}
}

// ---------------------------------------------------------------------------
// execSubprocess + execute（execute.go:39 / 70）
// ---------------------------------------------------------------------------

func TestExecSubprocess_Branches(t *testing.T) {
	ctx := context.Background()
	if runtime.GOOS == "windows" {
		t.Skip("依赖 /bin/sh，windows 跳过")
	}

	// 成功：/bin/sh -c 'echo hello' → stdout 捕获、exit 0
	overrideExecutable(t, "/bin/sh")
	out, err := execSubprocess(ctx, []string{"-c", "echo hello"})
	if err != nil {
		t.Fatalf("echo subprocess: %v", err)
	}
	if out.ExitCode != 0 || out.StdOut != "hello\n" {
		t.Errorf("echo out = %+v", out)
	}

	// 非零退出：exit 3 → ExitError 映射到 ExitCode，不作为 Go error
	out, err = execSubprocess(ctx, []string{"-c", "exit 3"})
	if err != nil {
		t.Fatalf("non-zero exit should not return Go error, got %v", err)
	}
	if out.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", out.ExitCode)
	}

	// 无法启动：不存在的可执行 → 返回 Go error（非 ExitError 分支）
	overrideExecutable(t, "/nonexistent-binary-xyz-123")
	if _, err := execSubprocess(ctx, []string{"x"}); err == nil {
		t.Error("missing binary should yield a Go error")
	}
}

func TestExecute_ViaSelector(t *testing.T) {
	ctx := context.Background()
	req := mcp.CallToolRequest{}
	req.Params.Name = "demo_echo"

	// 直接分支：Selector.execute → package execute → execSubprocess
	if runtime.GOOS != "windows" {
		overrideExecutable(t, "/bin/sh")
		input := ToolInput{CmdPath: []string{"-c", "echo via-execute"}}
		var s Selector
		_, out, err := s.execute(ctx, req, input)
		if err != nil {
			t.Fatalf("Selector.execute direct: %v", err)
		}
		if out.StdOut != "via-execute\n" {
			t.Errorf("direct execute out = %q", out.StdOut)
		}
	}

	// 中间件分支：Middleware 短路，不调用 next（不触发子进程）
	input := ToolInput{CmdPath: []string{"noop"}}
	mwCalled := false
	s := Selector{Middleware: func(context.Context, mcp.CallToolRequest, ToolInput, ExecuteFunc) (*mcp.CallToolResult, ToolOutput, error) {
		mwCalled = true
		return nil, ToolOutput{StdOut: "from-middleware", ExitCode: 0}, nil
	}}
	_, out, err := s.execute(ctx, req, input)
	if err != nil {
		t.Fatalf("middleware execute: %v", err)
	}
	if !mwCalled || out.StdOut != "from-middleware" {
		t.Errorf("middleware not invoked: called=%v out=%+v", mwCalled, out)
	}
}
