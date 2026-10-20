package cobramcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	clienttransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// structuredJSON 把结果的 structuredContent 序列化成 JSON 字符串，方便用
// JSONEq 断言整份结构化载荷（含字段缺省语义）。
func structuredJSON(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, result.StructuredContent, "every tool result must carry structuredContent")
	raw, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	return string(raw)
}

// textOf 取出结果文本轨道的唯一一段 TextContent。
func textOf(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "expected TextContent, got %T", result.Content[0])

	return text.Text
}

// TestToolOutputToResult_TextTrackUnchanged 锁死文本轨道的逐字节行为：
// 双轨之后，文本仍然只是 stdout 与 stderr 以单个换行拼接，非零退出码仍然
// 走 NewToolResultError，空输出且失败时仍然是 "exit code N"。
func TestToolOutputToResult_TextTrackUnchanged(t *testing.T) {
	cases := []struct {
		name     string
		output   ToolOutput
		wantText string
		wantErr  bool
	}{
		{"empty success", ToolOutput{}, "", false},
		{"stdout only", ToolOutput{StdOut: "out"}, "out", false},
		{"stderr only", ToolOutput{StdErr: "err"}, "err", false},
		{"both", ToolOutput{StdOut: "out", StdErr: "err"}, "out\nerr", false},
		{"failure both", ToolOutput{StdOut: "out", StdErr: "err", ExitCode: 2}, "out\nerr", true},
		{"failure stdout only", ToolOutput{StdOut: "out", ExitCode: 2}, "out", true},
		{"failure silent", ToolOutput{ExitCode: 3}, "exit code 3", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := toolOutputToResult(tc.output, toolMeta{})

			require.Equal(t, tc.wantErr, result.IsError)
			require.Equal(t, tc.wantText, textOf(t, result))
		})
	}
}

// TestToolOutputToResult_StructuredTrack 断言结构化轨道始终携带
// stdout/stderr/exitCode，成功与失败路径都如此（isError 与 structuredContent
// 在 mcp-go v1.1.0 里可并存）。
func TestToolOutputToResult_StructuredTrack(t *testing.T) {
	cases := []struct {
		name   string
		output ToolOutput
		want   string
	}{
		{"empty success", ToolOutput{}, `{"exitCode":0}`},
		{"both", ToolOutput{StdOut: "out", StdErr: "err"}, `{"stdout":"out","stderr":"err","exitCode":0}`},
		{"failure both", ToolOutput{StdOut: "out", StdErr: "err", ExitCode: 2}, `{"stdout":"out","stderr":"err","exitCode":2}`},
		{"failure silent", ToolOutput{ExitCode: 3}, `{"exitCode":3}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.JSONEq(t, tc.want, structuredJSON(t, toolOutputToResult(tc.output, toolMeta{})))
		})
	}
}

// TestToolOutputToResult_JSONData 覆盖 JSON 契约命令的解析通道：对象、数组、
// 嵌套值原样进入 data；数值用 json.Number 保留精度。
func TestToolOutputToResult_JSONData(t *testing.T) {
	meta := toolMeta{outputJSON: true}

	t.Run("object", func(t *testing.T) {
		result := toolOutputToResult(ToolOutput{StdOut: `{"code":0,"pods":3}`}, meta)

		require.False(t, result.IsError)
		require.JSONEq(t,
			`{"stdout":"{\"code\":0,\"pods\":3}","exitCode":0,"data":{"code":0,"pods":3}}`,
			structuredJSON(t, result))
	})

	t.Run("array", func(t *testing.T) {
		result := toolOutputToResult(ToolOutput{StdOut: `[{"name":"a"},{"name":"b"}]`}, meta)

		require.JSONEq(t,
			`{"stdout":"[{\"name\":\"a\"},{\"name\":\"b\"}]","exitCode":0,"data":[{"name":"a"},{"name":"b"}]}`,
			structuredJSON(t, result))
	})

	t.Run("trailing newline is tolerated", func(t *testing.T) {
		result := toolOutputToResult(ToolOutput{StdOut: "{\"ok\":true}\n"}, meta)

		require.JSONEq(t,
			`{"stdout":"{\"ok\":true}\n","exitCode":0,"data":{"ok":true}}`,
			structuredJSON(t, result))
	})

	t.Run("big integer keeps precision", func(t *testing.T) {
		const payload = `{"id":9007199254740993,"nested":{"tags":["x"],"on":true}}`
		result := toolOutputToResult(ToolOutput{StdOut: payload}, meta)

		structured, ok := result.StructuredContent.(toolStructuredContent)
		require.True(t, ok)
		data, ok := structured.Data.(map[string]any)
		require.True(t, ok)
		require.Equal(t, json.Number("9007199254740993"), data["id"])
		require.Contains(t, structuredJSON(t, result), "9007199254740993")
	})

	t.Run("failure path still parses", func(t *testing.T) {
		result := toolOutputToResult(ToolOutput{StdOut: `{"error":"boom"}`, ExitCode: 1}, meta)

		require.True(t, result.IsError)
		require.Equal(t, `{"error":"boom"}`, textOf(t, result))
		require.JSONEq(t,
			`{"stdout":"{\"error\":\"boom\"}","exitCode":1,"data":{"error":"boom"}}`,
			structuredJSON(t, result))
	})
}

// TestToolOutputToResult_JSONWarnings 覆盖「契约没兑现」的每一类情况：
// 文本轨道原样保留、结构化侧只给 warning 不给 data，且绝不把工具调用标成错误。
func TestToolOutputToResult_JSONWarnings(t *testing.T) {
	meta := toolMeta{outputJSON: true}

	cases := []struct {
		name        string
		stdout      string
		wantText    string
		wantWarning string
	}{
		{"invalid json", "{oops", "{oops", "not valid JSON"},
		{"trailing data", `{"a":1} junk`, `{"a":1} junk`, "trailing data"},
		{"scalar string", `"just text"`, `"just text"`, "not an object or array"},
		{"scalar number", "42", "42", "not an object or array"},
		{"scalar null", "null", "null", "not an object or array"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := toolOutputToResult(ToolOutput{StdOut: tc.stdout}, meta)

			require.False(t, result.IsError, "parse failure must never fabricate a tool error")
			require.Equal(t, tc.wantText, textOf(t, result))

			structured, ok := result.StructuredContent.(toolStructuredContent)
			require.True(t, ok)
			require.Nil(t, structured.Data, "data must be absent when the contract is not honoured")
			require.Contains(t, structured.Warning, tc.wantWarning)
			require.Contains(t, structured.Warning, warningPrefix)
		})
	}

	t.Run("empty stdout yields neither data nor warning", func(t *testing.T) {
		result := toolOutputToResult(ToolOutput{StdOut: "  \n"}, meta)

		structured, ok := result.StructuredContent.(toolStructuredContent)
		require.True(t, ok)
		require.Nil(t, structured.Data)
		require.Empty(t, structured.Warning)
	})

	t.Run("oversize stdout is not parsed and not truncated", func(t *testing.T) {
		payload := `{"pad":"` + strings.Repeat("x", maxJSONDataBytes) + `"}`

		result := toolOutputToResult(ToolOutput{StdOut: payload}, meta)

		require.False(t, result.IsError)
		require.Equal(t, payload, textOf(t, result), "text track must keep the full output")

		structured, ok := result.StructuredContent.(toolStructuredContent)
		require.True(t, ok)
		require.Nil(t, structured.Data)
		require.Contains(t, structured.Warning, "parse limit")
	})
}

// TestOutputJSONHint 覆盖注解键的读取语义：ParseBool 的全部写法都认，
// 非法值与缺省都当作「没有契约」，且该键不会泄漏进 mcp.ToolAnnotation。
func TestOutputJSONHint(t *testing.T) {
	cases := []struct {
		value string
		set   bool
		want  bool
	}{
		{"true", true, true},
		{"1", true, true},
		{"TRUE", true, true},
		{"false", true, false},
		{"0", true, false},
		{"nonsense", true, false},
		{"", true, false},
		{"", false, false},
	}

	for _, tc := range cases {
		cmd := &cobra.Command{Use: "list"}
		if tc.set {
			cmd.Annotations = map[string]string{AnnotationOutputJSON: tc.value}
		}

		require.Equal(t, tc.want, outputJSONHint(cmd), "value=%q set=%v", tc.value, tc.set)
	}

	t.Run("hint alone creates no mcp annotation", func(t *testing.T) {
		cmd := &cobra.Command{
			Use:         "list",
			Annotations: map[string]string{AnnotationOutputJSON: "true"},
		}

		require.Nil(t, toolAnnotations(cmd))

		_, meta, err := Selector{}.createToolFromCmd(cmd, "")
		require.NoError(t, err)
		require.True(t, meta.outputJSON)
	})

	t.Run("hint coexists with mcp annotations", func(t *testing.T) {
		cmd := &cobra.Command{
			Use: "list",
			Annotations: map[string]string{
				AnnotationOutputJSON: "true",
				AnnotationTitle:      "title",
				AnnotationReadOnly:   "true",
			},
		}

		tool, meta, err := Selector{}.createToolFromCmd(cmd, "")
		require.NoError(t, err)
		require.True(t, meta.outputJSON)
		require.Equal(t, "title", tool.Annotations.Title)
		require.NotNil(t, tool.Annotations.ReadOnlyHint)
		require.True(t, *tool.Annotations.ReadOnlyHint)
	})
}

// errBoom 是 boom 工具的失败原因。
var errBoom = errBoomType{}

type errBoomType struct{}

func (errBoomType) Error() string { return "kaboom from cobramcp" }

// newStructuredFactory 提供三个工具：json（JSON 契约成功）、plain（纯文本）、
// boom（JSON 契约 + 失败），用于验证结构化内容真的穿过了 transport 层。
//
// 根命令打开 SilenceUsage/SilenceErrors：真实世界里命令一旦出错，cobra 会把
// usage 追到 stdout，JSON 契约就被尾随文本破坏（这时结构化侧只会给 warning）。
// 要让失败路径也保持机器可读，命令本身就得静音 usage，本用例按这个正确姿势配置。
func newStructuredFactory() func() *cobra.Command {
	return func() *cobra.Command {
		root := &cobra.Command{Use: "demo", SilenceUsage: true, SilenceErrors: true}

		root.AddCommand(&cobra.Command{
			Use:         "json",
			Short:       "print a JSON document",
			Annotations: map[string]string{AnnotationOutputJSON: "true"},
			RunE: func(cmd *cobra.Command, _ []string) error {
				cmd.Println(`{"pods":[{"name":"a","restarts":2}]}`)

				return nil
			},
		})

		root.AddCommand(&cobra.Command{
			Use:   "plain",
			Short: "print plain text",
			RunE: func(cmd *cobra.Command, _ []string) error {
				cmd.Println("plain text output")

				return nil
			},
		})

		root.AddCommand(&cobra.Command{
			Use:         "boom",
			Short:       "fail with a JSON body",
			Annotations: map[string]string{AnnotationOutputJSON: "true"},
			RunE: func(cmd *cobra.Command, _ []string) error {
				cmd.Println(`{"error":"kaboom"}`)

				return errBoom
			},
		})

		return root
	}
}

// TestMCPServer_StructuredContentOverTransport 是端到端证据：结构化轨道
// （含 data / warning 语义与 exitCode）能穿过 bald/transport/mcp 到达 MCP
// 客户端，而文本轨道保持原样。
func TestMCPServer_StructuredContentOverTransport(t *testing.T) {
	srv, err := NewMCPServer(MCPOptions{
		Enabled: true,
		Addr:    "127.0.0.1:0",
		Name:    "demo",
		Version: "1.0.0",
	}, newStructuredFactory())
	require.NoError(t, err)

	require.Len(t, srv.Tools(), 3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startErr := make(chan error, 1)
	go func() { startErr <- srv.Start(ctx) }()

	var endpoint string
	require.Eventually(t, func() bool {
		endpoint = srv.Endpoint()

		return endpoint != ""
	}, 5*time.Second, 10*time.Millisecond, "Start should bind and expose the resolved address")
	require.NotContains(t, endpoint, ":0")

	sseTransport, err := clienttransport.NewSSE(endpoint + "/sse")
	require.NoError(t, err)

	mcpClient := client.NewClient(sseTransport)
	t.Cleanup(func() { _ = mcpClient.Close() })

	require.NoError(t, mcpClient.Start(ctx))

	_, err = mcpClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "cobramcp-structured-test", Version: "1.0.0"},
		},
	})
	require.NoError(t, err)

	call := func(name string) *mcp.CallToolResult {
		t.Helper()

		result, callErr := mcpClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: name},
		})
		require.NoError(t, callErr)

		return result
	}

	t.Run("json contract", func(t *testing.T) {
		result := call("json")

		require.False(t, result.IsError)
		require.Equal(t, "{\"pods\":[{\"name\":\"a\",\"restarts\":2}]}\n", textOf(t, result))
		require.NotEmpty(t, result.RawStructuredContent)

		var structured struct {
			StdOut   string `json:"stdout"`
			StdErr   string `json:"stderr"`
			ExitCode int    `json:"exitCode"`
			Data     struct {
				Pods []struct {
					Name     string `json:"name"`
					Restarts int    `json:"restarts"`
				} `json:"pods"`
			} `json:"data"`
			Warning string `json:"warning"`
		}
		require.NoError(t, json.Unmarshal(result.RawStructuredContent, &structured))

		require.Equal(t, textOf(t, result), structured.StdOut)
		require.Empty(t, structured.StdErr)
		require.Equal(t, 0, structured.ExitCode)
		require.Empty(t, structured.Warning)
		require.Len(t, structured.Data.Pods, 1)
		require.Equal(t, "a", structured.Data.Pods[0].Name)
		require.Equal(t, 2, structured.Data.Pods[0].Restarts)
	})

	t.Run("plain command carries no data", func(t *testing.T) {
		result := call("plain")

		require.False(t, result.IsError)
		require.Equal(t, "plain text output\n", textOf(t, result))

		var structured map[string]any
		require.NoError(t, json.Unmarshal(result.RawStructuredContent, &structured))
		require.NotContains(t, structured, "data")
		require.NotContains(t, structured, "warning")
		require.Equal(t, float64(0), structured["exitCode"])
	})

	t.Run("failing json command is both isError and structured", func(t *testing.T) {
		result := call("boom")

		require.True(t, result.IsError)
		require.Contains(t, textOf(t, result), "kaboom")

		var structured struct {
			ExitCode int            `json:"exitCode"`
			Data     map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(result.RawStructuredContent, &structured))
		require.Equal(t, 1, structured.ExitCode)
		require.Equal(t, "kaboom", structured.Data["error"])
	})

	cancel()
	select {
	case err := <-startErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after ctx cancellation")
	}

	require.NoError(t, srv.Stop(context.Background()))
}
