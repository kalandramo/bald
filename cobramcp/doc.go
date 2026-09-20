// Package cobramcp transforms Cobra CLI applications into MCP (Model Context Protocol) servers,
// enabling AI assistants to interact with command-line tools.
//
// cobramcp automatically converts existing Cobra commands into MCP tools, handling
// tool schema generation, command execution, and tool registration.
//
// # 与 bald 的关系
//
// 本包是 bald 生态的 "Cobra → MCP 工具" 桥接层，只回答"工具从哪来"：
//
//   - 协议实现与生命周期复用 [github.com/kalandramo/bald/transport/mcp]，
//     本包不再自建 MCP 服务端，也不自行捕获进程信号；
//   - 日志走 [github.com/kalandramo/bald/log] 契约，宿主（appkit/bootstrap）
//     决定后端；自带的 CLI 子命令会在运行期安装 stderr 后端。
//
// # Two Execution Models
//
// cobramcp supports two execution models:
//
//  1. Subprocess model ([Command] / [Config]): The MCP server re-invokes the
//     same binary as a subprocess for each tool call. This works well when all
//     necessary state can be reconstructed from CLI flags alone.
//
//  2. In-process model ([NewMCPServer] / [MCPServer]): The MCP server calls the
//     cobra.Command's Run/RunE function directly in-process. Because the command
//     closures already hold references to runtime dependencies (API clients,
//     database handles, business-logic objects, etc.), those objects are
//     available without any additional wiring. This is the preferred model when
//     commands depend on non-serialisable state.
//
// # In-Process Usage (NewMCPServer)
//
// Provide a factory function that produces a fresh *cobra.Command tree on each
// call. The factory is invoked once at registration time (read-only, for schema
// generation) and once per tool invocation at runtime. Using a factory ensures
// that every tool call gets its own Options structs, flag values, and
// closure-captured variables — eliminating all shared-state hazards and making
// concurrent tool calls fully independent.
//
//	package main
//
//	import (
//	    "context"
//	    "log"
//	    "github.com/kalandramo/bald/cobramcp"
//	)
//
//	func main() {
//	    // Dependencies are injected at construction time and shared safely
//	    // because they are read-only after initialisation.
//	    biz := newBotSreBiz(apiClient, dbClient)
//
//	    // Factory produces a fresh command tree per invocation.
//	    // Each call allocates new Options structs and closure state.
//	    factory := func() *cobra.Command {
//	        return buildRootCommand(biz) // adds all subcommands with biz in closures
//	    }
//
//	    srv, err := cobramcp.NewMCPServer(cobramcp.MCPOptions{
//	        Enabled: true,
//	        Addr:    ":8090",
//	        Name:    "myapp",
//	        Version: "1.0.0",
//	    }, factory)
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//
//	    // Start 阻塞直到 ctx 取消；信号与优雅停机由宿主负责。
//	    if err := srv.Start(ctx); err != nil {
//	        log.Fatal(err)
//	    }
//	}
//
// # Subprocess Usage (Command)
//
// Add MCP server management subcommands to an existing Cobra application:
//
//	package main
//
//	import (
//	    "os"
//	    "github.com/kalandramo/bald/cobramcp"
//	)
//
//	func main() {
//	    rootCmd := createMyRootCommand()
//
//	    // Adds: mcp start, mcp tools, mcp vscode enable/disable/list, etc.
//	    rootCmd.AddCommand(cobramcp.Command(nil))
//
//	    if err := rootCmd.Execute(); err != nil {
//	        os.Exit(1)
//	    }
//	}
//
// # Configuration (Subprocess model)
//
// The [Config] struct provides fine-grained control over which commands and
// flags are exposed as MCP tools through a selector system.
//
// Basic filters are always applied automatically:
//   - Hidden and deprecated commands/flags are excluded
//   - Commands without executable functions are excluded
//   - Built-in commands (mcp, help, completion) are excluded
//
// Example with selectors:
//
//	config := &cobramcp.Config{
//	    Selectors: []cobramcp.Selector{
//	        {
//	            CmdSelector:           cobramcp.AllowCmdsContaining("get", "list"),
//	            LocalFlagSelector:     cobramcp.AllowFlags("namespace", "output"),
//	            InheritedFlagSelector: cobramcp.NoFlags,
//	        },
//	        {
//	            CmdSelector:           cobramcp.AllowCmds("mycli delete"),
//	            LocalFlagSelector:     cobramcp.ExcludeFlags("all", "force"),
//	            InheritedFlagSelector: cobramcp.NoFlags,
//	        },
//	    },
//	}
//
// # Logging
//
// cobramcp 不持有自己的日志后端配置：所有日志都经 bald/log 契约输出。
// 进程内模型使用宿主装配的 Logger（未装配时为静默 nop）；自带的 CLI 子命令
// （mcp start / stream / rest / tools）会在运行期安装一个 stderr 后端，因为
// stdio 模式下 stdout 是 MCP 协议通道。
package cobramcp
