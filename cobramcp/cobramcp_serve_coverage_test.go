package cobramcp

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// dialOK 返回一个探测 addr 是否已可建立 TCP 连接的 Eventually 条件函数。
func dialOK(addr string) func() bool {
	return func() bool {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
}

// newServeRoot 构造一棵可注册出至少一个工具的命令树：
// root 无 Run（被 basic filter 摘掉，不成为工具），leaf 有 RunE（成为工具）。
func newServeRoot() *cobra.Command {
	root := &cobra.Command{Use: "srvapp"}
	root.AddCommand(&cobra.Command{
		Use:  "hello",
		RunE: func(c *cobra.Command, _ []string) error { c.Println("hi"); return nil },
	})
	return root
}

// newCollisionRoot 构造一棵注册必然失败（位置参数与 flag 同名）的命令树，
// 用于触发 serve* 里 newTransportServer/registerTools 的错误上抛分支。
func newCollisionRoot() *cobra.Command {
	root := &cobra.Command{Use: "srvapp"}
	root.AddCommand(newCollisionCmd())
	return root
}

// reserveFreeAddr 抢占一个空闲 TCP 端口后立即释放，返回其地址。
// 仅测试内串行使用，重绑竞态可忽略。
func reserveFreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// ---------------------------------------------------------------------------
// executeTool（config.go:242）：命中 selector / 回退默认 selector 两条分支
// ---------------------------------------------------------------------------

func TestExecuteTool_Branches(t *testing.T) {
	ctx := context.Background()

	// 命中 selector 且带 Middleware：短路，不进子进程。
	c := &Config{toolSelectors: map[string]Selector{
		"mw_tool": {Middleware: func(context.Context, mcp.CallToolRequest, ToolInput, ExecuteFunc) (*mcp.CallToolResult, ToolOutput, error) {
			return nil, ToolOutput{StdOut: "mw-hit"}, nil
		}},
	}}
	req := mcp.CallToolRequest{}
	req.Params.Name = "mw_tool"
	_, out, err := c.executeTool(ctx, req, ToolInput{})
	require.NoError(t, err)
	require.Equal(t, "mw-hit", out.StdOut)

	// 未命中 selector：回退 Selector{} → package execute → execSubprocess。
	if runtime.GOOS == "windows" {
		t.Skip("依赖 /bin/sh，windows 跳过")
	}
	overrideExecutable(t, "/bin/sh")
	c2 := &Config{toolSelectors: map[string]Selector{}}
	req2 := mcp.CallToolRequest{}
	req2.Params.Name = "missing"
	input := ToolInput{CmdPath: []string{"-c", "echo exec-default"}}
	_, out2, err := c2.executeTool(ctx, req2, input)
	require.NoError(t, err)
	require.Equal(t, "exec-default\n", out2.StdOut)
}

// ---------------------------------------------------------------------------
// restToolHandler（rest.go:136）：走真实 handler 的全部分支
// 注：既有 rest_test.go 用重复的 buildRESTMux 闭包替代了真实 handler，
// 这里直接驱动真实的 restToolHandler，补上这条被漏掉的产品代码路径。
// ---------------------------------------------------------------------------

func TestRestToolHandler_Real(t *testing.T) {
	// 非 POST → 405
	h := restToolHandler("t", toolMeta{flagNames: map[string]struct{}{}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/t", nil))
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)

	if runtime.GOOS == "windows" {
		t.Skip("依赖 /bin/sh，windows 跳过")
	}
	overrideExecutable(t, "/bin/sh")
	meta := toolMeta{
		cmdPath:   []string{"-c", "echo rest-ok"},
		flagNames: map[string]struct{}{"x": {}},
		argSpecs:  []ArgSpec{{Name: "a", Index: 0}}, // 驱动 argNames 还原循环
	}
	h2 := restToolHandler("t", meta)

	// 合法 JSON POST → 200，stdout 透传
	w2 := httptest.NewRecorder()
	h2.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/t", strings.NewReader(`{"x":1,"a":5}`)))
	require.Equal(t, http.StatusOK, w2.Code)
	var out ToolOutput
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &out))
	require.Contains(t, out.StdOut, "rest-ok")

	// 空 body → Decode 返回 EOF → 视作零参数 → 200
	w3 := httptest.NewRecorder()
	h2.ServeHTTP(w3, httptest.NewRequest(http.MethodPost, "/t", strings.NewReader("")))
	require.Equal(t, http.StatusOK, w3.Code)

	// 非法 JSON → 400
	w4 := httptest.NewRecorder()
	h2.ServeHTTP(w4, httptest.NewRequest(http.MethodPost, "/t", strings.NewReader("{bad")))
	require.Equal(t, http.StatusBadRequest, w4.Code)

	// 子进程无法启动 → 500
	overrideExecutable(t, "/nonexistent-binary-xyz-123")
	w5 := httptest.NewRecorder()
	h2.ServeHTTP(w5, httptest.NewRequest(http.MethodPost, "/t", strings.NewReader("{}")))
	require.Equal(t, http.StatusInternalServerError, w5.Code)
}

// ---------------------------------------------------------------------------
// serveStdio（config.go:125）：stdio 传输，ctx 取消即返回
// ---------------------------------------------------------------------------

func TestServeStdio_CancelReturns(t *testing.T) {
	// 用一根写端保持打开、内容恒空的管道顶替 os.Stdin，令 ServeStdio 阻塞在读上，
	// 从而 select 只能被 ctx.Done 释放（避免 stdin EOF 抢先返回错误）。
	//
	// 只在 serveStdio 调用前一次性写 os.Stdin（协程创建建立 happens-before），
	// 测试结束不还原、不关管道：还原/关闭会与 ServeStdio 协程对 os.Stdin 的读取
	// 构成数据竞争（-race 复现）。测试进程随即退出，这条被阻塞的协程与两个 fd
	// 的泄漏可接受。
	readEnd, writeEnd, err := os.Pipe()
	require.NoError(t, err)
	os.Stdin = readEnd
	runtime.KeepAlive(writeEnd)

	cmd := newServeRoot()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cmd.SetContext(ctx)
	defer cancel()

	c := &Config{}
	done := make(chan error, 1)
	go func() { done <- c.serveStdio(cmd) }()

	// 给 stdio 服务协程一点启动窗口，随后取消触发停机。
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serveStdio 未在 ctx 取消后返回")
	}
}

func TestServeStdio_RegisterError(t *testing.T) {
	cmd := newCollisionRoot()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetContext(context.Background())

	c := &Config{}
	require.Error(t, c.serveStdio(cmd))
}

// ---------------------------------------------------------------------------
// serveHTTP（config.go:148）：SSE 传输，ctx 取消即返回
// ---------------------------------------------------------------------------

func TestServeHTTP_CancelReturns(t *testing.T) {
	cmd := newServeRoot()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cmd.SetContext(ctx)
	defer cancel()

	c := &Config{}
	done := make(chan error, 1)
	// ":0" 让 Start 同步绑定到随机端口；随后阻塞在 <-ctx.Done。
	go func() { done <- c.serveHTTP(cmd, "127.0.0.1:0") }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serveHTTP 未在 ctx 取消后返回")
	}
}

func TestServeHTTP_RegisterError(t *testing.T) {
	cmd := newCollisionRoot()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetContext(context.Background())

	c := &Config{}
	require.Error(t, c.serveHTTP(cmd, "127.0.0.1:0"))
}

// ---------------------------------------------------------------------------
// serveREST（rest.go:56）：真实 HTTP 服务 + ctx 取消优雅停机 + 各错误分支
// ---------------------------------------------------------------------------

func TestServeREST_Lifecycle(t *testing.T) {
	addr := reserveFreeAddr(t)

	cmd := newServeRoot()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cmd.SetContext(ctx)
	defer cancel()

	c := &Config{}
	if runtime.GOOS != "windows" {
		// 子进程模型：把自身二进制换成 /bin/sh，工具执行不会重跑测试自身。
		overrideExecutable(t, "/bin/sh")
	}

	done := make(chan error, 1)
	go func() { done <- c.serveREST(cmd, addr, "") }()

	// 等到服务真正在听
	require.Eventually(t, func() bool {
		conn, derr := net.Dial("tcp", addr)
		if derr != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 5*time.Second, 20*time.Millisecond)

	base := "http://" + addr

	// 已知工具 → 命中 restToolHandler（经真实 mux），进程可启动即 200。
	resp, err := http.Post(base+"/srvapp_hello", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// 未知路径 → 命中 catch-all → 404
	resp2, err := http.Post(base+"/nope", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp2.StatusCode)
	_ = resp2.Body.Close()

	// 取消触发 Shutdown 协程 → ListenAndServe 返回 ErrServerClosed → 函数返回 nil。
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serveREST 未在 ctx 取消后返回")
	}
}

func TestServeREST_AuthAndOAuthBranches(t *testing.T) {
	addr := reserveFreeAddr(t)

	cmd := newServeRoot()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cmd.SetContext(ctx)
	defer cancel()

	// 设置 AuthToken + OAuth 资源元数据：覆盖 mux 的 OAuth 挂载分支
	// 以及 buildAuthMiddleware 非 nil 的包裹分支。此处只启停，不发请求
	// （带鉴权时匿名请求会 401，但那条路径已由其它测试覆盖）。
	c := &Config{
		AuthToken:              "secret-token",
		OAuthProtectedResource: &mcpserver.ProtectedResourceMetadataConfig{Resource: "https://api.example.com"},
	}

	done := make(chan error, 1)
	go func() { done <- c.serveREST(cmd, addr, "https://api.example.com") }()

	require.Eventually(t, func() bool {
		conn, derr := net.Dial("tcp", addr)
		if derr != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 5*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serveREST 未在 ctx 取消后返回")
	}
}

func TestServeREST_RegisterError(t *testing.T) {
	cmd := newCollisionRoot()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetContext(context.Background())

	c := &Config{}
	require.Error(t, c.serveREST(cmd, reserveFreeAddr(t), ""))
}

func TestServeREST_ListenError(t *testing.T) {
	// 非法端口 → ListenAndServe 立刻返回非 ErrServerClosed 错误 → 走 wrapped 分支。
	cmd := newServeRoot()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cmd.SetContext(ctx)
	defer cancel()

	c := &Config{}
	require.Error(t, c.serveREST(cmd, "127.0.0.1:99999999", ""))
}

// ---------------------------------------------------------------------------
// serveHTTP 配置分支 + 父命令上溯：一次带全量配置的 SSE 启停
// ---------------------------------------------------------------------------

func TestServeHTTP_FullConfigBranches(t *testing.T) {
	// 传子命令（有父节点）以命中 newTransportServer/registerTools 的 rootCmd 上溯循环体。
	root := newServeRoot()
	child := root.Commands()[0]
	child.SetOut(io.Discard)
	child.SetErr(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	child.SetContext(ctx)
	defer cancel()

	// BaseURL(SSE) + ServerDescription + AuthToken(中间件) + OAuth(SSE) 四个可选分支同时开启。
	c := &Config{
		BaseURL:                "http://base.example.com",
		ServerDescription:      "a described server",
		AuthToken:              "tok",
		OAuthProtectedResource: &mcpserver.ProtectedResourceMetadataConfig{Resource: "https://res.example.com"},
	}

	done := make(chan error, 1)
	go func() { done <- c.serveHTTP(child, "127.0.0.1:0") }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serveHTTP 未在 ctx 取消后返回")
	}
}

func TestServeHTTP_StartBindError(t *testing.T) {
	// 预先占住一个端口并持有，令 serveHTTP 的 SSE Start 同步绑定失败 → 走返回错误分支。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	cmd := newServeRoot()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetContext(context.Background())

	c := &Config{}
	// 绑定冲突使 Start 立即返回错误，serveHTTP 直接上抛（不阻塞）。
	require.Error(t, c.serveHTTP(cmd, ln.Addr().String()))
}

// ---------------------------------------------------------------------------
// newTransportServer 工具处理闭包（config.go:216-221）：经真实 SSE 客户端调用
// 子进程传输模型把闭包注册进服务端；只有 MCP 客户端 CallTool 才会执行闭包体。
// ---------------------------------------------------------------------------

func TestServeHTTP_ToolCallOverSSE(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("依赖 /bin/sh，windows 跳过")
	}
	addr := reserveFreeAddr(t)

	root := newServeRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	root.SetContext(ctx)
	defer cancel()

	c := &Config{}
	// 成功路径：executablePath 换成 /bin/sh，命令可启动（即便退出码非零）→ 闭包 err==nil → 返回结果。
	overrideExecutable(t, "/bin/sh")

	done := make(chan error, 1)
	go func() { done <- c.serveHTTP(root, addr) }()
	require.Eventually(t, dialOK(addr), 5*time.Second, 20*time.Millisecond)

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()

	sse, err := transport.NewSSE("http://" + addr + "/sse")
	require.NoError(t, err)
	cli := client.NewClient(sse)
	defer func() { _ = cli.Close() }()
	require.NoError(t, cli.Start(cctx))

	_, err = cli.Initialize(cctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "cobra-serve-test", Version: "1.0.0"},
		},
	})
	require.NoError(t, err)

	tools, err := cli.ListTools(cctx, mcp.ListToolsRequest{})
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
	name := tools.Tools[0].Name

	res, err := cli.CallTool(cctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name}})
	require.NoError(t, err) // 闭包成功返回（无 Go error）
	require.NotNil(t, res)

	// 错误路径：换成不可启动的二进制 → execSubprocess 返回 Go error → 闭包 err!=nil 分支。
	overrideExecutable(t, "/nonexistent-binary-xyz-999")
	res2, err := cli.CallTool(cctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name}})
	require.NoError(t, err) // 仍以工具结果形式返回（IsError），不是协议层 Go error
	require.True(t, res2.IsError)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serveHTTP 未在 ctx 取消后返回")
	}
}

// ---------------------------------------------------------------------------
// registerTools / registerToolsRecursive 配置分支
// ---------------------------------------------------------------------------

func TestRegisterTools_ToolNamePrefix(t *testing.T) {
	c := &Config{ToolNamePrefix: "pfx"}
	require.NoError(t, c.registerTools(newServeRoot()))
	require.Equal(t, "pfx", c.toolNamePrefix)
	require.NotEmpty(t, c.tools)
	require.True(t, strings.HasPrefix(c.tools[0].Name, "pfx_"))
}

func TestRegisterTools_CmdSelectorNonMatch(t *testing.T) {
	// 唯一 selector 的 CmdSelector 恒 false → registerToolsRecursive 命中 continue，工具集为空。
	c := &Config{Selectors: []Selector{{CmdSelector: func(*cobra.Command) bool { return false }}}}
	require.NoError(t, c.registerTools(newServeRoot()))
	require.Empty(t, c.tools)
}

// ---------------------------------------------------------------------------
// decodeToolInput / splitFlatInput / execute 输入还原分支
// ---------------------------------------------------------------------------

func TestDecodeToolInput_Branches(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"flag": "v", "arg": 1}
	meta := toolMeta{
		cmdPath:   []string{"srvapp", "get"},
		flagNames: map[string]struct{}{"flag": {}},
		argSpecs:  []ArgSpec{{Name: "arg", Index: 0}},
	}
	in := decodeToolInput(req, meta)
	require.Equal(t, []string{"srvapp", "get"}, in.CmdPath)
	require.Equal(t, map[string]any{"flag": "v", "arg": 1}, in.FlatInput)
	require.Equal(t, []string{"arg"}, in.ArgNames)
}

func TestSplitFlatInput_ArgTypes(t *testing.T) {
	input := ToolInput{
		FlagNames: map[string]struct{}{"f": {}},
		ArgNames:  []string{"a1", "a2", "a3"},
		FlatInput: map[string]any{
			"f":  "flagval",
			"a1": []any{"x", "y"},    // []any 逐元素
			"a2": []string{"p", "q"}, // []string 直接并入
			"a3": 42,                 // 标量 fmt
		},
	}
	flagMap, posArgs := splitFlatInput(input)
	require.Equal(t, map[string]any{"f": "flagval"}, flagMap)
	require.Equal(t, []string{"x", "y", "p", "q", "42"}, posArgs)
}

func TestExecute_LaunchError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("依赖不存在路径触发 Go error，windows 跳过")
	}
	overrideExecutable(t, "/nonexistent-binary-xyz-999")
	req := mcp.CallToolRequest{}
	req.Params.Name = "t"
	var s Selector
	_, out, err := s.execute(context.Background(), req, ToolInput{CmdPath: []string{"a"}})
	require.Error(t, err)
	require.Equal(t, ToolOutput{}, out)
}

// ---------------------------------------------------------------------------
// toolManifest：nil 工具项跳过
// ---------------------------------------------------------------------------

func TestToolManifest_SkipsNilTool(t *testing.T) {
	keep := mcp.NewTool("keep")
	data, err := toolManifest([]*mcp.Tool{nil, &keep})
	require.NoError(t, err)
	require.Contains(t, string(data), "keep")
	require.NotContains(t, string(data), "null")
}
