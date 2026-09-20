![Project Logo](./logo.png)

**Transform any Cobra CLI into an MCP server**

cobramcp automatically converts your Cobra commands into MCP tools, and provides CLI commands for integration with VSCode and Cursor.

> **Claude is not supported.** cobramcp does not manage Claude Desktop configuration
> (`mcp claude enable/disable/list` was removed). Use the `vscode` or `cursor`
> subcommands, or write your MCP client config manually.

## Quick Start

### Install

```bash
go get github.com/kalandramo/bald/cobramcp
```

### Add to your CLI

```go
package main

import (
    "os"
    "github.com/kalandramo/bald/cobramcp"
)

func main() {
    rootCmd := createMyRootCommand()
    rootCmd.AddCommand(cobramcp.Command(nil))

    if err := rootCmd.Execute(); err != nil {
        os.Exit(1)
    }
}
```

### Enable in VSCode or Cursor

```bash
# VSCode (requires Copilot in Agent Mode)
./my-cli mcp vscode enable

# Cursor
./my-cli mcp cursor enable
```

Claude Desktop is not supported — cobramcp provides no `claude` subcommand.

Your CLI commands are now available as MCP tools!

### Stream over HTTP

Expose your MCP server over HTTP for remote access:

```bash
./my-cli mcp stream --host localhost --port 8080
```

## Commands

The `cobramcp.Command(nil)` adds these subcommands to your CLI (the default command name is `mcp`, configurable via `Config.CommandName`):

```
mcp
├── start            # Start MCP server on stdio
├── stream           # Stream MCP server over HTTP
├── tools            # Export available MCP tools as JSON
├── vscode
│   ├── enable       # Add server to VSCode config
│   ├── disable      # Remove server from VSCode config
│   └── list         # List VSCode MCP servers
└── cursor
    ├── enable       # Add server to Cursor config
    ├── disable      # Remove server from Cursor config
    └── list         # List Cursor MCP servers
```

## Configuration

Control which commands and flags are exposed as MCP tools using selectors. By default, all commands and flags are exposed (except hidden/deprecated).

```go
config := &cobramcp.Config{
    Selectors: []cobramcp.Selector{
        {
            CmdSelector:           cobramcp.AllowCmdsContaining("get", "list"),
            LocalFlagSelector:     cobramcp.ExcludeFlags("token", "secret"),
            InheritedFlagSelector: cobramcp.NoFlags, // Exclude persistent flags

            // Middleware wraps command execution
            Middleware: func(ctx context.Context, req mcp.CallToolRequest, in cobramcp.ToolInput, next cobramcp.ExecuteFunc) (*mcp.CallToolResult, cobramcp.ToolOutput, error) {
                ctx, cancel := context.WithTimeout(ctx, time.Minute)
                defer cancel()
                return next(ctx, req, in)
            },
        },
    },
}

rootCmd.AddCommand(cobramcp.Command(config))
```

### Custom Command Name

By default the cobramcp command is named `mcp`. If your CLI already uses `mcp` for something else, set `CommandName` to avoid the collision:

```go
config := &cobramcp.Config{
    CommandName: "agent",
}

rootCmd.AddCommand(cobramcp.Command(config))
```

The command tree, editor config (`enable`/`disable`), and internal filters all use the configured name automatically.

### Default Environment Variables

Editors launch MCP server subprocesses with a minimal environment. On macOS this means a PATH of just `/usr/bin:/bin:/usr/sbin:/sbin`, so tools like `helm`, `kubectl`, or `docker` installed via mise/homebrew/nix won't be found. Use `DefaultEnv` to capture the current PATH (or any other variables) at `enable` time:

```go
config := &cobramcp.Config{
    DefaultEnv: map[string]string{
        "PATH": os.Getenv("PATH"),
    },
}

rootCmd.AddCommand(cobramcp.Command(config))
```

These are merged into the editor config written by `enable`. User-provided `--env` values take precedence on conflict.

See [docs/config.md](docs/config.md) for detailed configuration options.

## How It Works

cobramcp bridges Cobra commands and the Model Context Protocol:

1. **Command Discovery**: Recursively walks your Cobra command tree
2. **Schema Generation**: Creates flat JSON schemas from command flags and arguments ([docs/schema.md](docs/schema.md))
3. **Tool Execution**: Either re-executes your CLI as a subprocess or runs the cobra command in-process ([docs/execution.md](docs/execution.md))

## 与 bald 生态的关系

cobramcp 只回答"工具从哪来"，其余交给 bald：

| 关注点 | 归属 |
| --- | --- |
| MCP 协议实现、监听、优雅停机、`Endpoint()` | [`bald/transport/mcp`](../transport/mcp)（`Start` 同步绑定端口并返回真实地址） |
| 日志输出 | [`bald/log`](../log) 契约；进程内模型用宿主装配的 Logger，自带 CLI 子命令安装 stderr 后端 |
| 进程信号（SIGINT/SIGTERM）与生命周期编排 | 宿主（appkit / bootstrap）；本包不捕获信号 |
| 工具来源（Cobra 命令树 → MCP 工具） | **本包** |

进程内模型因此满足 bald 的 `transport.Server` 契约（`Start` 阻塞至 ctx 取消、`Stop` 优雅停机、`Endpoint` 返回真实监听地址），可直接交给 `appkit` 装配。

## Contributing

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md).
