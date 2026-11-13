package cobramcp

// cobramcp_gaps_test.go —— 批6 行级覆盖：命令 RunE 体、MCPServer 构造/注册/
// 处理闭包分支、schema/参数/认证/结构化输出的剩余零块。
//
// 命令 RunE 的注册错误路径统一用「挂到碰撞树（newCollisionRoot）下执行」触发：
// registerTools 上溯到根后必然命中位置参数与 flag 同名错误，不启服务、不读 stdin。

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	baldlog "github.com/kalandramo/bald/log"
)

// keepLogger 保存并还原全局 logger——RunE 会 installStderrLogger 改写全局。
func keepLogger(t *testing.T) {
	t.Helper()
	old := baldlog.GetLogger()
	t.Cleanup(func() { baldlog.SetLogger(old) })
}

// runSubOnCollisionRoot 把子命令挂到碰撞树下执行，注册期必然报错。
// tools 命令在 registerTools 之前就 OpenFile 创建 mcp-tools.json，故切到临时
// 目录隔离，避免在包目录留下空文件。
func runSubOnCollisionRoot(t *testing.T, sub *cobra.Command) error {
	t.Helper()
	t.Chdir(t.TempDir())
	keepLogger(t)
	root := newCollisionRoot()
	root.AddCommand(sub)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{sub.Name()})
	return root.Execute()
}

// ---------------------------------------------------------------------------
// tools.go：tools 命令 RunE
// ---------------------------------------------------------------------------

func TestToolsCommand_ExportSuccess(t *testing.T) {
	keepLogger(t)
	dir := t.TempDir()
	t.Chdir(dir)

	cmd := toolCommand(nil) // config == nil → 回退 &Config{}
	cmd.SetOut(io.Discard)
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(dir, "mcp-tools.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"name"`)
}

func TestToolsCommand_OpenFileError(t *testing.T) {
	keepLogger(t)
	dir := t.TempDir()
	t.Chdir(dir)
	// 同名目录占位 → O_WRONLY 打开失败（EISDIR），确定性触达创建错误分支。
	require.NoError(t, os.Mkdir(filepath.Join(dir, "mcp-tools.json"), 0o755))

	cmd := toolCommand(&Config{})
	cmd.SetOut(io.Discard)
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to create or open")
}

func TestToolsCommand_RegisterError(t *testing.T) {
	err := runSubOnCollisionRoot(t, toolCommand(&Config{}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "collides")
}

// ---------------------------------------------------------------------------
// start.go / stream.go / restcmd.go：RunE 经注册错误路径全覆盖
// ---------------------------------------------------------------------------

func TestStartCommand_RegisterError(t *testing.T) {
	err := runSubOnCollisionRoot(t, startCommand(nil))
	require.Error(t, err)
	require.Contains(t, err.Error(), "collides")
}

func TestStreamCommand_RegisterError(t *testing.T) {
	err := runSubOnCollisionRoot(t, streamCommand(nil))
	require.Error(t, err)
	require.Contains(t, err.Error(), "collides")
}

func TestRestCommand_RegisterError(t *testing.T) {
	err := runSubOnCollisionRoot(t, restCommand(nil))
	require.Error(t, err)
	require.Contains(t, err.Error(), "collides")
}

// ---------------------------------------------------------------------------
// server.go：NewMCPServer 构造校验与可选分支
// ---------------------------------------------------------------------------

func TestNewMCPServer_FactoryValidation(t *testing.T) {
	_, err := NewMCPServer(MCPOptions{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not be nil")

	_, err = NewMCPServer(MCPOptions{}, func() *cobra.Command { return nil })
	require.Error(t, err)
	require.Contains(t, err.Error(), "returned nil")
}

func TestNewMCPServer_FullOptions(t *testing.T) {
	// OAuth publicPaths / AuthToken 中间件 / OAuth 元数据三个可选分支。
	srv, err := NewMCPServer(MCPOptions{
		AuthToken:              "tok",
		OAuthProtectedResource: &mcpserver.ProtectedResourceMetadataConfig{Resource: "https://res.example.com"},
	}, newDemoFactory())
	require.NoError(t, err)
	require.Len(t, srv.Tools(), 1)
}

func TestNewMCPServer_RegistrationError(t *testing.T) {
	// 碰撞在子命令：递归错误上抛（registerToolsRecursive 循环体 + createToolFromCmd）。
	_, err := NewMCPServer(MCPOptions{}, newCollisionRoot)
	require.Error(t, err)
	require.Contains(t, err.Error(), "collides")
}

func TestNewMCPServer_SelectorNonMatchContinues(t *testing.T) {
	srv, err := NewMCPServer(MCPOptions{}, newDemoFactory(),
		WithSelectors(Selector{CmdSelector: func(*cobra.Command) bool { return false }}, Selector{}))
	require.NoError(t, err)
	require.Len(t, srv.Tools(), 1)
}

func TestNewMCPServer_HiddenDeprecatedFiltered(t *testing.T) {
	factory := func() *cobra.Command {
		root := &cobra.Command{Use: "r"}
		root.AddCommand(&cobra.Command{Use: "hid", Hidden: true, RunE: func(*cobra.Command, []string) error { return nil }})
		root.AddCommand(&cobra.Command{Use: "dep", Deprecated: "use ok", RunE: func(*cobra.Command, []string) error { return nil }})
		root.AddCommand(&cobra.Command{Use: "ok", RunE: func(*cobra.Command, []string) error { return nil }})
		return root
	}
	srv, err := NewMCPServer(MCPOptions{}, factory)
	require.NoError(t, err)
	require.Len(t, srv.Tools(), 1)
	require.Equal(t, "ok", srv.Tools()[0].Name)
}

func TestMCPServer_StartBindError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	srv, err := NewMCPServer(MCPOptions{Enabled: true, Addr: ln.Addr().String()}, newDemoFactory())
	require.NoError(t, err)
	require.Error(t, srv.Start(context.Background()))
}

// ---------------------------------------------------------------------------
// server.go：makeInProcessHandler 的 middleware 与 panic 恢复（直驱 handler）
// ---------------------------------------------------------------------------

func TestMakeInProcessHandler_MiddlewareCallsNext(t *testing.T) {
	srv, err := NewMCPServer(MCPOptions{}, newDemoFactory())
	require.NoError(t, err)

	var executed bool
	sel := Selector{Middleware: func(ctx context.Context, req mcp.CallToolRequest, input ToolInput, next ExecuteFunc) (*mcp.CallToolResult, ToolOutput, error) {
		executed = true
		return next(ctx, req, input)
	}}
	h := srv.makeInProcessHandler(sel, nil)
	req := mcp.CallToolRequest{}
	req.Params.Name = "greet"
	_, out, err := h(context.Background(), req, ToolInput{})
	require.NoError(t, err)
	require.True(t, executed)
	require.Contains(t, out.StdOut, "hello from cobramcp")
}

func TestMakeInProcessHandler_PanicRecovered(t *testing.T) {
	factory := func() *cobra.Command {
		root := &cobra.Command{Use: "p"}
		root.AddCommand(&cobra.Command{Use: "boom", RunE: func(*cobra.Command, []string) error {
			panic("kaboom")
		}})
		return root
	}
	srv, err := NewMCPServer(MCPOptions{}, factory)
	require.NoError(t, err)

	h := srv.makeInProcessHandler(Selector{}, nil)
	req := mcp.CallToolRequest{}
	req.Params.Name = "boom"
	_, _, err = h(context.Background(), req, ToolInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "panic during tool execution")
	require.Contains(t, err.Error(), "kaboom")
}

// ---------------------------------------------------------------------------
// server.go：注册闭包的 err/result 分支——经真实 Streamable HTTP 客户端调用 middleware
// ---------------------------------------------------------------------------

func TestMCPServer_ToolHandlerMiddlewareBranches(t *testing.T) {
	var calls atomic.Int32
	mw := func(context.Context, mcp.CallToolRequest, ToolInput, ExecuteFunc) (*mcp.CallToolResult, ToolOutput, error) {
		if calls.Add(1) == 1 {
			return nil, ToolOutput{}, errors.New("mw-error")
		}
		return mcp.NewToolResultText("mw-result"), ToolOutput{}, nil
	}
	srv, err := NewMCPServer(MCPOptions{Enabled: true, Addr: "127.0.0.1:0"}, newDemoFactory(),
		WithSelectors(Selector{Middleware: mw}))
	require.NoError(t, err)
	toolName := srv.Tools()[0].Name

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- srv.Start(ctx) }()

	var endpoint string
	require.Eventually(t, func() bool {
		endpoint = srv.Endpoint()
		return endpoint != ""
	}, 5*time.Second, 10*time.Millisecond)

	streamable, err := transport.NewStreamableHTTP(endpoint + "/mcp")
	require.NoError(t, err)
	cli := client.NewClient(streamable)
	t.Cleanup(func() { _ = cli.Close() })
	require.NoError(t, cli.Start(ctx))
	_, err = cli.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "gaps-test", Version: "1.0.0"},
		},
	})
	require.NoError(t, err)

	// 第一次：middleware 返回 Go error → 闭包 err!=nil → NewToolResultError。
	res1, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: toolName}})
	require.NoError(t, err)
	require.True(t, res1.IsError)

	// 第二次：middleware 返回非 nil result → 闭包直接透传 result。
	res2, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: toolName}})
	require.NoError(t, err)
	require.False(t, res2.IsError)
	require.Contains(t, res2.Content[0].(mcp.TextContent).Text, "mw-result")

	cancel()
	select {
	case err := <-startErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start 未在 ctx 取消后返回")
	}
	require.NoError(t, srv.Stop(context.Background()))
}

// ---------------------------------------------------------------------------
// selector.go：继承 flag 的去重/过滤分支 + execute panic 恢复
// ---------------------------------------------------------------------------

func TestBuildFlatSchema_InheritedFlagBranches(t *testing.T) {
	root := &cobra.Command{Use: "r"}
	root.PersistentFlags().String("shadow", "", "parent persistent")
	root.PersistentFlags().String("hid", "", "hidden persistent")
	require.NoError(t, root.PersistentFlags().MarkHidden("hid"))
	child := &cobra.Command{Use: "c", RunE: func(*cobra.Command, []string) error { return nil }}
	child.Flags().String("shadow", "", "child local shadows parent")
	root.AddCommand(child)

	schema, meta, err := Selector{}.buildFlatSchema(child)
	require.NoError(t, err)
	// 本地 shadow 遮蔽父级持久同名 flag（cobra InheritedFlags 不再含它）；hid 被过滤。
	require.Contains(t, schema.Properties, "shadow")
	require.NotContains(t, schema.Properties, "hid")
	require.Contains(t, meta.flagNames, "shadow")
	require.NotContains(t, meta.flagNames, "hid")
}

func TestSelectorExecute_PanicRecovered(t *testing.T) {
	s := Selector{Middleware: func(context.Context, mcp.CallToolRequest, ToolInput, ExecuteFunc) (*mcp.CallToolResult, ToolOutput, error) {
		panic("selector-boom")
	}}
	_, _, err := s.execute(context.Background(), mcp.CallToolRequest{}, ToolInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "panic: selector-boom")
}

// ---------------------------------------------------------------------------
// args.go / auth.go / structured.go：剩余零块
// ---------------------------------------------------------------------------

func TestParseArgSpecs_NoArgTokens(t *testing.T) {
	// Use 含空格但无括号 token → matches 为空 → nil。
	require.Nil(t, parseArgSpecs(&cobra.Command{Use: "cmd plainword"}))
}

func TestNormaliseName_SpecialChars(t *testing.T) {
	require.Equal(t, "a_b_c", normaliseName("a-b.c"))
}

func TestCollectPositionalArgs_VariadicStringSliceAndScalar(t *testing.T) {
	specs := []ArgSpec{{Name: "x", Variadic: true}}
	// []string 直接并入。
	require.Equal(t, []string{"a", "b"}, collectPositionalArgs(specs, map[string]any{"x": []string{"a", "b"}}))
	// 标量兜底为单元素。
	require.Equal(t, []string{"single"}, collectPositionalArgs(specs, map[string]any{"x": "single"}))
}

func TestBuildAuthMiddleware_UserOnly(t *testing.T) {
	var called bool
	userMW := func(next http.Handler) http.Handler {
		called = true
		return next
	}
	mw := buildAuthMiddleware(userMW, "")
	require.NotNil(t, mw)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.True(t, called)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestBearerToken_BareToken(t *testing.T) {
	require.Equal(t, "rawtok", bearerToken("rawtok"))
}

func TestParseJSONStdout_ScalarTopLevel(t *testing.T) {
	// null 与 boolean 顶层都不是对象/数组 → warning 带 jsonKind 的类型名。
	data, warning := parseJSONStdout("null", toolMeta{})
	require.Nil(t, data)
	require.Contains(t, warning, "null")

	data, warning = parseJSONStdout("true", toolMeta{})
	require.Nil(t, data)
	require.Contains(t, warning, "boolean")
}

func TestJsonKind_DefaultBranch(t *testing.T) {
	// JSON 解码不会产出 int（数值是 json.Number），default 分支直测。
	require.Equal(t, "int", jsonKind(42))
}

// ---------------------------------------------------------------------------
// 结构性零覆盖（登记备查）：
//   - tools.go:36-37：file.Close 警告分支——成功打开的本地常规文件 Close 恒成功，
//     错误只在 NFS 等延迟写回场景非确定性出现，离线不可触达。
//   - tools.go:47-48：encoder.Encode 错误分支——config.tools 由 registerTools 即时
//     从 cobra 树派生，RawInputSchema 是 json.Marshal 产物（恒合法 JSON），编码恒成功。
//   - selector.go:212-213：json.Marshal(schema) 回退分支——schema 仅由字符串/布尔/
//     嵌套 *Schema 构成，any 型字段（Enum/Examples/Const）只经 UnmarshalJSON 注入
//     （值必可再编码），Marshal 恒成功。
//   - selector.go:138-140：继承 flag 去重分支——cobra v1.10.2 InheritedFlags 以
//     local.Lookup 排除与本地同名的 flag，进入 VisitAll 的名字必不在 schema.Properties，
//     防御性去重恒不触发。
//   - server.go:211-212：registerManifestResource 错误分支——toolManifest 编码恒成功
//     （同 manifest.go:59 登记），RegisterResource 仅在底层 mcpServer 为 nil 时报错，
//     而 NewServer 构造期恒非 nil。
//   - server.go:333-334：RegisterHandler 错误分支——同上，mcpServer 构造期恒非 nil
//     （与 config.go:223-225 同一判据）。
