package cobramcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	clienttransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	mcptransport "github.com/kalandramo/bald/transport/mcp"
)

// manifestTree 构造一棵覆盖"flag + 位置参数 + 注解"的命令树，供清单测试使用。
// use 是根命令名；子进程模型用它做工具名前缀，进程内模型不用它——需要两个
// 模型的工具名完全一致时传空串。
func manifestTree(use string) *cobra.Command {
	root := &cobra.Command{Use: use, Short: "demo root"}
	root.Version = "1.0.0"

	list := &cobra.Command{
		Use:   "list",
		Short: "list things",
		Long:  "List things in a namespace.",
		Annotations: map[string]string{
			AnnotationTitle:      "List things",
			AnnotationReadOnly:   "true",
			AnnotationIdempotent: "true",
		},
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	list.Flags().String("namespace", "default", "namespace to list")
	list.Flags().Bool("wide", false, "show extra columns")

	get := &cobra.Command{
		Use:   "get <name>",
		Short: "get one thing",
		Annotations: map[string]string{
			AnnotationArgPrefix + "0": "the thing name",
			AnnotationOutputJSON:      "true",
		},
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	get.Flags().Int("timeout", 30, "timeout in seconds")

	root.AddCommand(list, get)

	return root
}

// manifestEntry 是清单数组的元素形状：与 tools/list 的元素同名同义。
type manifestEntry struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Annotations mcp.ToolAnnotation
}

// decodeManifest 按数组顺序解码清单（顺序即断言对象，不能用 map 丢掉）。
func decodeManifest(t *testing.T, data []byte) []manifestEntry {
	t.Helper()

	var raws []json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raws))

	entries := make([]manifestEntry, 0, len(raws))

	for _, raw := range raws {
		entries = append(entries, decodeEntry(t, raw))
	}

	return entries
}

// decodeEntry 从一段工具 JSON（清单元素或 tools/list 元素的编码）解码出四个字段。
func decodeEntry(t *testing.T, raw []byte) manifestEntry {
	t.Helper()

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))

	var entry manifestEntry

	// mcp.Tool.MarshalJSON 会省略空 description，缺键按零值处理，而不是当成非法 JSON。
	if field, ok := fields["name"]; ok {
		require.NoError(t, json.Unmarshal(field, &entry.Name))
	}

	if field, ok := fields["description"]; ok {
		require.NoError(t, json.Unmarshal(field, &entry.Description))
	}

	if field, ok := fields["annotations"]; ok {
		require.NoError(t, json.Unmarshal(field, &entry.Annotations))
	}

	entry.InputSchema = fields["inputSchema"]
	require.NotEmpty(t, entry.InputSchema, "清单元素必须带 inputSchema")

	return entry
}

// schemaFields 把 inputSchema 解成 map，并抹平 mcp-go 客户端往返带来的等价差异。
//
// 服务端发出的 inputSchema 就是注册期的原始字节；客户端把它解成结构化
// ToolInputSchema 再编回来时，mcp-go（mcp/tools.go 的
// toolArgumentsSchemaMarshalJSON）会给空 required 补一个 []。JSON Schema 语义上
// "required 缺省"与"required: []"等价，比较前统一去掉，避免把 mcp-go 的往返
// 规范化误判成清单与 tools/list 不同源。
func schemaFields(t *testing.T, raw []byte) map[string]any {
	t.Helper()

	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))

	if required, ok := fields["required"].([]any); ok && len(required) == 0 {
		delete(fields, "required")
	}

	return fields
}

// P2-1：清单必须字节级确定，且与输入顺序无关（按 name 升序）。
func TestToolManifest_DeterministicAndSorted(t *testing.T) {
	schema := json.RawMessage("{\"type\":\"object\"}")
	tools := []*mcp.Tool{
		{Name: "zulu", Description: "z", RawInputSchema: schema},
		{Name: "alpha", Description: "a", RawInputSchema: schema},
		{Name: "mike", Description: "m", RawInputSchema: schema},
	}

	first, err := toolManifest(tools)
	require.NoError(t, err)

	for i := 0; i < 50; i++ {
		again, err := toolManifest(tools)
		require.NoError(t, err)
		require.Equal(t, string(first), string(again), "第 %d 次编码与首次不一致——清单不是确定性的", i)
	}

	// 输入顺序无关：反转输入仍得同一串字节。
	reversed, err := toolManifest([]*mcp.Tool{tools[2], tools[1], tools[0]})
	require.NoError(t, err)
	require.Equal(t, string(first), string(reversed), "清单内容不应依赖工具注册顺序")

	entries := decodeManifest(t, first)
	require.Len(t, entries, 3)
	require.Equal(t, "alpha", entries[0].Name)
	require.Equal(t, "mike", entries[1].Name)
	require.Equal(t, "zulu", entries[2].Name)
}

// P2-2：清单条目与工具定义（tools/list 的同一编码）逐字段同源，没有第二份构造。
func TestToolManifest_MatchesToolDefinitions(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.registerTools(manifestTree("demo")))
	require.NotEmpty(t, c.tools)

	data, err := toolManifest(c.tools)
	require.NoError(t, err)

	entries := decodeManifest(t, data)
	require.Len(t, entries, len(c.tools))

	got := make(map[string]manifestEntry, len(entries))
	for _, entry := range entries {
		got[entry.Name] = entry
	}

	for _, tool := range c.tools {
		entry, ok := got[tool.Name]
		require.True(t, ok, "工具 %q 未出现在清单里", tool.Name)

		// tools/list 对每个工具用的就是 mcp.Tool.MarshalJSON，这里以它为准逐字段比对。
		raw, err := json.Marshal(*tool)
		require.NoError(t, err)

		want := decodeEntry(t, raw)
		require.Equal(t, want.Name, entry.Name)
		require.Equal(t, want.Description, entry.Description)
		require.Equal(t, schemaFields(t, want.InputSchema), schemaFields(t, entry.InputSchema))
		require.Equal(t, want.Annotations, entry.Annotations)
	}

	// 注解确实被带进清单（否则上面的比对可能因两边都为空而"通过"）。
	listEntry, ok := got["demo_list"]
	require.True(t, ok)
	require.NotNil(t, listEntry.Annotations.ReadOnlyHint)
	require.True(t, *listEntry.Annotations.ReadOnlyHint, "readOnlyHint 注解必须出现在清单里")
	require.Equal(t, "List things", listEntry.Annotations.Title)
}

// P2-3：清单 Resource 的注册参数与读取语义；未知 URI fail-closed。
func TestRegisterManifestResource_Contract(t *testing.T) {
	var (
		gotResource mcp.Resource
		gotHandler  mcpserver.ResourceHandlerFunc
	)

	register := func(resource mcp.Resource, handler mcpserver.ResourceHandlerFunc) error {
		gotResource, gotHandler = resource, handler

		return nil
	}

	tools := []*mcp.Tool{{Name: "list", RawInputSchema: json.RawMessage("{\"type\":\"object\"}")}}
	require.NoError(t, registerManifestResource(register, tools))

	require.Equal(t, ManifestResourceURI, gotResource.URI)
	require.Equal(t, ManifestMIMEType, gotResource.MIMEType)
	require.NotEmpty(t, gotResource.Name)
	require.NotEmpty(t, gotResource.Description)
	require.NotNil(t, gotResource.Size)

	ctx := context.Background()

	_, err := gotHandler(ctx, mcp.ReadResourceRequest{Params: mcp.ReadResourceParams{URI: "tools://other"}})
	require.Error(t, err, "未注册的 URI 必须 fail-closed")

	contents, err := gotHandler(ctx, mcp.ReadResourceRequest{Params: mcp.ReadResourceParams{URI: ManifestResourceURI}})
	require.NoError(t, err)
	require.Len(t, contents, 1)

	text, ok := contents[0].(mcp.TextResourceContents)
	require.True(t, ok)
	require.Equal(t, ManifestResourceURI, text.URI)
	require.Equal(t, ManifestMIMEType, text.MIMEType)
	require.Equal(t, int64(len(text.Text)), *gotResource.Size, "Resource.Size 必须与真实内容字节数一致")

	entries := decodeManifest(t, []byte(text.Text))
	require.Len(t, entries, 1)
	require.Equal(t, "list", entries[0].Name)
}

// startSSEClient 用模型自己的启动入口起服务，并连上一个已初始化的客户端。
func startSSEClient(t *testing.T, start func(context.Context) error, endpoint func() string) (*client.Client, context.Context) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	startErr := make(chan error, 1)
	go func() { startErr <- start(ctx) }()

	var addr string
	require.Eventually(t, func() bool {
		addr = endpoint()

		return addr != ""
	}, 5*time.Second, 10*time.Millisecond, "Start 应同步绑定端口")
	require.NotContains(t, addr, ":0")

	sseTransport, err := clienttransport.NewSSE(addr + "/sse")
	require.NoError(t, err)

	mcpClient := client.NewClient(sseTransport)
	t.Cleanup(func() { _ = mcpClient.Close() })

	require.NoError(t, mcpClient.Start(ctx))

	_, err = mcpClient.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "cobramcp-manifest-test", Version: "1.0.0"},
	}})
	require.NoError(t, err)

	t.Cleanup(func() {
		cancel()

		select {
		case err := <-startErr:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("Start 未在 ctx 取消后返回")
		}
	})

	return mcpClient, ctx
}

// readManifest 断言清单 Resource 的存在性与语义，返回清单原文。
//
// 这是"单一真相源"的硬证据：清单条目必须与同一次会话里 tools/list 的结果
// 逐字段一致（name/description/inputSchema/annotations），且两次读取逐字节相同。
func readManifest(t *testing.T, ctx context.Context, mcpClient *client.Client) string {
	t.Helper()

	resources, err := mcpClient.ListResources(ctx, mcp.ListResourcesRequest{})
	require.NoError(t, err)
	require.Len(t, resources.Resources, 1, "P2 只暴露一个清单 Resource（不做逐工具 Resource）")

	resource := resources.Resources[0]
	require.Equal(t, ManifestResourceURI, resource.URI)
	require.Equal(t, ManifestMIMEType, resource.MIMEType)
	require.NotEmpty(t, resource.Name)

	read := func() string {
		t.Helper()

		result, err := mcpClient.ReadResource(ctx, mcp.ReadResourceRequest{
			Params: mcp.ReadResourceParams{URI: ManifestResourceURI},
		})
		require.NoError(t, err)
		require.Len(t, result.Contents, 1)

		text, ok := result.Contents[0].(mcp.TextResourceContents)
		require.True(t, ok, "清单内容必须是文本（application/json 序列化后的字符串）")
		require.Equal(t, ManifestResourceURI, text.URI)
		require.Equal(t, ManifestMIMEType, text.MIMEType)

		return text.Text
	}

	manifest := read()
	require.Equal(t, manifest, read(), "两次读取必须逐字节一致")

	tools, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	require.NoError(t, err)

	entries := decodeManifest(t, []byte(manifest))
	require.Len(t, entries, len(tools.Tools), "清单必须覆盖全部对外暴露的工具")

	byName := make(map[string]mcp.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}

	prev := ""

	for _, entry := range entries {
		require.Greater(t, entry.Name, prev, "清单必须按 name 升序")
		prev = entry.Name

		tool, ok := byName[entry.Name]
		require.True(t, ok, "清单里的工具 %q 不在 tools/list 结果里", entry.Name)

		raw, err := json.Marshal(tool)
		require.NoError(t, err)

		want := decodeEntry(t, raw)
		require.Equal(t, want.Description, entry.Description)
		require.Equal(t, schemaFields(t, want.InputSchema), schemaFields(t, entry.InputSchema))
		require.Equal(t, want.Annotations, entry.Annotations)
	}

	return manifest
}

// P2-4（进程内模型）：NewMCPServer 路径的客户端可以一次拉全工具清单。
func TestMCPServer_ManifestResourceOverTransport(t *testing.T) {
	srv, err := NewMCPServer(MCPOptions{
		Enabled: true,
		Addr:    "127.0.0.1:0",
		Name:    "demo",
		Version: "1.0.0",
	}, func() *cobra.Command { return manifestTree("demo") })
	require.NoError(t, err)
	require.Len(t, srv.Tools(), 2)

	mcpClient, ctx := startSSEClient(t, srv.Start, srv.Endpoint)

	manifest := readManifest(t, ctx, mcpClient)

	// 进程内模型的工具名不带根命令前缀（registerToolsRecursive 的
	// toolNamePrefix := ""），清单里就是这些名字本身。
	require.Contains(t, manifest, "\"name\":\"get\"")
	require.Contains(t, manifest, "\"name\":\"list\"")
}

// P2-5（子进程模型）：Config/Command 路径行为与进程内模型一致。
func TestConfig_ManifestResourceOverTransport(t *testing.T) {
	c := &Config{}

	transportSrv, err := c.newTransportServer(manifestTree("demo"), mcptransport.ServerTypeSSE, "127.0.0.1:0")
	require.NoError(t, err)

	mcpClient, ctx := startSSEClient(t, transportSrv.Start, transportSrv.Endpoint)

	manifest := readManifest(t, ctx, mcpClient)

	// 子进程模型的工具名带根命令前缀（config.go 解析出的 toolNamePrefix）。
	require.Contains(t, manifest, "\"name\":\"demo_get\"")
	require.Contains(t, manifest, "\"name\":\"demo_list\"")
}

// P2-6：两个执行模型对同一份工具集给出逐字节相同的清单与相同的 Resource 元数据。
func TestManifestResource_IdenticalAcrossExecutionModels(t *testing.T) {
	inProcess, err := NewMCPServer(MCPOptions{
		Enabled: true,
		Addr:    "127.0.0.1:0",
		Name:    "demo",
		Version: "1.0.0",
	}, func() *cobra.Command { return manifestTree("") })
	require.NoError(t, err)

	subprocess := &Config{}

	subprocessSrv, err := subprocess.newTransportServer(manifestTree(""), mcptransport.ServerTypeSSE, "127.0.0.1:0")
	require.NoError(t, err)

	inProcessClient, inProcessCtx := startSSEClient(t, inProcess.Start, inProcess.Endpoint)
	subprocessClient, subprocessCtx := startSSEClient(t, subprocessSrv.Start, subprocessSrv.Endpoint)

	inProcessManifest := readManifest(t, inProcessCtx, inProcessClient)
	subprocessManifest := readManifest(t, subprocessCtx, subprocessClient)

	require.Equal(t, inProcessManifest, subprocessManifest,
		"子进程模型与进程内模型的清单必须逐字节相同")

	inProcessList, err := inProcessClient.ListResources(inProcessCtx, mcp.ListResourcesRequest{})
	require.NoError(t, err)

	subprocessList, err := subprocessClient.ListResources(subprocessCtx, mcp.ListResourcesRequest{})
	require.NoError(t, err)

	require.Equal(t, inProcessList.Resources[0], subprocessList.Resources[0],
		"两个模型暴露的 Resource 元数据必须一致")
}
