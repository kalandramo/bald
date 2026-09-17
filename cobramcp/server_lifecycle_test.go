package cobramcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"

	"github.com/stretchr/testify/require"
)

// newDemoFactory 每次调用都返回一棵全新的命令树，符合 MCPServer 的 cmdFactory 约定。
func newDemoFactory() func() *cobra.Command {
	return func() *cobra.Command {
		root := &cobra.Command{Use: "demo", Short: "demo root"}

		root.AddCommand(&cobra.Command{
			Use:   "greet",
			Short: "print a greeting",
			RunE: func(cmd *cobra.Command, _ []string) error {
				cmd.Println("hello from cobramcp")
				return nil
			},
		})

		return root
	}
}

// TestMCPServer_ServesCobraToolsOverTransport 覆盖 L2 融合后的完整链路：
// Cobra 命令树 → 工具注册到 bald/transport/mcp → SSE 服务端 → MCP 客户端可见并可调用。
func TestMCPServer_ServesCobraToolsOverTransport(t *testing.T) {
	srv, err := NewMCPServer(MCPOptions{
		Enabled: true,
		Addr:    "127.0.0.1:0",
		Name:    "demo",
		Version: "1.0.0",
	}, newDemoFactory())
	require.NoError(t, err)

	require.Len(t, srv.Tools(), 1)
	toolName := srv.Tools()[0].Name
	require.NotEmpty(t, toolName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startErr := make(chan error, 1)
	go func() { startErr <- srv.Start(ctx) }()

	var endpoint string
	require.Eventually(t, func() bool {
		endpoint = srv.Endpoint()
		return endpoint != ""
	}, 5*time.Second, 10*time.Millisecond, "Start 应同步绑定并解析出真实端口")
	require.NotContains(t, endpoint, ":0")

	sseTransport, err := transport.NewSSE(endpoint + "/sse")
	require.NoError(t, err)

	mcpClient := client.NewClient(sseTransport)
	t.Cleanup(func() { _ = mcpClient.Close() })

	require.NoError(t, mcpClient.Start(ctx))
	_, err = mcpClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "cobramcp-lifecycle-test",
				Version: "1.0.0",
			},
		},
	})
	require.NoError(t, err)

	// Cobra 命令必须以 MCP 工具的形式出现在服务端。
	tools, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
	require.Equal(t, toolName, tools.Tools[0].Name)

	// 进程内执行：命令的 stdout 必须回传给客户端。
	result, err := mcpClient.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: toolName},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "expected TextContent")
	require.Contains(t, text.Text, "hello from cobramcp")

	// ctx 取消后 Start 必须返回（不再自行捕获进程信号，停机交给宿主）。
	cancel()
	select {
	case err := <-startErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start 未在 ctx 取消后返回")
	}

	require.NoError(t, srv.Stop(context.Background()))
}

// TestMCPServer_DisabledStartIsNoop 验证 Enabled=false 时不绑定端口、不阻塞。
func TestMCPServer_DisabledStartIsNoop(t *testing.T) {
	srv, err := NewMCPServer(MCPOptions{Enabled: false}, newDemoFactory())
	require.NoError(t, err)

	require.NoError(t, srv.Start(context.Background()))
	require.Equal(t, "", srv.Endpoint())
}

// TestMCPServer_StartRequiresAddr 验证启用但未配置地址时快速失败。
func TestMCPServer_StartRequiresAddr(t *testing.T) {
	srv, err := NewMCPServer(MCPOptions{Enabled: true}, newDemoFactory())
	require.NoError(t, err)

	err = srv.Start(context.Background())
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "Addr"))
}
