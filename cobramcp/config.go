package cobramcp

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	baldlog "github.com/kalandramo/bald/log"
	mcptransport "github.com/kalandramo/bald/transport/mcp"
	"github.com/spf13/cobra"
)

// shutdownTimeout 是 CLI 子命令收到中断后等待服务优雅停机的上限。
const shutdownTimeout = 5 * time.Second

// Config customizes MCP server behavior and command-to-tool conversion.
type Config struct {
	// CommandName is the Use name for the top-level command returned by Command().
	// It is also used by GetCmdPath to locate the cobramcp command in the Cobra tree
	// and by cmdFilter to exclude cobramcp subcommands from tool exposure.
	// Default: "mcp".
	CommandName string

	// Selectors defines rules for converting commands to MCP tools.
	// Each selector specifies which commands to match and which flags to include.
	//
	// Basic safety filters are always applied first:
	//   - Hidden/deprecated commands and flags are excluded
	//   - Non-runnable commands are excluded
	//   - Built-in commands (the cobramcp command, help, completion) are excluded
	//
	// Then selectors are evaluated in order for each command:
	//   1. The first selector whose CmdSelector returns true is used
	//   2. That selector's FlagSelector determines which flags are included
	//   3. If no selectors match, the command is not exposed as a tool
	//
	// If nil or empty, defaults to exposing all commands with all flags.
	Selectors []Selector

	// DefaultEnv specifies environment variables that are automatically
	// included when `enable` writes a server config for any editor.
	// These are merged with user-provided --env values; user values
	// take precedence on conflict.
	//
	// A common use is to capture PATH so the MCP server subprocess can
	// find executables that live outside the system PATH:
	//
	//   &cobramcp.Config{
	//       DefaultEnv: map[string]string{
	//           "PATH": os.Getenv("PATH"),
	//       },
	//   }
	//
	// If nil, no default environment variables are added (current behavior).
	DefaultEnv map[string]string

	// ToolNamePrefix replaces the root command name in tool names.
	// This is useful for shortening tool names to comply with MCP API limits (e.g., a 64-char tool name limit).
	// For example, if root command is "omnistrate-ctl" and ToolNamePrefix is "omctl",
	// a command "omnistrate-ctl cost by-cell list" becomes "omctl_cost_by-cell_list" instead of
	// "omnistrate-ctl_cost_by-cell_list".
	// If empty, the root command name is used as-is.
	ToolNamePrefix string

	// BaseURL is the base URL advertised by the SSE server (e.g. "http://localhost:8080").
	// Used by the stream command when the server sits behind a proxy; if empty,
	// the listen address is used.
	BaseURL string

	tools          []*mcp.Tool
	toolMetas      map[string]toolMeta // tool name → registration-time metadata
	toolSelectors  map[string]Selector // tool name → selector that owns it
	toolNamePrefix string              // resolved prefix (either ToolNamePrefix or root command name)
}

// commandName returns the configured CommandName, defaulting to "mcp".
func (c *Config) commandName() string {
	if c != nil && c.CommandName != "" {
		return c.CommandName
	}
	return "mcp"
}

// serveStdio 以 stdio 传输暴露 CLI 命令，直到客户端断开或 ctx 取消。
func (c *Config) serveStdio(cmd *cobra.Command) error {
	srv, err := c.newTransportServer(cmd, mcptransport.ServerTypeStdio, "")
	if err != nil {
		return err
	}

	if err := srv.Start(cmd.Context()); err != nil {
		return err
	}

	// stdio 场景下客户端断开（stdin EOF）即服务结束，无需等待进程信号。
	select {
	case <-cmd.Context().Done():
	case <-srv.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	return srv.Stop(shutdownCtx)
}

// serveHTTP 以 SSE 传输暴露 CLI 命令，直到 ctx 取消。
func (c *Config) serveHTTP(cmd *cobra.Command, addr string) error {
	srv, err := c.newTransportServer(cmd, mcptransport.ServerTypeSSE, addr)
	if err != nil {
		return err
	}

	if err := srv.Start(cmd.Context()); err != nil {
		return err
	}

	cmd.Printf("MCP SSE server listening on address %q\n", addr)
	baldlog.Info(cmd.Context(), "MCP SSE server listening", "addr", addr, "endpoint", srv.Endpoint())

	<-cmd.Context().Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	return srv.Stop(shutdownCtx)
}

// newTransportServer 发现命令树并构造 bald/transport/mcp 服务端：
// 协议、监听与停机都交给 transport 层，本包只负责把 Cobra 命令注册成工具。
// 命令树中若存在无法表示为 MCP 工具的命令，返回错误而不启动服务端。
func (c *Config) newTransportServer(cmd *cobra.Command, serverType mcptransport.ServerType, addr string) (*mcptransport.Server, error) {
	// 工具发现：填充 c.tools / c.toolMetas / c.toolSelectors。
	if err := c.registerTools(cmd); err != nil {
		return nil, err
	}

	rootCmd := cmd
	for rootCmd.Parent() != nil {
		rootCmd = rootCmd.Parent()
	}

	opts := []mcptransport.ServerOption{
		mcptransport.WithServerName(rootCmd.Name()),
		mcptransport.WithServerVersion(rootCmd.Version),
		mcptransport.WithMCPServeType(serverType),
		mcptransport.WithMCPServeAddress(addr),
	}
	if c.BaseURL != "" && serverType == mcptransport.ServerTypeSSE {
		opts = append(opts, mcptransport.WithSSEOptions(mcpserver.WithBaseURL(c.BaseURL)))
	}

	srv := mcptransport.NewServer(opts...)

	// 子进程模型：每次工具调用都以还原出的参数重新执行自身二进制。
	for _, tool := range c.tools {
		meta := c.toolMetas[tool.Name]
		if err := srv.RegisterHandler(*tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			input := decodeToolInput(req, meta)
			_, output, err := c.executeTool(ctx, req, input)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return toolOutputToResult(output), nil
		}); err != nil {
			baldlog.Error(cmd.Context(), "register subprocess tool failed", "tool", tool.Name, "error", err)
		}
	}

	return srv, nil
}

// executeTool 用工具注册期命中的 selector 执行一次工具调用
// （selector 的 Middleware 在这里生效）。
func (c *Config) executeTool(ctx context.Context, req mcp.CallToolRequest, input ToolInput) (*mcp.CallToolResult, ToolOutput, error) {
	sel, ok := c.toolSelectors[req.Params.Name]
	if !ok {
		sel = Selector{}
	}

	return sel.execute(ctx, req, input)
}

// registerTools 从命令树中发现工具，填充 c.tools / c.toolMetas / c.toolSelectors。
// 它不再创建 MCP 服务端——服务端由 newTransportServer 按每次 serve 调用的传输类型构造。
// 命令树中存在无法表示为 MCP 工具的命令（如位置参数与 flag 同名）时返回错误。
func (c *Config) registerTools(cmd *cobra.Command) error {
	// get root cmd
	rootCmd := cmd
	for rootCmd.Parent() != nil {
		rootCmd = rootCmd.Parent()
	}

	// resolve tool name prefix
	if c.ToolNamePrefix != "" {
		c.toolNamePrefix = c.ToolNamePrefix
	} else {
		c.toolNamePrefix = rootCmd.Name()
	}

	// reset tool metadata
	c.tools = nil
	c.toolMetas = make(map[string]toolMeta)
	c.toolSelectors = make(map[string]Selector)

	// ensure at least one selector exists for tool creation logic
	if len(c.Selectors) == 0 {
		c.Selectors = []Selector{{}}
	}

	// register tools
	return c.registerToolsRecursive(rootCmd)
}

// registerToolsRecursive explores a cmd tree, making tools recursively out of the provided cmd and its children.
// It returns an error if any command in the tree cannot be represented as an MCP tool.
func (c *Config) registerToolsRecursive(cmd *cobra.Command) error {
	// register all subcommands
	for _, subCmd := range cmd.Commands() {
		if err := c.registerToolsRecursive(subCmd); err != nil {
			return err
		}
	}

	// apply basic filters
	if c.cmdFilter(cmd) {
		return nil
	}

	// cycle through selectors until one matches the cmd
	for i, s := range c.Selectors {
		if s.CmdSelector != nil && !s.CmdSelector(cmd) {
			continue
		}

		// create tool from cmd — returns flat schema and per-tool metadata
		tool, meta, err := s.createToolFromCmd(cmd, c.toolNamePrefix)
		if err != nil {
			return err
		}
		baldlog.Debug(nil, "created tool", "tool_name", tool.Name, "selector_index", i)

		// add tool to manager's tool list (for `tools` command)
		c.tools = append(c.tools, tool)

		// store metadata and owning selector for the subprocess executor and REST server
		c.toolMetas[tool.Name] = meta
		c.toolSelectors[tool.Name] = s

		// only the first matching selector is used
		break
	}

	return nil
}

// cmdFilter returns true if cmd should be filtered out.
// It uses the configured CommandName (defaulting to "mcp") to exclude
// the cobramcp command group from being exposed as MCP tools.
func (c *Config) cmdFilter(cmd *cobra.Command) bool {
	if cmd.Hidden || cmd.Deprecated != "" {
		return true
	}

	if cmd.Run == nil && cmd.RunE == nil && cmd.PreRun == nil && cmd.PreRunE == nil {
		return true
	}

	// Match built-in commands by exact path segment (not substring) so that
	// legitimate commands such as "helper" or "mcp-tools" are not dropped.
	return hasPathSegment(cmd, c.commandName(), "help", "completion")
}

// decodeToolInput extracts a ToolInput from a mark3labs CallToolRequest.
// The MCP client sends all parameters as a flat top-level map.  meta carries
// the flag name set and arg spec slice that were computed at registration time
// and are required to reconstruct the cobra command arguments at execution time.
func decodeToolInput(req mcp.CallToolRequest, meta toolMeta) ToolInput {
	rawArgs := req.GetArguments()
	flat := make(map[string]any, len(rawArgs))
	for k, v := range rawArgs {
		flat[k] = v
	}

	argNames := make([]string, len(meta.argSpecs))
	for i, spec := range meta.argSpecs {
		argNames[i] = spec.Name
	}

	return ToolInput{
		CmdPath:   meta.cmdPath,
		FlatInput: flat,
		FlagNames: meta.flagNames,
		ArgNames:  argNames,
	}
}

// toolOutputToResult converts a ToolOutput into a *mcp.CallToolResult.
func toolOutputToResult(output ToolOutput) *mcp.CallToolResult {
	combined := output.StdOut
	if output.StdErr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += output.StdErr
	}
	if output.ExitCode != 0 {
		if combined == "" {
			combined = fmt.Sprintf("exit code %d", output.ExitCode)
		}
		return mcp.NewToolResultError(combined)
	}
	return mcp.NewToolResultText(combined)
}
