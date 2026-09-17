package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stretchr/testify/require"
)

const echoToolJSON = `{
  "name": "echo",
  "description": "Echoes the input string",
  "inputSchema": {
    "type": "object",
    "properties": {
      "message": {"type": "string", "description": "The message to echo"}
    },
    "required": ["message"]
  }
}`

// TestServer_LifecycleStreamableHTTP 覆盖 Start 同步绑定、Endpoint 真实端口、
// 工具注册与 Stop 优雅停机。全部在进程内完成，不依赖信号，可纳入常规回归。
func TestServer_LifecycleStreamableHTTP(t *testing.T) {
	srv := NewServer(
		WithServerName("lifecycle-demo"),
		WithServerVersion("1.0.0"),
		WithMCPServeType(ServerTypeHTTP),
		WithMCPServeAddress("127.0.0.1:0"),
	)

	require.NoError(t, srv.RegisterHandlerWithJsonString(echoToolJSON,
		func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			msg, err := request.RequireString("message")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(msg), nil
		}))

	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, srv.Stop(context.Background())) })

	endpoint := srv.Endpoint()
	require.Contains(t, endpoint, "127.0.0.1:")
	require.NotContains(t, endpoint, ":0", "动态端口必须解析为真实端口")

	ctx := context.Background()
	httpTransport, err := transport.NewStreamableHTTP(endpoint + "/mcp")
	require.NoError(t, err)

	mcpClient := client.NewClient(httpTransport)
	t.Cleanup(func() { _ = mcpClient.Close() })

	require.NoError(t, mcpClient.Start(ctx))
	_, err = mcpClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "lifecycle-test",
				Version: "1.0.0",
			},
		},
	})
	require.NoError(t, err)

	result, err := mcpClient.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "echo",
			Arguments: map[string]any{"message": "pong"},
		},
	})
	require.NoError(t, err)
	require.Len(t, result.Content, 1)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "expected TextContent")
	require.Equal(t, "pong", text.Text)
}

// TestServer_StartFailsOnBusyPort 验证端口占用时 Start 同步返回错误，
// 而不是"启动成功但实际没在听"。
func TestServer_StartFailsOnBusyPort(t *testing.T) {
	first := NewServer(
		WithMCPServeType(ServerTypeHTTP),
		WithMCPServeAddress("127.0.0.1:0"),
	)
	require.NoError(t, first.Start(context.Background()))
	t.Cleanup(func() { _ = first.Stop(context.Background()) })

	addr := strings.TrimPrefix(first.Endpoint(), "http://")

	second := NewServer(
		WithMCPServeType(ServerTypeHTTP),
		WithMCPServeAddress(addr),
	)
	require.Error(t, second.Start(context.Background()))
	require.False(t, second.started.Load(), "绑定失败不得标记为已启动")
}

// TestServer_EndpointEmptyBeforeStart 验证 Start 之前不伪造 endpoint，
// 上层可据此区分"未监听"与"已监听"。
func TestServer_EndpointEmptyBeforeStart(t *testing.T) {
	stdio := NewServer(WithMCPServeType(ServerTypeStdio))
	require.Equal(t, "", stdio.Endpoint())

	inProcess := NewServer(WithMCPServeType(ServerTypeInProcess))
	require.Equal(t, "", inProcess.Endpoint())

	sse := NewServer(WithMCPServeType(ServerTypeSSE), WithMCPServeAddress("127.0.0.1:8080"))
	require.Equal(t, "", sse.Endpoint())
}
